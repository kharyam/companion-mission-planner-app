//go:build !linux || !cgo

// Stub implementation used on non-Linux platforms and on linux without
// cgo. Surfaces ErrUnavailable so the device.Registry can fall through
// to ADB without special-casing the build flavor.

package mtp

import "io"

type deviceImpl struct{}

func listDevices() ([]*Device, error)                                                   { return nil, nil }
func openDevice(*Device) error                                                          { return ErrUnavailable }
func closeDevice(*Device) error                                                         { return nil }
func lookupPath(*Device, string) (*FileEntry, error)                                    { return nil, ErrUnavailable }
func listDir(*Device, *FileEntry) ([]FileEntry, error)                                  { return nil, ErrUnavailable }
func getFile(*Device, *FileEntry, io.Writer) error                                      { return ErrUnavailable }
func putFile(*Device, *FileEntry, string, int64, io.Reader) (uint32, error)             { return 0, ErrUnavailable }
func deleteObject(*Device, *FileEntry) error                                            { return ErrUnavailable }
