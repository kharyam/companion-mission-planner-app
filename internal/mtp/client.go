// Package mtp is the MTP transport for DJI devices that don't expose
// ADB — most notably the consumer DJI RC 2, which ships with developer
// options stripped out and connects in MTP mode by default.
//
// The real implementation lives in client_linux.go behind a `linux &&
// cgo` build tag and binds directly to libmtp. On other platforms (or
// when cgo is disabled) Open returns ErrUnavailable, the ADB transport
// keeps working, and the binary still cross-compiles cleanly.
//
// To build with MTP support on Linux:
//
//	sudo dnf install libmtp libmtp-devel        # Fedora
//	sudo apt install libmtp-dev libmtp-runtime  # Debian/Ubuntu
//	CGO_ENABLED=1 go build ./cmd/kam-transfer
//
// macOS and Windows backends are not yet wired up. macOS will use the
// same libmtp via Homebrew; Windows will need a WPD/IPortableDevice
// implementation in client_windows.go.
package mtp

import (
	"errors"
	"io"
	"sync"
	"time"
)

// ErrUnavailable is returned by Open when MTP isn't compiled into this
// build (no cgo, no libmtp on the host, or unsupported platform).
var ErrUnavailable = errors.New("MTP transport unavailable in this build")

// ErrNotFound is returned when a requested path doesn't exist on the
// device. Callers translate this into the SLOT_NOT_FOUND API error.
var ErrNotFound = errors.New("mtp: path not found")

// Device is the public handle on an open MTP device. The concrete type
// lives in the platform-specific file; this header just lets callers
// keep an opaque reference.
type Device struct {
	// Identifier is the libmtp serial-number string (or a fallback
	// composed from vendor:product if the device declines to serve one).
	// Stable across reconnects.
	Identifier string

	// Friendly is the libmtp "friendly name" — usually the manufacturer-
	// chosen display name like "DJI RC 2".
	Friendly string

	// Vendor / Product are the USB IDs, useful for filtering to DJI.
	Vendor  uint16
	Product uint16

	// mu serializes every method that touches libmtp through this
	// handle. libmtp is not thread-safe — concurrent calls into the
	// same device pointer can SIGSEGV inside the C library (we caught
	// this with two simultaneous Get_Files_And_Folders). All public
	// methods acquire mu around their cgo path.
	mu sync.Mutex

	// impl carries the platform-specific state (libmtp pointers on
	// linux+cgo, nil on stub builds). Access via the receiver methods
	// in the build-tagged files.
	impl deviceImpl
}

// FileEntry mirrors libmtp's LIBMTP_file_t for the fields we care about.
type FileEntry struct {
	ObjectID   uint32
	StorageID  uint32
	ParentID   uint32
	Name       string
	Size       int64
	ModifiedAt time.Time
	IsFolder   bool
}

// Client is the package-level façade. Construct once at startup; List
// is safe to call repeatedly to refresh.
type Client struct{}

// New returns a Client. It does not touch the bus until List is called.
func New() *Client { return &Client{} }

// List returns currently-connected MTP devices. On unsupported builds
// it returns an empty slice (not an error) so callers can fall through
// to ADB without special-casing.
func (c *Client) List() ([]*Device, error) {
	return listDevices()
}

// Open returns a handle ready for I/O. Caller must Close.
func (c *Client) Open(d *Device) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return openDevice(d)
}

// Close releases the libmtp handle. Idempotent.
func (d *Device) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return closeDevice(d)
}

// LookupPath resolves an absolute MTP path (e.g.
// "Internal shared storage/Android/data/dji.go.v5/files/waypoint")
// into the FileEntry for that node. Returns ErrNotFound if any segment
// is missing.
func (d *Device) LookupPath(p string) (*FileEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return lookupPath(d, p)
}

// ListDir returns the immediate children of folder, which must itself
// be a folder. To list the device root, pass a zero FileEntry (libmtp
// treats folder.ObjectID == 0 + StorageID == 0 as "all roots").
func (d *Device) ListDir(folder *FileEntry) ([]FileEntry, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return listDir(d, folder)
}

// GetFile streams an MTP object into w.
func (d *Device) GetFile(entry *FileEntry, w io.Writer) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return getFile(d, entry, w)
}

// PutFile uploads r as a child of parent with the given name and size.
// MTP requires the size up front, so callers must know it before
// streaming. Returns the new object ID.
func (d *Device) PutFile(parent *FileEntry, name string, size int64, r io.Reader) (uint32, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return putFile(d, parent, name, size, r)
}

// Delete removes an object (file or folder). Folders must be empty.
func (d *Device) Delete(entry *FileEntry) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return deleteObject(d, entry)
}
