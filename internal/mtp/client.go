// Package mtp is the fallback transport for Android phones that don't
// expose ADB. The implementation is a stub; the device package will
// prefer ADB and only reach for MTP when ADB is unavailable.
//
// TODO: pick an MTP backing implementation:
//   - github.com/hanwen/go-mtpfs (mature, uses libusb via cgo)
//   - native MTP via WPD (Windows) / libmtp wrappers (macOS, Linux)
//
// libusb-based options break the "single static binary" goal on macOS
// and Windows. WPD/Image Capture Framework wrappers are platform-specific
// but avoid cgo. Decision deferred until we have a phone to test on.
package mtp

import "errors"

var ErrNotImplemented = errors.New("MTP transport not yet implemented")

type Device struct {
	ID    string
	Model string
}

type Client struct{}

func New() *Client { return &Client{} }

func (c *Client) ListDevices() ([]*Device, error) {
	return nil, nil // empty, not an error: MTP simply not available yet
}

func (c *Client) Push(deviceID, remotePath string, data []byte) error {
	return ErrNotImplemented
}

func (c *Client) Pull(deviceID, remotePath string) ([]byte, error) {
	return nil, ErrNotImplemented
}
