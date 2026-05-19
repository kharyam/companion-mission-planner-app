package device

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/kmz"
	"github.com/kamdynamics/kam-transfer/internal/mtp"
)

// mtpController wraps an open *mtp.Device with the Controller interface
// the API and registry use. Path navigation is cached for the lifetime
// of the controller (which the registry keeps across Refreshes via
// r.mtpControllers), so the expensive locateWaypointDir walk only runs
// once per device connection.
type mtpController struct {
	dev    *mtp.Device
	logger *slog.Logger

	// locateMu guards everything related to the classify background
	// walk. Reads of kind / waypointDir / previewDir / mediaRoot from
	// outside must take this lock; once locateDone is closed the values
	// are stable for the lifetime of the controller.
	locateMu      sync.Mutex
	kind          string // KindController | KindCamera | KindUnknown
	waypointDir   *mtp.FileEntry
	previewDir    *mtp.FileEntry
	mediaRoot     *mtp.FileEntry // DCIM folder, set when kind == KindCamera
	locateStarted bool
	locateDone    chan struct{} // closed when walk finishes (success or fail)
	locateErr     error
	onLocated     func() // optional: invoked after walk completes

	// mediaMu guards the cached media index, rebuilt by ListMedia and
	// lazily by ensureMediaIndex. mediaByID maps an object ID to its
	// file entry; lrfByVideo maps a video's object ID to its .LRF proxy.
	mediaMu    sync.Mutex
	mediaByID  map[uint32]mtp.FileEntry
	lrfByVideo map[uint32]mtp.FileEntry
}

func newMTPController(d *mtp.Device, logger *slog.Logger, onLocated func()) *mtpController {
	return &mtpController{dev: d, logger: logger, onLocated: onLocated}
}

// Info is non-blocking: it returns whatever classify state has been
// resolved so far. The walk runs in a goroutine started by kickoffLocate
// (called eagerly when the controller is created), and the controller's
// onLocated callback emits a WebSocket event when the walk completes so
// the UI re-fetches once the device's Kind is known. Until then Kind is
// reported as "unknown" and the UI shows an "identifying device" state.
func (c *mtpController) Info() Info {
	// Mirror the ADB modelLabel fallback — without it, devices that
	// libmtp opens but doesn't surface a Friendly string for produce an
	// empty Model in /api/devices, which downstream consumers (e.g. the
	// planner's transfer-history schema requiring deviceName ≥ 1 char)
	// then reject.
	model := c.dev.Friendly
	if model == "" {
		model = "MTP device"
	}
	c.locateMu.Lock()
	kind := c.kind
	c.locateMu.Unlock()
	if kind == "" {
		kind = KindUnknown
	}
	return Info{
		ID:             c.dev.Identifier,
		Model:          model,
		ConnectionType: ConnMTP,
		// MTP doesn't have an auth dance: if the device shows up,
		// it's already accessible.
		Authorized:     true,
		State:          "online",
		Kind:           kind,
		DJIFlyDetected: kind == KindController,
	}
}

// isLocating reports whether the background locateWaypointDir walk
// is currently running. Used by Refresh to skip the Ping liveness
// check while the walk is holding d.mu — the walk's own USB traffic
// is sufficient proof the handle is alive, and Ping would otherwise
// serialize behind the walk's LookupPath (which holds d.mu for the
// entire multi-second path traversal).
func (c *mtpController) isLocating() bool {
	c.locateMu.Lock()
	defer c.locateMu.Unlock()
	if !c.locateStarted || c.locateDone == nil {
		return false
	}
	select {
	case <-c.locateDone:
		return false
	default:
		return true
	}
}

// kickoffLocate starts the locateWaypointDir walk in a goroutine if
// it hasn't been started yet. Safe to call repeatedly and concurrently
// — only the first call spawns the goroutine. Returns immediately;
// callers that need the result should use awaitLocate.
func (c *mtpController) kickoffLocate() {
	c.locateMu.Lock()
	if c.locateStarted {
		c.locateMu.Unlock()
		return
	}
	c.locateStarted = true
	c.locateDone = make(chan struct{})
	c.locateMu.Unlock()
	go c.runLocate()
}

// awaitLocate ensures the walk has started, then blocks until it
// finishes. Used by methods that genuinely need waypointDir before
// they can do their work (ListSlots, ReadKMZ, WriteKMZ, etc.).
func (c *mtpController) awaitLocate() error {
	c.kickoffLocate()
	c.locateMu.Lock()
	done := c.locateDone
	c.locateMu.Unlock()
	<-done
	c.locateMu.Lock()
	defer c.locateMu.Unlock()
	return c.locateErr
}

func (c *mtpController) runLocate() {
	err := c.classify()
	c.locateMu.Lock()
	c.locateErr = err
	done := c.locateDone
	c.locateMu.Unlock()
	close(done)
	if c.onLocated != nil {
		c.onLocated()
	}
}

// classify walks the device's storages once and decides what kind of
// device this is: a controller (DJI Fly's Android waypoint tree is
// present), a camera/drone (a DCIM folder is present), or unknown. It
// populates c.kind plus c.waypointDir/c.previewDir (controller) or
// c.mediaRoot (camera).
//
// It returns an error only for genuine I/O failures — a device that is
// simply neither a controller nor a camera resolves to KindUnknown with
// a nil error. Callers should go through kickoffLocate / awaitLocate so
// the work is properly serialized.
//
// The DJI Fly Android layout is the same on every controller, but the
// top-level storage name differs ("Internal shared storage", localized
// variants, etc.) — so we walk every storage and check each one.
func (c *mtpController) classify() error {
	start := time.Now()
	storages, err := c.dev.ListDir(nil)
	if err != nil {
		return fmt.Errorf("list storages: %w", err)
	}
	c.logger.Debug("mtp storages enumerated", "count", len(storages))
	// Try the most likely controller storage first; "disk"-type
	// secondary entries rarely carry the Android tree.
	ordered := append([]mtp.FileEntry(nil), storages...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return storagePriority(ordered[i].Name) > storagePriority(ordered[j].Name)
	})

	var dcimRoot *mtp.FileEntry
	for i := range ordered {
		s := ordered[i]
		children, err := c.dev.ListDir(&s)
		if err != nil {
			return err
		}
		// Controller? Descend the DJI Fly waypoint tree, but only if an
		// Android/ folder is even present — otherwise skip the multi-
		// segment PTP walk that would just fall through with ErrNotFound.
		if findIn(children, "Android") != nil {
			const relative = "Android/data/dji.go.v5/files/waypoint"
			full := s.Name + "/" + relative
			entry, err := c.dev.LookupPath(full)
			switch {
			case err == nil:
				var preview *mtp.FileEntry
				if p, perr := findChild(c.dev, entry, "map_preview"); perr == nil && p != nil {
					preview = p
				}
				c.locateMu.Lock()
				c.kind = KindController
				c.waypointDir = entry
				c.previewDir = preview
				c.locateMu.Unlock()
				c.logger.Info("classified MTP device as controller", "path", full, "elapsed", time.Since(start))
				return nil
			case errors.Is(err, mtp.ErrNotFound):
				c.logger.Debug("waypoint dir not on this storage", "path", full)
			default:
				return err
			}
		}
		// Camera? Remember the first DCIM folder we see, but keep
		// scanning — a later storage could still be a controller, which
		// wins.
		if dcimRoot == nil {
			if dcim := findIn(children, "DCIM"); dcim != nil {
				cp := *dcim
				dcimRoot = &cp
			}
		}
	}

	if dcimRoot != nil {
		c.locateMu.Lock()
		c.kind = KindCamera
		c.mediaRoot = dcimRoot
		c.locateMu.Unlock()
		c.logger.Info("classified MTP device as camera", "storage", dcimRoot.StorageID, "elapsed", time.Since(start))
		return nil
	}

	c.locateMu.Lock()
	c.kind = KindUnknown
	c.locateMu.Unlock()
	c.logger.Warn("MTP device is neither a controller nor a camera", "elapsed", time.Since(start))
	return nil
}

// findIn returns the first entry whose name matches (case-insensitively)
// from an already-fetched listing, or nil. The returned pointer aliases
// the slice element; copy it if the slice may be reused.
func findIn(entries []mtp.FileEntry, name string) *mtp.FileEntry {
	for i := range entries {
		if strings.EqualFold(entries[i].Name, name) {
			return &entries[i]
		}
	}
	return nil
}

// storagePriority ranks MTP storage names so the most likely place to
// find the DJI Fly Android tree is tried first. Higher returns sort
// earlier in classify's iteration.
func storagePriority(name string) int {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "internal") && strings.Contains(lower, "shared"):
		return 3
	case strings.Contains(lower, "internal"):
		return 2
	case strings.Contains(lower, "disk"):
		return 0
	default:
		return 1
	}
}

func (c *mtpController) ListSlots() ([]Slot, error) {
	if err := c.awaitLocate(); err != nil {
		return nil, err
	}
	if c.waypointDir == nil {
		return nil, fmt.Errorf("DJI Fly waypoint folder not found on device: %w", mtp.ErrNotFound)
	}
	entries, err := c.dev.ListDir(c.waypointDir)
	if err != nil {
		return nil, err
	}
	// Build a set of preview-subfolder names so we can answer
	// previewAvailable in one pass. DJI Fly's layout is
	// map_preview/<GUID>/<GUID>.jpg — children of map_preview/ are
	// folders named after the GUID. We treat presence of the folder
	// (and a JPEG inside it) as "has preview".
	previewFolders := map[string]mtp.FileEntry{}
	if c.previewDir != nil {
		pentries, _ := c.dev.ListDir(c.previewDir)
		for _, p := range pentries {
			if p.IsFolder {
				previewFolders[strings.ToUpper(p.Name)] = p
			}
		}
	}
	slots := make([]Slot, 0, len(entries))
	for _, e := range entries {
		if !e.IsFolder || !looksLikeGUID(e.Name) {
			continue
		}
		// Stat the inner <GUID>/<GUID>.kmz to populate size + mtime.
		var size int64
		mtime := e.ModifiedAt
		kmzChildren, _ := c.dev.ListDir(&e)
		for _, kc := range kmzChildren {
			if strings.EqualFold(kc.Name, e.Name+".kmz") {
				size = kc.Size
				if kc.ModifiedAt.After(mtime) {
					mtime = kc.ModifiedAt
				}
				break
			}
		}
		previewExists := false
		if folder, ok := previewFolders[strings.ToUpper(e.Name)]; ok {
			// Look up the JPEG inside the per-slot folder and roll its
			// mtime into the slot's LastModified. Without this, a
			// /preview/regenerate (which only touches the JPEG) leaves
			// the API's lastModified unchanged, so the UI's cache-bust
			// query param doesn't change, so browsers serve the stale
			// thumbnail forever.
			if jpgMtime, ok := c.previewJPEGMtime(&folder, e.Name); ok {
				previewExists = true
				if jpgMtime.After(mtime) {
					mtime = jpgMtime
				}
			}
		}
		slots = append(slots, Slot{
			GUID:             e.Name,
			Name:             "Slot " + e.Name[:8],
			LastModified:     mtime,
			FileSize:         size,
			PreviewAvailable: previewExists,
		})
	}
	return slots, nil
}

// hasPreviewFile checks whether the JPEG inside a map_preview/<GUID>/
// folder exists. Cheap one-time listing per slot, only called when the
// folder itself is present.
func (c *mtpController) hasPreviewFile(folder *mtp.FileEntry, guid string) bool {
	_, ok := c.previewJPEGMtime(folder, guid)
	return ok
}

// previewJPEGMtime returns the modification time of map_preview/<GUID>/
// <GUID>.jpg if it exists. Used to roll the JPEG mtime into the slot's
// LastModified so cache-busting URLs change after /preview/regenerate.
func (c *mtpController) previewJPEGMtime(folder *mtp.FileEntry, guid string) (time.Time, bool) {
	children, err := c.dev.ListDir(folder)
	if err != nil {
		return time.Time{}, false
	}
	want := strings.ToUpper(guid + ".jpg")
	for _, ch := range children {
		if !ch.IsFolder && strings.ToUpper(ch.Name) == want {
			return ch.ModifiedAt, true
		}
	}
	return time.Time{}, false
}

func (c *mtpController) ReadPreview(guid string) (io.ReadCloser, error) {
	if err := c.awaitLocate(); err != nil {
		return nil, err
	}
	if c.previewDir == nil {
		return nil, ErrPreviewNotFound
	}
	// Navigate into map_preview/<GUID>/, then read <GUID>.jpg inside.
	subFolder, err := findChild(c.dev, c.previewDir, guid)
	if err != nil || subFolder == nil || !subFolder.IsFolder {
		return nil, ErrPreviewNotFound
	}
	jpg, err := findChild(c.dev, subFolder, guid+".jpg")
	if err != nil || jpg == nil {
		return nil, ErrPreviewNotFound
	}
	pr, pw := io.Pipe()
	entry := *jpg
	go func() {
		err := c.dev.GetFile(&entry, pw)
		_ = pw.CloseWithError(err)
	}()
	return pr, nil
}

func (c *mtpController) WriteKMZ(guid string, kmz io.Reader, meta *PreviewMetadata) (*TransferResult, error) {
	if err := c.awaitLocate(); err != nil {
		return nil, err
	}
	// Find the slot folder. DJI Fly creates these via placeholder
	// missions in the app; we don't create them ourselves because
	// fresh GUIDs aren't recognized.
	slotFolder, err := findChild(c.dev, c.waypointDir, guid)
	if err != nil {
		return nil, fmt.Errorf("slot %s: %w", guid, ErrSlotNotFound)
	}
	// MTP doesn't have an in-place "replace" op — we delete the old
	// KMZ (if present) and upload fresh. DJI Fly tolerates this.
	kmzName := guid + ".kmz"
	if existing, _ := findChild(c.dev, slotFolder, kmzName); existing != nil {
		if err := c.dev.Delete(existing); err != nil {
			return nil, fmt.Errorf("delete existing kmz: %w", err)
		}
	}
	// Buffer to count size — MTP needs the size up front.
	buf, size, err := bufferReader(kmz)
	if err != nil {
		return nil, err
	}
	if _, err := c.dev.PutFile(slotFolder, kmzName, size, buf); err != nil {
		return nil, fmt.Errorf("put kmz: %w", err)
	}
	return &TransferResult{
		Success:       true,
		GUID:          guid,
		FileSize:      size,
		TransferredAt: time.Now(),
	}, nil
}

// WaypointImage is one rendered per-waypoint JPEG bound for the
// slot's image/ subfolder.
type WaypointImage struct {
	Index int    // 0-based waypoint position
	Name  string // filename, e.g. "WP_kam_3_<ts>.jpg"
	Bytes []byte // JPEG payload
}

// WriteWaypointImages replaces the slot's image/ contents with the
// provided per-waypoint JPEGs and a regenerated ShotSnap.json mapping
// filename → waypoint index. Existing WP_*.jpg files are deleted so
// DJI Fly doesn't show stale drone photos alongside our renders.
func (c *mtpController) WriteWaypointImages(guid string, images []WaypointImage) error {
	if err := c.awaitLocate(); err != nil {
		return err
	}
	slotFolder, err := findChild(c.dev, c.waypointDir, guid)
	if err != nil || slotFolder == nil {
		return fmt.Errorf("slot %s: %w", guid, ErrSlotNotFound)
	}
	imageFolder, err := findChild(c.dev, slotFolder, "image")
	if err != nil || imageFolder == nil {
		return fmt.Errorf("slot %s has no image/ folder; DJI Fly creates it on placeholder init", guid)
	}
	// Delete any existing WP_*.jpg files + ShotSnap.json so we have a
	// clean slate. Skip anything that isn't a regular file (defensive).
	existing, err := c.dev.ListDir(imageFolder)
	if err != nil {
		return fmt.Errorf("list image/: %w", err)
	}
	for i := range existing {
		ch := existing[i]
		if ch.IsFolder {
			continue
		}
		upper := strings.ToUpper(ch.Name)
		if strings.HasPrefix(upper, "WP_") && strings.HasSuffix(upper, ".JPG") {
			_ = c.dev.Delete(&ch)
		}
		if strings.EqualFold(ch.Name, "ShotSnap.json") {
			_ = c.dev.Delete(&ch)
		}
	}
	// Push new WP_*.jpg files.
	for _, img := range images {
		if _, err := c.dev.PutFile(imageFolder, img.Name, int64(len(img.Bytes)), bytes.NewReader(img.Bytes)); err != nil {
			return fmt.Errorf("put %s: %w", img.Name, err)
		}
	}
	// Build ShotSnap.json: {"POI_POINT":{},"WAY_POINT":{"WP_xxx.jpg":0,...}}.
	shotSnap := map[string]any{"POI_POINT": map[string]any{}}
	wayPoint := map[string]int{}
	for _, img := range images {
		wayPoint[img.Name] = img.Index
	}
	shotSnap["WAY_POINT"] = wayPoint
	jsonBytes, err := json.Marshal(shotSnap)
	if err != nil {
		return err
	}
	if _, err := c.dev.PutFile(imageFolder, "ShotSnap.json", int64(len(jsonBytes)), bytes.NewReader(jsonBytes)); err != nil {
		return fmt.Errorf("put ShotSnap.json: %w", err)
	}
	return nil
}

// ReadKMZ streams the slot's <GUID>.kmz back to the caller. Used by
// Registry.RegeneratePreview to re-render the preview without making
// the user re-upload from disk.
func (c *mtpController) ReadKMZ(guid string, w io.Writer) error {
	if err := c.awaitLocate(); err != nil {
		return err
	}
	slotFolder, err := findChild(c.dev, c.waypointDir, guid)
	if err != nil || slotFolder == nil {
		return fmt.Errorf("slot %s: %w", guid, ErrSlotNotFound)
	}
	kmzEntry, err := findChild(c.dev, slotFolder, guid+".kmz")
	if err != nil || kmzEntry == nil {
		return fmt.Errorf("kmz for %s not found on device", guid)
	}
	return c.dev.GetFile(kmzEntry, w)
}

func (c *mtpController) WritePreview(guid string, jpg io.Reader) error {
	if err := c.awaitLocate(); err != nil {
		return err
	}
	if c.previewDir == nil {
		// DJI Fly creates the map_preview directory itself; if it's
		// missing, the waypoint feature hasn't been initialized yet.
		return fmt.Errorf("map_preview folder not yet present on device")
	}
	// Real on-disk layout is map_preview/<GUID>/<GUID>.jpg. DJI Fly
	// creates the per-slot subfolder when it generates its own
	// thumbnail for a placeholder, so it should already exist.
	subFolder, err := findChild(c.dev, c.previewDir, guid)
	if err != nil {
		return fmt.Errorf("locate preview subfolder: %w", err)
	}
	if subFolder == nil {
		return fmt.Errorf("preview subfolder %s does not exist (DJI Fly creates it on placeholder init)", guid)
	}
	previewName := guid + ".jpg"
	if existing, _ := findChild(c.dev, subFolder, previewName); existing != nil {
		_ = c.dev.Delete(existing)
	}
	buf, size, err := bufferReader(jpg)
	if err != nil {
		return err
	}
	_, err = c.dev.PutFile(subFolder, previewName, size, buf)
	return err
}

// ClearSlot resets a slot back to a placeholder state: replaces the
// KMZ with a minimal one-waypoint mission (centered on whatever the
// previous mission's first waypoint was, falling back to 0,0), wipes
// the per-waypoint image folder, and deletes the preview JPEG so DJI
// Fly regenerates one. The slot itself stays — DJI Fly created it and
// only DJI Fly can really delete the GUID. After ClearSlot the slot
// shows up as a fresh, mostly-empty entry in the mission list.
func (c *mtpController) ClearSlot(guid string) error {
	if err := c.awaitLocate(); err != nil {
		return err
	}
	slotFolder, err := findChild(c.dev, c.waypointDir, guid)
	if err != nil || slotFolder == nil {
		return fmt.Errorf("slot %s: %w", guid, ErrSlotNotFound)
	}

	// Try to preserve the previous mission's first-waypoint location
	// so the placeholder lands somewhere sensible (the user's last
	// flight area). Falls back to (0,0) if we can't read it.
	var lat, lng float64
	if existing, _ := findChild(c.dev, slotFolder, guid+".kmz"); existing != nil {
		var buf bytes.Buffer
		if err := c.dev.GetFile(existing, &buf); err == nil {
			if m, err := kmz.ExtractMission(bytes.NewReader(buf.Bytes()), int64(buf.Len())); err == nil && len(m.Waypoints) > 0 {
				lat = m.Waypoints[0].Lat
				lng = m.Waypoints[0].Lng
			}
		}
	}

	placeholderBytes, err := kmz.PlaceholderKMZ(lat, lng)
	if err != nil {
		return fmt.Errorf("build placeholder kmz: %w", err)
	}

	// Replace the KMZ.
	kmzName := guid + ".kmz"
	if existing, _ := findChild(c.dev, slotFolder, kmzName); existing != nil {
		if err := c.dev.Delete(existing); err != nil {
			return fmt.Errorf("delete existing kmz: %w", err)
		}
	}
	if _, err := c.dev.PutFile(slotFolder, kmzName, int64(len(placeholderBytes)), bytes.NewReader(placeholderBytes)); err != nil {
		return fmt.Errorf("write placeholder kmz: %w", err)
	}

	// Wipe everything in image/ (drone photos + WP renders + ShotSnap).
	// Leaving stale photos misaligned with a one-waypoint mission would
	// be confusing in DJI Fly's editor.
	if imageFolder, _ := findChild(c.dev, slotFolder, "image"); imageFolder != nil {
		children, err := c.dev.ListDir(imageFolder)
		if err == nil {
			for i := range children {
				ch := children[i]
				if !ch.IsFolder {
					_ = c.dev.Delete(&ch)
				}
			}
		}
	}

	// Delete the preview JPEG. DJI Fly regenerates one on next view.
	if c.previewDir != nil {
		if subFolder, _ := findChild(c.dev, c.previewDir, guid); subFolder != nil {
			if existing, _ := findChild(c.dev, subFolder, guid+".jpg"); existing != nil {
				_ = c.dev.Delete(existing)
			}
		}
	}
	return nil
}

// findChild returns the immediate child of parent whose name matches
// (case-insensitively). nil entry + nil error means not found.
func findChild(d *mtp.Device, parent *mtp.FileEntry, name string) (*mtp.FileEntry, error) {
	entries, err := d.ListDir(parent)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if strings.EqualFold(entries[i].Name, name) {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// bufferReader reads all of r into memory and reports its size. The
// 10 MB cap that the KMZ API layer already enforces is what keeps this
// from being problematic.
func bufferReader(r io.Reader) (io.Reader, int64, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(data), int64(len(data)), nil
}

// --- MediaBrowser implementation -------------------------------------------

// exifProbeBytes is how much of a photo's head we pull to extract the
// embedded EXIF thumbnail. Camera EXIF thumbnails sit near the start of
// the file, well within this window.
const exifProbeBytes = 256 * 1024

// mediaScanMaxDepth caps the DCIM tree recursion. Real camera layouts
// are DCIM/<album>/<file> — two levels — so this is generous.
const mediaScanMaxDepth = 8

// ListMedia walks the camera's DCIM tree and returns every photo and
// video, newest first. It rebuilds the controller's media index as a
// side effect so later ReadMedia / ReadThumbnail / ReadVideoPreview
// calls resolve object IDs without re-walking.
func (c *mtpController) ListMedia() ([]MediaItem, error) {
	if err := c.awaitLocate(); err != nil {
		return nil, err
	}
	if c.kind != KindCamera || c.mediaRoot == nil {
		return nil, ErrMediaUnavailable
	}
	c.mediaMu.Lock()
	defer c.mediaMu.Unlock()
	return c.scanMediaLocked()
}

// scanMediaLocked rebuilds the media index. Caller must hold mediaMu.
func (c *mtpController) scanMediaLocked() ([]MediaItem, error) {
	files, err := c.collectFiles(c.mediaRoot, 0)
	if err != nil {
		return nil, err
	}
	// Index .LRF proxy clips by parent + base name so each video can be
	// paired with its low-res preview.
	lrf := map[string]mtp.FileEntry{}
	for _, f := range files {
		if strings.EqualFold(path.Ext(f.Name), ".lrf") {
			lrf[proxyKey(f.ParentID, f.Name)] = f
		}
	}
	c.mediaByID = map[uint32]mtp.FileEntry{}
	c.lrfByVideo = map[uint32]mtp.FileEntry{}
	items := make([]MediaItem, 0, len(files))
	for _, f := range files {
		kind := mediaKind(f.Name)
		if kind == "" {
			continue // skip .lrf / .srt / json sidecars
		}
		c.mediaByID[f.ObjectID] = f
		item := MediaItem{
			ID:         strconv.FormatUint(uint64(f.ObjectID), 10),
			Name:       f.Name,
			Kind:       kind,
			Size:       f.Size,
			ModifiedAt: f.ModifiedAt,
		}
		if kind == "video" {
			if proxy, ok := lrf[proxyKey(f.ParentID, f.Name)]; ok {
				item.HasPreview = true
				c.lrfByVideo[f.ObjectID] = proxy
			}
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].ModifiedAt.After(items[j].ModifiedAt)
	})
	return items, nil
}

// collectFiles recursively gathers every non-folder entry under folder.
func (c *mtpController) collectFiles(folder *mtp.FileEntry, depth int) ([]mtp.FileEntry, error) {
	if depth > mediaScanMaxDepth {
		return nil, nil
	}
	entries, err := c.dev.ListDir(folder)
	if err != nil {
		return nil, err
	}
	var out []mtp.FileEntry
	for i := range entries {
		e := entries[i]
		if e.IsFolder {
			sub, err := c.collectFiles(&e, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// ensureMediaIndex builds the media index if it hasn't been built yet.
// ListMedia always rebuilds; the read paths use this so they still work
// if called before any ListMedia (e.g. a direct API hit after restart).
func (c *mtpController) ensureMediaIndex() error {
	if err := c.awaitLocate(); err != nil {
		return err
	}
	if c.kind != KindCamera || c.mediaRoot == nil {
		return ErrMediaUnavailable
	}
	c.mediaMu.Lock()
	defer c.mediaMu.Unlock()
	if c.mediaByID != nil {
		return nil
	}
	_, err := c.scanMediaLocked()
	return err
}

// mediaEntry resolves a media object ID to its file entry.
func (c *mtpController) mediaEntry(id string) (mtp.FileEntry, error) {
	oid, err := parseObjectID(id)
	if err != nil {
		return mtp.FileEntry{}, err
	}
	if err := c.ensureMediaIndex(); err != nil {
		return mtp.FileEntry{}, err
	}
	c.mediaMu.Lock()
	defer c.mediaMu.Unlock()
	e, ok := c.mediaByID[oid]
	if !ok {
		return mtp.FileEntry{}, ErrMediaNotFound
	}
	return e, nil
}

// ReadMedia streams the full original file for a media object into w
// and returns its on-device filename.
func (c *mtpController) ReadMedia(id string, w io.Writer) (string, error) {
	e, err := c.mediaEntry(id)
	if err != nil {
		return "", err
	}
	return e.Name, c.dev.GetObjectTo(e.ObjectID, w)
}

// ReadVideoPreview streams a video's sibling .LRF proxy clip into w.
// Returns ErrMediaNotFound if the video has no proxy.
func (c *mtpController) ReadVideoPreview(id string, w io.Writer) (string, error) {
	oid, err := parseObjectID(id)
	if err != nil {
		return "", err
	}
	if err := c.ensureMediaIndex(); err != nil {
		return "", err
	}
	c.mediaMu.Lock()
	proxy, ok := c.lrfByVideo[oid]
	c.mediaMu.Unlock()
	if !ok {
		return "", ErrMediaNotFound
	}
	return proxy.Name, c.dev.GetObjectTo(proxy.ObjectID, w)
}

// ReadThumbnail returns a small JPEG preview for a media object. It
// tries the device's own MTP thumbnail first; for photos with none it
// falls back to the JPEG's embedded EXIF thumbnail, pulled from just the
// head of the file. Returns ErrThumbnailNotFound when neither yields one.
func (c *mtpController) ReadThumbnail(id string) ([]byte, error) {
	e, err := c.mediaEntry(id)
	if err != nil {
		return nil, err
	}
	if thumb, terr := c.dev.GetThumbnail(e.ObjectID); terr == nil && len(thumb) > 0 {
		return thumb, nil
	}
	if mediaKind(e.Name) == "photo" {
		head, perr := c.dev.GetPartialObject(e.ObjectID, 0, exifProbeBytes)
		if perr == nil {
			if t := extractEXIFThumbnail(head); len(t) > 0 {
				return t, nil
			}
		}
	}
	return nil, ErrThumbnailNotFound
}

// proxyKey identifies a video and its .LRF proxy as the same clip:
// same parent folder, same base name (extension stripped).
func proxyKey(parentID uint32, name string) string {
	base := strings.TrimSuffix(name, path.Ext(name))
	return strconv.FormatUint(uint64(parentID), 10) + "/" + strings.ToUpper(base)
}

// mediaKind classifies a filename as "photo", "video", or "" (not media).
func mediaKind(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".dng", ".png", ".heic", ".heif", ".webp", ".tif", ".tiff", ".raw":
		return "photo"
	case ".mp4", ".mov", ".m4v", ".avi", ".mkv":
		return "video"
	default:
		return ""
	}
}

// parseObjectID parses a decimal MTP object ID. A malformed ID is
// reported as ErrMediaNotFound — there is no such object either way.
func parseObjectID(id string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(id), 10, 32)
	if err != nil {
		return 0, ErrMediaNotFound
	}
	return uint32(n), nil
}
