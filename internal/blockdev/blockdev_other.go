//go:build !linux

// Stub implementation for non-Linux platforms. USB mass-storage media
// browsing is a Linux-only feature; elsewhere Scan finds nothing and the
// rest of the daemon carries on unaffected.

package blockdev

import "errors"

var errUnsupported = errors.New("blockdev: USB mass-storage support is Linux-only")

func Scan() ([]Volume, error)                      { return nil, nil }
func MountRO(devPath, fsType, target string) error { return errUnsupported }
func Unmount(target string) error                  { return errUnsupported }
