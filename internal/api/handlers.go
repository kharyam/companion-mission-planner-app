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

	// Optional: push per-waypoint images right after the KMZ + main
	// preview. Triggered by the UI checkbox; off by default so the
	// transfer stays fast unless the user wants the extras.
	pushWP := strings.EqualFold(strings.TrimSpace(r.FormValue("pushWaypointImages")), "true")
	if pushWP {
		if n, perr := s.registry.PushWaypointImages(r.Context(), deviceID, guid); perr != nil {
			s.logger.Warn("waypoint images push failed", "guid", guid, "err", perr)
		} else {
			s.logger.Info("waypoint images pushed", "guid", guid, "count", n)
		}
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

// handleInspectKMZ parses an uploaded KMZ and returns the waypoints +
// mission metadata in the exact JSON shape the transfer endpoint
// accepts as `previewMetadata`. The UI calls this when the user picks
// a file so the previewMetadata field can be auto-populated.
func (s *Server) handleInspectKMZ(w http.ResponseWriter, r *http.Request) {
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
	raw, err := io.ReadAll(io.LimitReader(file, kmz.MaxSize+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "read upload: "+err.Error(), nil)
		return
	}
	mission, err := kmz.ExtractMission(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeKMZInvalid, err.Error(), nil)
		return
	}

	// Reshape into the exact previewMetadata schema the transfer
	// handler expects, so the UI can plug it straight into the form.
	type wp struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	}
	resp := struct {
		Name      string `json:"name,omitempty"`
		Date      string `json:"date,omitempty"`
		Waypoints []wp   `json:"waypoints"`
		Author    string `json:"author,omitempty"`
		Source    string `json:"source,omitempty"`
		Count     int    `json:"count"`
	}{
		Name:   strings.TrimSpace(strings.TrimSuffix(header.Filename, ".kmz")),
		Author: mission.Author,
		Source: mission.Source,
		Count:  len(mission.Waypoints),
	}
	if mission.Name != "" {
		resp.Name = mission.Name
	}
	if mission.Date != nil {
		resp.Date = mission.Date.UTC().Format(time.RFC3339)
	}
	for _, p := range mission.Waypoints {
		resp.Waypoints = append(resp.Waypoints, wp{Lat: p.Lat, Lng: p.Lng})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSetSlotName persists a user-assigned name. The slot's GUID
// doesn't actually have to exist on the device — names are stored
// host-side, so the user can pre-name a slot before plugging in.
func (s *Server) handleSetSlotName(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	guid := pathParam(r, "guid")
	if !kmz.IsValidGUID(guid) {
		writeError(w, http.StatusBadRequest, CodeInvalidGUID, "guid is malformed", map[string]any{"guid": guid})
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "body must be {\"name\":\"...\"}", nil)
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if err := s.registry.SetSlotName(deviceID, guid, body.Name); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deviceId": deviceID, "guid": guid, "name": body.Name})
}

func (s *Server) handleClearSlotName(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	guid := pathParam(r, "guid")
	if !kmz.IsValidGUID(guid) {
		writeError(w, http.StatusBadRequest, CodeInvalidGUID, "guid is malformed", map[string]any{"guid": guid})
		return
	}
	if err := s.registry.SetSlotName(deviceID, guid, ""); err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deviceId": deviceID, "guid": guid, "name": ""})
}

// handleRegeneratePreview pulls the slot's KMZ off the device, renders
// a fresh preview from it, and pushes the new JPEG. The UI exposes
// this so users can recover after DJI Fly's editor-Save trigger
// clobbers our previous preview push.
func (s *Server) handleRegeneratePreview(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	guid := pathParam(r, "guid")
	if !kmz.IsValidGUID(guid) {
		writeError(w, http.StatusBadRequest, CodeInvalidGUID, "guid is malformed", map[string]any{"guid": guid})
		return
	}
	if err := s.registry.RegeneratePreview(r.Context(), deviceID, guid); err != nil {
		s.handleRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceId": deviceID,
		"guid":     guid,
		"ok":       true,
		"at":       time.Now().UTC().Format(time.RFC3339),
	})
}

// handlePushWaypointImages renders one small satellite tile per
// waypoint and writes them into the slot's image/ folder, along with
// a regenerated ShotSnap.json. DJI Fly displays these next to each
// waypoint in its mission editor.
func (s *Server) handlePushWaypointImages(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	guid := pathParam(r, "guid")
	if !kmz.IsValidGUID(guid) {
		writeError(w, http.StatusBadRequest, CodeInvalidGUID, "guid is malformed", map[string]any{"guid": guid})
		return
	}
	n, err := s.registry.PushWaypointImages(r.Context(), deviceID, guid)
	if err != nil {
		s.handleRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceId": deviceID,
		"guid":     guid,
		"count":    n,
		"ok":       true,
		"at":       time.Now().UTC().Format(time.RFC3339),
	})
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

