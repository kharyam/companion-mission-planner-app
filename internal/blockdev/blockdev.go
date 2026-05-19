// Package blockdev discovers and mounts removable USB storage volumes —
// a camera/drone SD card in a reader, the drone's own mass-storage
// gadget — so the media browser can read them as an ordinary
// filesystem.
//
// Modern DJI drones (e.g. the Mini 5 Pro) expose their footage as USB
// Mass Storage, not MTP, so this is the transport that reaches them.
// The real implementation is Linux-only (see blockdev_linux.go); other
// platforms get no-op stubs and the feature is simply absent.
package blockdev

// Volume is a mountable USB filesystem found on the host.
type Volume struct {
	// ID is stable across reconnects: the filesystem UUID when available,
	// else the label, else the device path. Prefixed "usbms:".
	ID string

	// DevPath is the block device node, e.g. /dev/sda1.
	DevPath string

	// FSType is the filesystem, e.g. "vfat" or "exfat".
	FSType string

	// Label is the filesystem label, if any.
	Label string

	// Mountpoint is non-empty when the OS already has the volume mounted;
	// callers then read from there instead of mounting it themselves.
	Mountpoint string
}
