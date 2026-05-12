package device

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/mtp"
)

// mtpController wraps an open *mtp.Device with the Controller interface
// the API and registry use. Path navigation is cached per-Refresh so
// repeated slot listings don't re-walk the tree.
type mtpController struct {
	dev    *mtp.Device
	logger *slog.Logger

	// Cached: the FileEntry for the .../waypoint directory. Nil until
	// we successfully look it up, which we lazy-do on first use.
	waypointDir *mtp.FileEntry
	previewDir  *mtp.FileEntry
}

func newMTPController(d *mtp.Device, logger *slog.Logger) *mtpController {
	return &mtpController{dev: d, logger: logger}
}

func (c *mtpController) Info() Info {
	return Info{
		ID:             c.dev.Identifier,
		Model:          c.dev.Friendly,
		ConnectionType: ConnMTP,
		// MTP doesn't have an auth dance: if the device shows up,
		// it's already accessible. DJIFlyDetected mirrors whether
		// the waypoint folder is reachable.
		Authorized:     true,
		State:          "online",
		DJIFlyDetected: c.locateWaypointDir() == nil,
	}
}

// locateWaypointDir walks the MTP tree to find Android/data/dji.go.v5/
// files/waypoint. Returns nil on success (with side-effect of populating
// c.waypointDir + c.previewDir), or an error if the structure doesn't
// match what DJI Fly expects.
//
// DJI Fly uses the same Android layout on every controller, but the
// top-level storage name differs ("Internal shared storage", "Internal
// storage", localized variants, etc.) — so we walk every storage and
// pick whichever one has the Android/data tree.
func (c *mtpController) locateWaypointDir() error {
	if c.waypointDir != nil {
		return nil
	}
	storages, err := c.dev.ListDir(nil)
	if err != nil {
		return fmt.Errorf("list storages: %w", err)
	}
	c.logger.Debug("mtp storages enumerated", "count", len(storages))
	for _, s := range storages {
		c.logger.Debug("mtp storage", "name", s.Name, "id", s.StorageID)
	}
	const relative = "Android/data/dji.go.v5/files/waypoint"
	var attempted []string
	for i := range storages {
		full := storages[i].Name + "/" + relative
		attempted = append(attempted, full)
		entry, err := c.dev.LookupPath(full)
		if err != nil {
			if errors.Is(err, mtp.ErrNotFound) {
				c.logger.Debug("waypoint dir not on this storage", "path", full)
				continue
			}
			return err
		}
		c.waypointDir = entry
		previewPath := full + "/map_preview"
		if p, err := c.dev.LookupPath(previewPath); err == nil {
			c.previewDir = p
		}
		c.logger.Info("located DJI Fly waypoint folder", "path", full, "object_id", entry.ObjectID)
		return nil
	}
	c.logger.Warn("DJI Fly waypoint folder not found", "tried", attempted)
	return fmt.Errorf("DJI Fly waypoint folder not found on any storage: %w", mtp.ErrNotFound)
}

func (c *mtpController) ListSlots() ([]Slot, error) {
	if err := c.locateWaypointDir(); err != nil {
		return nil, err
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
			previewExists = c.hasPreviewFile(&folder, e.Name)
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
	children, err := c.dev.ListDir(folder)
	if err != nil {
		return false
	}
	want := strings.ToUpper(guid + ".jpg")
	for _, ch := range children {
		if !ch.IsFolder && strings.ToUpper(ch.Name) == want {
			return true
		}
	}
	return false
}

func (c *mtpController) ReadPreview(guid string) (io.ReadCloser, error) {
	if err := c.locateWaypointDir(); err != nil {
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
	if err := c.locateWaypointDir(); err != nil {
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

// ReadKMZ streams the slot's <GUID>.kmz back to the caller. Used by
// Registry.RegeneratePreview to re-render the preview without making
// the user re-upload from disk.
func (c *mtpController) ReadKMZ(guid string, w io.Writer) error {
	if err := c.locateWaypointDir(); err != nil {
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
	if err := c.locateWaypointDir(); err != nil {
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

func (c *mtpController) ClearSlot(guid string) error {
	return fmt.Errorf("clear-slot not yet implemented for MTP (see TROUBLESHOOTING.md)")
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
