package adb

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	goadb "github.com/zach-klippenstein/goadb"
)

// DeviceState mirrors adb's reported state (device|offline|unauthorized|...).
type DeviceState string

const (
	StateDevice       DeviceState = "device"
	StateOffline      DeviceState = "offline"
	StateUnauthorized DeviceState = "unauthorized"
	StateUnknown      DeviceState = "unknown"
)

// Device is a thin handle on a single ADB-visible device.
type Device struct {
	Serial string
	State  DeviceState
	Model  string
	t      *Transport
}

// Client is the high-level ADB façade the rest of the app uses.
type Client struct {
	t *Transport
}

func NewClient(t *Transport) *Client { return &Client{t: t} }

// ListDevices returns all devices the adb-server currently sees.
// goadb's DeviceInfo doesn't carry state inline, so we follow up with
// a per-device State() probe. This is one extra RPC per device but
// keeps the API simple; the device count is always small in practice.
func (c *Client) ListDevices() ([]*Device, error) {
	infos, err := c.t.adb.ListDevices()
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	devs := make([]*Device, 0, len(infos))
	for _, info := range infos {
		handle := &Device{Serial: info.Serial, Model: info.Model, t: c.t}
		if st, err := handle.RawState(); err == nil {
			handle.State = st
		} else {
			handle.State = StateUnknown
		}
		devs = append(devs, handle)
	}
	return devs, nil
}

// Device returns a handle for a specific serial.
func (c *Client) Device(serial string) *Device {
	return &Device{Serial: serial, State: StateUnknown, t: c.t}
}

// StateChange is the device-state transition event surfaced by Watch.
type StateChange struct {
	Serial   string
	Previous DeviceState
	Current  DeviceState
}

// Watch returns a channel of state-change events. The channel is closed
// when goadb's underlying watcher errors out; callers should treat
// closure as a signal to call Watch again after backing off. ListDevices
// remains usable while a Watch is active.
//
// The returned channel is not bounded by any context; the caller stops
// receiving by stopping reads. The underlying goadb watcher cannot be
// cancelled directly, but its goroutine is small and Shutdown is called
// when the *Client is collected.
func (c *Client) Watch() <-chan StateChange {
	w := c.t.adb.NewDeviceWatcher()
	out := make(chan StateChange, 16)
	go func() {
		defer close(out)
		for ev := range w.C() {
			out <- StateChange{
				Serial:   ev.Serial,
				Previous: mapState(ev.OldState),
				Current:  mapState(ev.NewState),
			}
		}
	}()
	return out
}

func mapState(s goadb.DeviceState) DeviceState {
	switch s {
	case goadb.StateOnline:
		return StateDevice
	case goadb.StateOffline, goadb.StateDisconnected:
		return StateOffline
	case goadb.StateUnauthorized:
		return StateUnauthorized
	default:
		return StateUnknown
	}
}

// Shell runs a single shell command and returns combined stdout/stderr.
func (d *Device) Shell(cmd string) (string, error) {
	dev := d.t.adb.Device(goadb.DeviceWithSerial(d.Serial))
	out, err := dev.RunCommand(cmd)
	if err != nil {
		return out, fmt.Errorf("shell %q: %w", cmd, err)
	}
	return out, nil
}

// Authorized reports whether the device is in `device` state.
func (d *Device) Authorized() (bool, error) {
	st, err := d.RawState()
	if err != nil {
		return false, err
	}
	return st == StateDevice, nil
}

// RawState queries the live adb state for this device, bypassing any
// cached value on the Device handle.
//
// goadb's State() returns an error (not a state) for several
// non-online conditions. Its top-level .Error() string drops the
// underlying message, so we walk the cause chain to recover it and
// also check structured error codes.
func (d *Device) RawState() (DeviceState, error) {
	dev := d.t.adb.Device(goadb.DeviceWithSerial(d.Serial))
	st, err := dev.State()
	if err == nil {
		return mapState(st), nil
	}
	if goadb.HasErrCode(err, goadb.DeviceNotFound) {
		return StateOffline, nil
	}
	chain := strings.ToLower(goadb.ErrorWithCauseChain(err))
	switch {
	case strings.Contains(chain, "unauthorized"):
		return StateUnauthorized, nil
	case strings.Contains(chain, "offline"):
		return StateOffline, nil
	case strings.Contains(chain, "no devices") || strings.Contains(chain, "device not found"):
		return StateOffline, nil
	}
	return StateUnknown, err
}

// HasDJIFly checks whether the DJI Fly waypoint folder exists.
func (d *Device) HasDJIFly() (bool, error) {
	out, err := d.Shell("ls /sdcard/Android/data/dji.go.v5/files/waypoint 2>/dev/null && echo OK")
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "OK"), nil
}

// DirEntry is the minimal directory-entry view we expose. Stripped down
// from goadb's wire type so callers don't drag in goadb imports.
type DirEntry struct {
	Name       string
	Mode       os.FileMode
	Size       int64
	ModifiedAt time.Time
}

// ListDir lists immediate entries under remotePath. Returns an empty
// slice (not an error) if the path doesn't exist.
func (d *Device) ListDir(remotePath string) ([]DirEntry, error) {
	dev := d.t.adb.Device(goadb.DeviceWithSerial(d.Serial))
	entries, err := dev.ListDirEntries(remotePath)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", remotePath, err)
	}
	raw, err := entries.ReadAll()
	if err != nil {
		return nil, err
	}
	out := make([]DirEntry, 0, len(raw))
	for _, e := range raw {
		if e.Name == "." || e.Name == ".." {
			continue
		}
		out = append(out, DirEntry{
			Name:       e.Name,
			Mode:       e.Mode,
			Size:       int64(e.Size),
			ModifiedAt: e.ModifiedAt,
		})
	}
	return out, nil
}

// Stat returns metadata for a single remote path, or (zero, false, nil)
// if the path doesn't exist. Other errors propagate.
func (d *Device) Stat(remotePath string) (DirEntry, bool, error) {
	dev := d.t.adb.Device(goadb.DeviceWithSerial(d.Serial))
	e, err := dev.Stat(remotePath)
	if err != nil {
		// goadb returns an error for missing paths; for our purposes
		// "no such file" is not a hard error.
		if strings.Contains(err.Error(), "no such") || strings.Contains(err.Error(), "does not exist") {
			return DirEntry{}, false, nil
		}
		return DirEntry{}, false, err
	}
	// A zero-mode result also indicates non-existence with this goadb version.
	if e == nil || (e.Mode == 0 && e.Size == 0 && e.ModifiedAt.IsZero()) {
		return DirEntry{}, false, nil
	}
	return DirEntry{
		Name:       e.Name,
		Mode:       e.Mode,
		Size:       int64(e.Size),
		ModifiedAt: e.ModifiedAt,
	}, true, nil
}

// OpenRead returns a streaming reader for remotePath.
func (d *Device) OpenRead(remotePath string) (io.ReadCloser, error) {
	dev := d.t.adb.Device(goadb.DeviceWithSerial(d.Serial))
	return dev.OpenRead(remotePath)
}

// ErrNotImplemented is returned by stubbed methods.
var ErrNotImplemented = errors.New("not implemented")
