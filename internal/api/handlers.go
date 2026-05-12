package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/device"
	"github.com/kamdynamics/kam-transfer/internal/kmz"
	"github.com/kamdynamics/kam-transfer/internal/version"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.registry.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devs})
}

func (s *Server) handleListSlots(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	slots, err := s.registry.ListSlots(r.Context(), deviceID)
	if err != nil {
		s.handleRegistryError(w, err)
		return
	}
	for i := range slots {
		slots[i].PreviewURL = "/api/devices/" + deviceID + "/slots/" + slots[i].GUID + "/preview"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceId": deviceID,
		"slots":    slots,
	})
}

func (s *Server) handleReadPreview(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	guid := pathParam(r, "guid")
	if !kmz.IsValidGUID(guid) {
		writeError(w, http.StatusBadRequest, CodeInvalidGUID, "guid is malformed", map[string]any{"guid": guid})
		return
	}
	rc, err := s.registry.ReadPreview(deviceID, guid)
	if err != nil {
		s.handleRegistryError(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, rc)
}

func (s *Server) handleTransfer(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	guid := pathParam(r, "guid")
	if !kmz.IsValidGUID(guid) {
		writeError(w, http.StatusBadRequest, CodeInvalidGUID, "guid is malformed", map[string]any{"guid": guid})
		return
	}

	if err := r.ParseMultipartForm(kmz.MaxSize); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "could not parse multipart form: "+err.Error(), nil)
		return
	}
	file, header, err := r.FormFile("kmz")
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "missing kmz field", nil)
		return
	}
	defer file.Close()
	if header.Size > kmz.MaxSize {
		writeError(w, http.StatusRequestEntityTooLarge, CodeKMZTooLarge, "kmz exceeds size cap", map[string]any{"size": header.Size, "max": kmz.MaxSize})
		return
	}

	// Slurp into memory; with a 10 MB cap this is fine and lets us
	// validate + rewrite before touching the device.
	raw, err := io.ReadAll(io.LimitReader(file, kmz.MaxSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "read upload: "+err.Error(), nil)
		return
	}
	if int64(len(raw)) > kmz.MaxSize {
		writeError(w, http.StatusRequestEntityTooLarge, CodeKMZTooLarge, "kmz exceeds size cap", nil)
		return
	}

	if _, err := kmz.Inspect(bytes.NewReader(raw), int64(len(raw))); err != nil {
		writeError(w, http.StatusBadRequest, CodeKMZInvalid, err.Error(), nil)
		return
	}

	rewritten, err := kmz.RewriteForGUID(bytes.NewReader(raw), int64(len(raw)), guid)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeKMZInvalid, "rewrite failed: "+err.Error(), nil)
		return
	}

	name := r.FormValue("name")
	meta := &device.PreviewMetadata{Name: name, Date: time.Now()}
	if raw := r.FormValue("previewMetadata"); raw != "" {
		if err := json.Unmarshal([]byte(raw), meta); err != nil {
			writeError(w, http.StatusBadRequest, CodeBadRequest, "previewMetadata is not valid JSON: "+err.Error(), nil)
			return
		}
		// Preserve name override even if JSON didn't include it.
		if meta.Name == "" {
			meta.Name = name
		}
	}

	res, err := s.registry.TransferWithMeta(r.Context(), deviceID, guid, bytes.NewReader(rewritten), meta)
	if err != nil {
		s.handleRegistryError(w, err)
		return
	}

	s.broadcast(Event{Type: "transfer.completed", Device: deviceID, Slot: guid, At: time.Now()})
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleClearSlot(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	guid := pathParam(r, "guid")
	if !kmz.IsValidGUID(guid) {
		writeError(w, http.StatusBadRequest, CodeInvalidGUID, "guid is malformed", map[string]any{"guid": guid})
		return
	}
	if err := s.registry.ClearSlot(r.Context(), deviceID, guid); err != nil {
		s.handleRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "guid": guid})
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.registry.Refresh(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRegistryError maps registry errors onto API codes.
func (s *Server) handleRegistryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, device.ErrUnknownDevice):
		writeError(w, http.StatusNotFound, CodeDeviceNotFound, err.Error(), nil)
	case errors.Is(err, device.ErrPreviewNotFound):
		writeError(w, http.StatusNotFound, CodeSlotNotFound, "preview not found", nil)
	case errors.Is(err, device.ErrSlotNotFound):
		writeError(w, http.StatusNotFound, CodeSlotNotFound, err.Error(), nil)
	default:
		// Default: surface as internal error. As we identify more failure
		// modes (auth revoked, disk full, etc.), add Is-checks above.
		writeError(w, http.StatusInternalServerError, CodeTransferFailed, err.Error(), nil)
	}
}

// pathParam extracts a {name} segment from net/http 1.22+ pattern routing.
// We use r.PathValue, which is available on Go 1.22+.
func pathParam(r *http.Request, name string) string {
	return strings.TrimSpace(r.PathValue(name))
}

