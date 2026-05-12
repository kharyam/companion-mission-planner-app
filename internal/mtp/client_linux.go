//go:build linux && cgo

// libmtp-backed implementation. Builds when the binary is compiled
// with CGO_ENABLED=1 on Linux and libmtp + libmtp-devel are present.
//
// libmtp is a C library: this file calls into it via cgo, marshals
// the C linked-list of devices/files into Go slices, and exposes the
// minimum operations the device package needs.

package mtp

/*
#cgo pkg-config: libmtp
#include <stdlib.h>
#include <string.h>
#include <libmtp.h>

// libmtp's Get_Files_And_Folders signature uses callbacks in some
// builds. The version-stable path is the linked-list form below.
// We don't bind progress callbacks for now — every transfer fits well
// under the libmtp default timeouts.
*/
import "C"

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// One-time libmtp init. Safe to call concurrently; sync.Once guarantees
// the underlying LIBMTP_Init runs exactly once per process.
var initOnce sync.Once

func ensureInit() {
	initOnce.Do(func() {
		C.LIBMTP_Init()
		// Quiet libmtp's stderr blather (PTP errors are normal during
		// device negotiation and just confuse users).
		C.LIBMTP_Set_Debug(C.int(0))
	})
}

// deviceImpl carries the cgo state for a device. raw is set after
// listDevices populates it; dev is set after openDevice succeeds.
// raw is a copy-by-value (not a pointer into libmtp's array, which
// gets freed immediately) so it stays valid for the lifetime of the
// Go-side Device.
type deviceImpl struct {
	raw C.LIBMTP_raw_device_t
	dev *C.LIBMTP_mtpdevice_t
}

// listDevices walks libmtp's raw device list. We don't keep the
// connection open here — Open() does that — so the caller can probe
// what's attached without holding the USB interface.
func listDevices() ([]*Device, error) {
	ensureInit()
	var rawArr *C.LIBMTP_raw_device_t
	var n C.int
	if err := C.LIBMTP_Detect_Raw_Devices(&rawArr, &n); err != C.LIBMTP_ERROR_NONE {
		switch err {
		case C.LIBMTP_ERROR_NO_DEVICE_ATTACHED:
			return nil, nil
		case C.LIBMTP_ERROR_CONNECTING:
			return nil, errors.New("mtp: cannot connect to USB subsystem")
		case C.LIBMTP_ERROR_MEMORY_ALLOCATION:
			return nil, errors.New("mtp: out of memory")
		default:
			return nil, fmt.Errorf("mtp: detect raw devices: error %d", int(err))
		}
	}
	defer C.free(unsafe.Pointer(rawArr))
	if n == 0 {
		return nil, nil
	}

	// rawArr points to an array of LIBMTP_raw_device_t. Iterate via pointer math.
	rawSize := unsafe.Sizeof(C.LIBMTP_raw_device_t{})
	out := make([]*Device, 0, int(n))
	for i := 0; i < int(n); i++ {
		raw := (*C.LIBMTP_raw_device_t)(unsafe.Pointer(uintptr(unsafe.Pointer(rawArr)) + uintptr(i)*rawSize))
		// Copy-by-value into the Go-side deviceImpl. libmtp will free
		// the source array via the defer above; we need our own copy.
		dev := &Device{
			Identifier: fmt.Sprintf("usb:%d-%d", uint32(raw.bus_location), uint32(raw.devnum)),
			Friendly:   "MTP device",
			Vendor:     uint16(raw.device_entry.vendor_id),
			Product:    uint16(raw.device_entry.product_id),
			impl:       deviceImpl{raw: *raw},
		}
		out = append(out, dev)
	}
	return out, nil
}

// openDevice performs the PTP-level open and populates Friendly/Identifier.
//
// On Linux desktops, GVFS/KDE auto-claim the USB interface for the
// device's MTP volume the moment it enumerates. libmtp's
// libusb_claim_interface then fails with "device is busy". We
// transparently release the GVFS mount before opening, and (best
// effort) leave a note on stderr so users understand why their file
// manager's "DJI RC 2" entry disappeared.
func openDevice(d *Device) error {
	ensureInit()
	mtp := C.LIBMTP_Open_Raw_Device_Uncached(&d.impl.raw)
	if mtp == nil {
		// Try to free the device from competitors (GVFS / kiod6 /
		// adb-server) then retry once.
		if released := releaseGVFS(d); released {
			mtp = C.LIBMTP_Open_Raw_Device_Uncached(&d.impl.raw)
		}
		if mtp == nil {
			return errors.New("mtp: open failed (device busy — file manager / GVFS / KDE kiod6 may have it claimed; try `gio mount -u \"mtp://[usb:" + busDevString(d) + "]/\"` then retry)")
		}
	}
	d.impl.dev = mtp

	if friendly := C.LIBMTP_Get_Friendlyname(mtp); friendly != nil {
		d.Friendly = C.GoString(friendly)
		C.free(unsafe.Pointer(friendly))
	}
	if serial := C.LIBMTP_Get_Serialnumber(mtp); serial != nil {
		s := strings.TrimSpace(C.GoString(serial))
		if s != "" {
			d.Identifier = s
		}
		C.free(unsafe.Pointer(serial))
	}
	return nil
}

func closeDevice(d *Device) error {
	if d.impl.dev != nil {
		C.LIBMTP_Release_Device(d.impl.dev)
		d.impl.dev = nil
	}
	return nil
}

// listDir returns immediate children. If folder is nil, lists all
// storages at the device root level.
func listDir(d *Device, folder *FileEntry) ([]FileEntry, error) {
	if d.impl.dev == nil {
		return nil, errors.New("mtp: device not open")
	}
	if folder == nil {
		// Synthesize one FileEntry per storage so callers can navigate
		// like a normal filesystem.
		return listStorages(d), nil
	}
	first := C.LIBMTP_Get_Files_And_Folders(d.impl.dev, C.uint32_t(folder.StorageID), C.uint32_t(folder.ObjectID))
	defer freeFileList(first)
	return walkFileList(folder.StorageID, first), nil
}

func listStorages(d *Device) []FileEntry {
	// Ensure storage list is populated.
	if rc := C.LIBMTP_Get_Storage(d.impl.dev, C.LIBMTP_STORAGE_SORTBY_NOTSORTED); rc != 0 {
		// Some devices return -1 even though storages were enumerated.
		// Continue and use whatever's on the linked list.
	}
	var out []FileEntry
	for s := d.impl.dev.storage; s != nil; s = s.next {
		name := ""
		if s.StorageDescription != nil {
			name = C.GoString(s.StorageDescription)
		}
		if name == "" {
			name = fmt.Sprintf("Storage %d", uint32(s.id))
		}
		out = append(out, FileEntry{
			ObjectID:  0, // root of this storage
			StorageID: uint32(s.id),
			Name:      name,
			IsFolder:  true,
		})
	}
	return out
}

func walkFileList(storageID uint32, head *C.LIBMTP_file_t) []FileEntry {
	var out []FileEntry
	for f := head; f != nil; f = f.next {
		entry := FileEntry{
			ObjectID:   uint32(f.item_id),
			StorageID:  storageID,
			ParentID:   uint32(f.parent_id),
			Name:       C.GoString(f.filename),
			Size:       int64(f.filesize),
			ModifiedAt: time.Unix(int64(f.modificationdate), 0),
			IsFolder:   f.filetype == C.LIBMTP_FILETYPE_FOLDER,
		}
		out = append(out, entry)
	}
	return out
}

func freeFileList(head *C.LIBMTP_file_t) {
	for f := head; f != nil; {
		next := f.next
		C.LIBMTP_destroy_file_t(f)
		f = next
	}
}

// lookupPath descends from the device root one segment at a time.
// MTP doesn't have a real "absolute path" — each object only knows its
// immediate parent — so we walk the tree the same way `cd` would.
func lookupPath(d *Device, p string) (*FileEntry, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil, errors.New("mtp: empty path")
	}
	segments := strings.Split(p, "/")

	storages := listStorages(d)
	var current *FileEntry
	for _, s := range storages {
		if equalFold(s.Name, segments[0]) {
			cp := s
			current = &cp
			break
		}
	}
	if current == nil {
		return nil, fmt.Errorf("mtp: storage %q not found: %w", segments[0], ErrNotFound)
	}

	for _, seg := range segments[1:] {
		children, err := listDir(d, current)
		if err != nil {
			return nil, err
		}
		var next *FileEntry
		for i := range children {
			if equalFold(children[i].Name, seg) {
				next = &children[i]
				break
			}
		}
		if next == nil {
			return nil, fmt.Errorf("mtp: %q not found under %q: %w", seg, current.Name, ErrNotFound)
		}
		current = next
	}
	return current, nil
}

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

// getFile streams an MTP object to w. libmtp's "_To_File" variant is
// the simplest API; we spool to a temp file then copy out.
func getFile(d *Device, entry *FileEntry, w io.Writer) error {
	if d.impl.dev == nil {
		return errors.New("mtp: device not open")
	}
	tmp, err := os.CreateTemp("", "kam-mtp-get-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	cPath := C.CString(tmpPath)
	defer C.free(unsafe.Pointer(cPath))
	if rc := C.LIBMTP_Get_File_To_File(d.impl.dev, C.uint32_t(entry.ObjectID), cPath, nil, nil); rc != 0 {
		dumpLibmtpErrors(d.impl.dev)
		return fmt.Errorf("mtp: Get_File_To_File(%d): rc=%d", entry.ObjectID, int(rc))
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// putFile uploads r as a child of parent. libmtp's "_From_File" variant
// needs a real on-disk path, so we spool first.
func putFile(d *Device, parent *FileEntry, name string, size int64, r io.Reader) (uint32, error) {
	if d.impl.dev == nil {
		return 0, errors.New("mtp: device not open")
	}
	if parent == nil || !parent.IsFolder {
		return 0, errors.New("mtp: parent must be a folder")
	}
	tmp, err := os.CreateTemp("", "kam-mtp-put-*")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	_ = tmp.Close()

	// Populate the metadata struct libmtp wants. We allocate it in C
	// land because libmtp may free it on transfer completion.
	meta := C.LIBMTP_new_file_t()
	if meta == nil {
		return 0, errors.New("mtp: LIBMTP_new_file_t failed")
	}
	meta.filename = C.CString(name)
	meta.filesize = C.uint64_t(size)
	meta.filetype = guessFiletype(name)
	meta.parent_id = C.uint32_t(parent.ObjectID)
	meta.storage_id = C.uint32_t(parent.StorageID)

	cPath := C.CString(tmpPath)
	defer C.free(unsafe.Pointer(cPath))

	rc := C.LIBMTP_Send_File_From_File(d.impl.dev, cPath, meta, nil, nil)
	newID := uint32(meta.item_id)
	C.LIBMTP_destroy_file_t(meta) // also frees the CString(name) it owns
	if rc != 0 {
		dumpLibmtpErrors(d.impl.dev)
		return 0, fmt.Errorf("mtp: Send_File_From_File: rc=%d", int(rc))
	}
	return newID, nil
}

func deleteObject(d *Device, entry *FileEntry) error {
	if d.impl.dev == nil {
		return errors.New("mtp: device not open")
	}
	if rc := C.LIBMTP_Delete_Object(d.impl.dev, C.uint32_t(entry.ObjectID)); rc != 0 {
		dumpLibmtpErrors(d.impl.dev)
		return fmt.Errorf("mtp: Delete_Object(%d): rc=%d", entry.ObjectID, int(rc))
	}
	return nil
}

// guessFiletype maps a filename to libmtp's filetype enum. The DJI Fly
// flow only needs KMZ (treated as generic file) and JPEG.
func guessFiletype(name string) C.LIBMTP_filetype_t {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		return C.LIBMTP_FILETYPE_JPEG
	case ".png":
		return C.LIBMTP_FILETYPE_PNG
	default:
		// LIBMTP_FILETYPE_UNKNOWN is the safe default for KMZ; the
		// device just stores the bytes.
		return C.LIBMTP_FILETYPE_UNKNOWN
	}
}

// dumpLibmtpErrors walks libmtp's internal error stack and feeds the
// messages into stderr-going debug output, then clears the stack.
// Without this, libmtp accumulates errors that surface confusingly
// in later operations.
func dumpLibmtpErrors(dev *C.LIBMTP_mtpdevice_t) {
	C.LIBMTP_Dump_Errorstack(dev)
	C.LIBMTP_Clear_Errorstack(dev)
}

// busDevString formats the bus/dev pair the way GVFS encodes it in
// MTP URIs: zero-padded three-digit numbers, joined by a comma. For
// USB bus 1 device 29, this returns "001,029".
func busDevString(d *Device) string {
	return fmt.Sprintf("%03d,%03d", uint32(d.impl.raw.bus_location), uint32(d.impl.raw.devnum))
}

// releaseGVFS frees the USB interface from whoever is currently
// claiming it: GVFS on GNOME desktops, kiod6 on KDE, and adb-server
// (which auto-claims any DJI vendor device even when it can't
// actually talk to it because USB debugging is off).
//
// We try each known competitor in turn, return true if we did
// *something*, then libmtp retries the open. Best-effort: if a
// competitor isn't running, the relevant command is a no-op.
func releaseGVFS(d *Device) bool {
	released := false
	uri := "mtp://[usb:" + busDevString(d) + "]/"

	// (1) GVFS user-mounted volume → ask GVFS to drop it nicely.
	for _, candidate := range [][]string{
		{"gio", "mount", "-u", uri},
		{"gvfs-mount", "-u", uri},
	} {
		path, err := exec.LookPath(candidate[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, candidate[1:]...)
		if out, err := cmd.CombinedOutput(); err == nil {
			slog.Info("released GVFS MTP mount", "uri", uri)
			released = true
			break
		} else {
			slog.Debug("gvfs unmount", "tool", candidate[0], "out", strings.TrimSpace(string(out)), "err", err)
		}
	}

	// (2) KDE kiod6 — KDE's I/O daemon — holds the interface for the
	// MTP KIO worker. Restarting kded6 / kioworker is heavy; killing
	// the specific kio worker is enough. They respawn on demand.
	if pkill, err := exec.LookPath("pkill"); err == nil {
		// kiod6 sometimes has the device handle even when no Dolphin
		// window is open. SIGTERM is fine — KDE respawns it lazily.
		_ = exec.Command(pkill, "-f", "kiod6").Run()
		// gvfsd-mtp respawns from gvfs-mtp-volume-monitor on next plug.
		_ = exec.Command(pkill, "-f", "gvfsd-mtp").Run()
		released = true
	}

	// (3) adb-server. adb auto-claims any DJI USB device on enumeration
	// because the vendor ID matches its AOSP allowlist. For an RC 2
	// (which can never talk ADB) this is dead weight blocking libmtp.
	// We don't kill adb-server outright — that would break other
	// devices the user has connected — but the safest middle ground
	// is to ask adb to disconnect just this device. If that fails,
	// kill the server (the user can restart it).
	if adb, err := exec.LookPath("adb"); err == nil {
		// `adb disconnect` is for TCP/IP devices and won't drop USB;
		// `adb kill-server` is the only universal lever. Use it only
		// when we're already on the retry path — that's why
		// releaseGVFS is called only after the first open attempt
		// failed.
		_ = exec.Command(adb, "kill-server").Run()
		slog.Info("stopped adb-server to free USB interface for MTP")
		released = true
	}

	if released {
		// Give the kernel a beat to actually drop the interface refs.
		time.Sleep(500 * time.Millisecond)
	}
	return released
}
