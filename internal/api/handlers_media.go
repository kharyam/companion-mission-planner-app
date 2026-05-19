package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/kamdynamics/kam-transfer/internal/device"
)

// handleListMedia enumerates the photos and videos on a connected
// camera/drone and returns them newest-first, each with the URLs the UI
// uses to fetch its thumbnail, preview, and full download.
func (s *Server) handleListMedia(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	items, err := s.registry.ListMedia(r.Context(), deviceID)
	if err != nil {
		s.handleRegistryError(w, err)
		return
	}
	base := "/api/devices/" + deviceID + "/media/"
	for i := range items {
		items[i].DownloadURL = base + items[i].ID
		items[i].ThumbnailURL = base + items[i].ID + "/thumbnail"
		if items[i].HasPreview {
			items[i].PreviewURL = base + items[i].ID + "/preview"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deviceId": deviceID,
		"items":    items,
	})
}

// handleMediaThumbnail streams a small JPEG preview for a media object.
// A 404 here is normal — it just means the device served no thumbnail
// and (for videos) there's no EXIF fallback — and the UI shows an icon.
func (s *Server) handleMediaThumbnail(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	fileID := pathParam(r, "fileId")
	if !isObjectID(fileID) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "fileId must be a numeric MTP object ID", nil)
		return
	}
	data, err := s.registry.ReadMediaThumbnail(deviceID, fileID)
	if err != nil {
		s.handleRegistryError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// handleMediaPreview streams a video for in-browser playback.
//
// For a device whose media live on a mounted volume (a card reader),
// the file is served straight from disk via http.ServeContent — the
// browser gets HTTP range support and streams/seeks without downloading
// the whole clip. A sibling .LRF proxy is used when present; otherwise
// the original video streams directly (recent DJI drones such as the
// Mini 5 Pro no longer write .LRF proxies).
//
// MTP devices have no local file, so their .LRF proxy is pulled into a
// disk cache first and that copy is served.
func (s *Server) handleMediaPreview(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	fileID := pathParam(r, "fileId")
	if !isObjectID(fileID) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "fileId must be a numeric MTP object ID", nil)
		return
	}
	// Mass storage: serve the on-disk file directly (Range support).
	if path, err := s.registry.MediaPreviewPath(deviceID, fileID); err == nil {
		s.serveMediaFile(w, r, path)
		return
	} else if !errors.Is(err, device.ErrMediaUnavailable) {
		s.handleRegistryError(w, err)
		return
	}
	// MTP: serve the cached .LRF proxy copy.
	cached, err := s.cachedVideoPreview(deviceID, fileID)
	if err != nil {
		s.handleRegistryError(w, err)
		return
	}
	s.serveMediaFile(w, r, cached)
}

// serveMediaFile streams a local file with http.ServeContent — Range
// requests, conditional GETs and seeking are all handled by the stdlib.
func (s *Server) serveMediaFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "open media file: "+err.Error(), nil)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternalError, "stat media file: "+err.Error(), nil)
		return
	}
	w.Header().Set("Content-Type", videoContentType(path))
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}

// videoContentType picks a Content-Type from the file extension. A .LRF
// proxy is an H.264/MP4 stream despite its extension.
func videoContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mov":
		return "video/quicktime"
	default: // .mp4, .m4v, .lrf
		return "video/mp4"
	}
}

// handleDownloadMedia streams the full original photo or video to the
// browser as a downloadable attachment. The saved filename comes from
// the ?name= query (the UI passes the on-device name); without one we
// fall back to a fileId-based name. Mirrors handleDownloadKMZ — once the
// 200 headers are out, a mid-stream failure can only be logged.
func (s *Server) handleDownloadMedia(w http.ResponseWriter, r *http.Request) {
	deviceID := pathParam(r, "deviceId")
	fileID := pathParam(r, "fileId")
	if !isObjectID(fileID) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "fileId must be a numeric MTP object ID", nil)
		return
	}
	filename := "media-" + fileID
	if n := strings.TrimSpace(r.URL.Query().Get("name")); n != "" {
		filename = sanitizeFilename(n)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if _, err := s.registry.ReadMedia(deviceID, fileID, w); err != nil {
		s.logger.Warn("media download failed", "deviceId", deviceID, "fileId", fileID, "err", err)
	}
}

// cachedVideoPreview returns the on-disk path of a video's .LRF proxy,
// pulling it off the device on first request. The cache is keyed by
// device + MTP object ID; a device.disconnected event purges the
// device's whole cache directory (object IDs don't survive a reconnect).
func (s *Server) cachedVideoPreview(deviceID, fileID string) (string, error) {
	dir := filepath.Join(s.mediaCacheDir, sanitizeFilename(deviceID))
	dst := filepath.Join(dir, fileID+".mp4")
	if fi, err := os.Stat(dst); err == nil && fi.Size() > 0 {
		return dst, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Download into a temp file first, then rename — so a concurrent or
	// interrupted pull can never leave a half-written file at dst.
	tmp, err := os.CreateTemp(dir, "lrf-*.part")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	_, readErr := s.registry.ReadVideoPreview(deviceID, fileID, tmp)
	closeErr := tmp.Close()
	if readErr != nil {
		_ = os.Remove(tmpName)
		return "", readErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return "", closeErr
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return dst, nil
}

// isObjectID reports whether s is a non-empty run of decimal digits —
// the shape of an MTP object ID.
func isObjectID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
