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

// ErrPartialUnsupported is returned by GetPartialObject when the
// device's PTP stack can't serve partial-object reads. Callers should
// fall back gracefully (e.g. skip the EXIF-thumbnail shortcut).
var ErrPartialUnsupported = errors.New("mtp: device does not support partial object reads")

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

	// USBBus / USBDev are the bus_location and devnum reported by libmtp
	// at enumeration time. They identify the *current* USB address, so a
	// replug (which renumbers the device) produces a different pair —
	// that's how the detector tells "same hardware as before" from "this
	// is a fresh connection that needs its own Open call." Stable for
	// the lifetime of a single connection.
	USBBus uint32
	USBDev uint32

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

// GetObjectTo streams the MTP object with the given ID into w. Unlike
// GetFile it needs only the object ID, not a full FileEntry — handy for
// the media browser, whose download URLs carry just the ID.
func (d *Device) GetObjectTo(objectID uint32, w io.Writer) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return getObjectTo(d, objectID, w)
}

// GetThumbnail returns the device-stored thumbnail (typically JPEG) for
// an object, or ErrNotFound if the device serves none. This is the
// cheap path: no full-file transfer.
func (d *Device) GetThumbnail(objectID uint32) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return getThumbnail(d, objectID)
}

// GetPartialObject reads up to maxBytes starting at offset from an
// object. Used to pull just the head of a JPEG so its embedded EXIF
// thumbnail can be extracted without downloading the whole photo.
// Returns ErrPartialUnsupported when the device can't do partial reads.
func (d *Device) GetPartialObject(objectID uint32, offset uint64, maxBytes uint32) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return getPartialObject(d, objectID, offset, maxBytes)
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

// Ping issues a wire-touching PTP request and reports whether the
// device is still responding. Used by the detector to validate a
// previously-opened handle before reusing it across a Refresh — libmtp
// caches storage / device-info on Open and keeps that cache in memory
// after the underlying USB device disappears, so a structural check
// like "do we have any cached storages?" lies about dead handles.
// Returns nil iff the device is still on the bus and answered.
func (d *Device) Ping() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return pingDevice(d)
}
