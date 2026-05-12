package device

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/mtp"
)

// TreeEntry is one node in a recursive MTP listing. Path is the slash-
// separated absolute path (with storage name as the first segment),
// matching what mtp.LookupPath accepts.
type TreeEntry struct {
	Path     string
	Size     int64
	Mtime    time.Time
	IsFolder bool
	Depth    int
}

// ErrNotMTP means the requested device isn't reachable over MTP. Probe
// helpers only support MTP today — ADB has its own shell-based tooling.
var ErrNotMTP = errors.New("device is not an MTP device")

// WalkTree returns every entry under rootPath up to maxDepth levels
// deep (maxDepth <= 0 means unlimited). It's intended for debugging /
// research, not as a hot-path API.
func (r *Registry) WalkTree(deviceID, rootPath string, maxDepth int) ([]TreeEntry, error) {
	dev, err := r.mtpDevice(deviceID)
	if err != nil {
		return nil, err
	}
	root, err := dev.LookupPath(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", rootPath, err)
	}
	var out []TreeEntry
	out = append(out, TreeEntry{
		Path:     rootPath,
		Size:     root.Size,
		Mtime:    root.ModifiedAt,
		IsFolder: root.IsFolder,
		Depth:    0,
	})
	if !root.IsFolder {
		return out, nil
	}
	if err := walkRecursive(dev, root, rootPath, 1, maxDepth, &out); err != nil {
		return out, err
	}
	// Sort deterministically: depth, then path. Easier to eyeball output.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func walkRecursive(dev *mtp.Device, folder *mtp.FileEntry, parentPath string, depth, maxDepth int, acc *[]TreeEntry) error {
	if maxDepth > 0 && depth > maxDepth {
		return nil
	}
	children, err := dev.ListDir(folder)
	if err != nil {
		return fmt.Errorf("list %q: %w", parentPath, err)
	}
	for i := range children {
		c := children[i]
		full := parentPath + "/" + c.Name
		*acc = append(*acc, TreeEntry{
			Path:     full,
			Size:     c.Size,
			Mtime:    c.ModifiedAt,
			IsFolder: c.IsFolder,
			Depth:    depth,
		})
		if c.IsFolder {
			if err := walkRecursive(dev, &c, full, depth+1, maxDepth, acc); err != nil {
				// keep going past errors; partial trees are still useful
				*acc = append(*acc, TreeEntry{Path: full + " <ERR: " + err.Error() + ">", Depth: depth})
			}
		}
	}
	return nil
}

// ReadDeviceFile streams the file at path into w. Used by `probe-cat`
// to dump candidate metadata files inline.
func (r *Registry) ReadDeviceFile(deviceID, path string, w io.Writer) error {
	dev, err := r.mtpDevice(deviceID)
	if err != nil {
		return err
	}
	entry, err := dev.LookupPath(path)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", path, err)
	}
	if entry.IsFolder {
		return fmt.Errorf("%q is a folder", path)
	}
	return dev.GetFile(entry, w)
}

// mtpDevice returns the open *mtp.Device for deviceID, or an error if
// the device isn't currently open over MTP.
func (r *Registry) mtpDevice(deviceID string) (*mtp.Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if dev, ok := r.openMTP[deviceID]; ok {
		return dev, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotMTP, deviceID)
}
