package adb

import (
	"errors"
	"fmt"
	"strings"

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
		state := StateUnknown
		dev := c.t.adb.Device(goadb.DeviceWithSerial(info.Serial))
		if st, err := dev.State(); err == nil {
			state = mapState(st)
		}
		devs = append(devs, &Device{
			Serial: info.Serial,
			State:  state,
			Model:  info.Model,
			t:      c.t,
		})
	}
	return devs, nil
}

// Device returns a handle for a specific serial.
func (c *Client) Device(serial string) *Device {
	return &Device{Serial: serial, State: StateUnknown, t: c.t}
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
	dev := d.t.adb.Device(goadb.DeviceWithSerial(d.Serial))
	st, err := dev.State()
	if err != nil {
		return false, err
	}
	return mapState(st) == StateDevice, nil
}

// HasDJIFly checks whether the DJI Fly waypoint folder exists.
func (d *Device) HasDJIFly() (bool, error) {
	out, err := d.Shell("ls /sdcard/Android/data/dji.go.v5/files/waypoint 2>/dev/null && echo OK")
	if err != nil {
		return false, err
	}
	return strings.Contains(out, "OK"), nil
}

// ErrNotImplemented is returned by stubbed methods.
var ErrNotImplemented = errors.New("not implemented")
