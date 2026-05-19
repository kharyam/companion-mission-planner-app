package device

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// massStorageController backs a camera/drone whose storage is a mounted
// USB Mass Storage filesystem — most commonly an SD card in a reader,
// which is how a modern DJI drone's footage is reached (the Mini 5 Pro
// and similar expose Mass Storage, not MTP).
//
// It presents the same MediaBrowser surface as the MTP path, but every
// operation is a plain filesystem read: no libmtp, no cgo. The slot /
// transfer half of the Controller interface doesn't apply — the volume
// is mounted read-only — and those methods return errors.
type massStorageController struct {
	id     string
	model  string
	root   string // mountpoint to walk for media
	logger *slog.Logger

	// mu guards the cached media index, (re)built by ListMedia and
	// lazily by ensureIndex. mediaByID maps a synthetic numeric ID to a
	// file path; lrfByVideo maps a video's ID to its .LRF proxy path.
	mu         sync.Mutex
	mediaByID  map[uint32]string
	lrfByVideo map[uint32]string
}

func newMassStorageController(id, model, root string, logger *slog.Logger) *massStorageController {
	return &massStorageController{id: id, model: model, root: root, logger: logger}
}

func (c *massStorageController) Info() Info {
	model := c.model
	if model == "" {
		model = "USB storage"
	}
	return Info{
		ID:             c.id,
		Model:          model,
		ConnectionType: ConnUSB,
		Authorized:     true,
		State:          "online",
		Kind:           KindCamera,
	}
}

// --- Controller slot surface: not applicable to a read-only volume ---------

func (c *massStorageController) ListSlots() ([]Slot, error) {
	return nil, fmt.Errorf("USB storage volume has no waypoint slots")
}

func (c *massStorageController) ReadPreview(string) (io.ReadCloser, error) {
	return nil, ErrPreviewNotFound
}

func (c *massStorageController) WriteKMZ(string, io.Reader, *PreviewMetadata) (*TransferResult, error) {
	return nil, fmt.Errorf("USB storage volume is mounted read-only")
}

func (c *massStorageController) ClearSlot(string) error {
	return fmt.Errorf("USB storage volume is mounted read-only")
}

// --- MediaBrowser ----------------------------------------------------------

// ListMedia walks the mounted volume for photos and videos, newest
// first, rebuilding the media index as a side effect.
func (c *massStorageController) ListMedia() ([]MediaItem, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scanLocked()
}

// scanLocked rebuilds the media index. Caller must hold mu.
func (c *massStorageController) scanLocked() ([]MediaItem, error) {
	type found struct {
		path  string
		kind  string
		size  int64
		mtime time.Time
	}
	var files []found
	lrf := map[string]string{} // dir+base -> .LRF path

	err := filepath.WalkDir(c.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep walking
		}
		if d.IsDir() {
			name := d.Name()
			// Skip hidden and OS-bookkeeping directories.
			if p != c.root && (strings.HasPrefix(name, ".") || strings.EqualFold(name, "System Volume Information")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(p), ".lrf") {
			lrf[fsProxyKey(p)] = p
			return nil
		}
		kind := mediaKind(d.Name())
		if kind == "" {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		files = append(files, found{p, kind, info.Size(), info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(files, func(i, j int) bool { return files[i].mtime.After(files[j].mtime) })

	c.mediaByID = map[uint32]string{}
	c.lrfByVideo = map[uint32]string{}
	items := make([]MediaItem, 0, len(files))
	for i, f := range files {
		id := uint32(i + 1)
		c.mediaByID[id] = f.path
		item := MediaItem{
			ID:         strconv.FormatUint(uint64(id), 10),
			Name:       filepath.Base(f.path),
			Kind:       f.kind,
			Size:       f.size,
			ModifiedAt: f.mtime,
		}
		if f.kind == "video" {
			if proxy, ok := lrf[fsProxyKey(f.path)]; ok {
				item.HasPreview = true
				c.lrfByVideo[id] = proxy
			}
		}
		items = append(items, item)
	}
	return items, nil
}

// ensureIndex builds the media index once if it hasn't been built yet,
// so the read paths work even if called before any ListMedia.
func (c *massStorageController) ensureIndex() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mediaByID != nil {
		return nil
	}
	_, err := c.scanLocked()
	return err
}

// pathFor resolves a media ID to its absolute file path.
func (c *massStorageController) pathFor(id string) (string, error) {
	oid, err := parseObjectID(id)
	if err != nil {
		return "", err
	}
	if err := c.ensureIndex(); err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.mediaByID[oid]
	if !ok {
		return "", ErrMediaNotFound
	}
	return p, nil
}

// ReadMedia streams the full original file and returns its filename.
func (c *massStorageController) ReadMedia(id string, w io.Writer) (string, error) {
	p, err := c.pathFor(id)
	if err != nil {
		return "", err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return filepath.Base(p), err
}

// ReadVideoPreview streams a video's sibling .LRF proxy clip.
func (c *massStorageController) ReadVideoPreview(id string, w io.Writer) (string, error) {
	oid, err := parseObjectID(id)
	if err != nil {
		return "", err
	}
	if err := c.ensureIndex(); err != nil {
		return "", err
	}
	c.mu.Lock()
	proxy, ok := c.lrfByVideo[oid]
	c.mu.Unlock()
	if !ok {
		return "", ErrMediaNotFound
	}
	f, err := os.Open(proxy)
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return filepath.Base(proxy), err
}

// ReadThumbnail returns a photo's embedded EXIF thumbnail, read from
// just the head of the file. Videos have no cheap thumbnail here, so
// ErrThumbnailNotFound is returned and the UI falls back to an icon.
func (c *massStorageController) ReadThumbnail(id string) ([]byte, error) {
	p, err := c.pathFor(id)
	if err != nil {
		return nil, err
	}
	if mediaKind(filepath.Base(p)) != "photo" {
		return nil, ErrThumbnailNotFound
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	head := make([]byte, exifProbeBytes)
	n, _ := io.ReadFull(f, head)
	if n > 0 {
		if t := extractEXIFThumbnail(head[:n]); len(t) > 0 {
			return t, nil
		}
	}
	return nil, ErrThumbnailNotFound
}

// fsProxyKey identifies a video and its .LRF sibling as the same clip:
// same directory, same base name (extension stripped, case-insensitive).
func fsProxyKey(p string) string {
	base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
	return filepath.Dir(p) + "/" + strings.ToUpper(base)
}
