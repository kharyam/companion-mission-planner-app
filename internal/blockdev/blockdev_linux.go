//go:build linux

package blockdev

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// lsblkNode mirrors one node of `lsblk --json` output. Disks carry
// partitions as children; nullable columns decode to "".
type lsblkNode struct {
	Path       string      `json:"path"`
	Tran       string      `json:"tran"`
	FSType     string      `json:"fstype"`
	Label      string      `json:"label"`
	UUID       string      `json:"uuid"`
	Mountpoint string      `json:"mountpoint"`
	Children   []lsblkNode `json:"children"`
}

// Scan returns every USB-attached filesystem with a mountable type.
// It shells out to lsblk (always present on Linux) and parses its JSON.
func Scan() ([]Volume, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-o",
		"PATH,TRAN,FSTYPE,LABEL,UUID,MOUNTPOINT").Output()
	if err != nil {
		return nil, fmt.Errorf("blockdev: lsblk: %w", err)
	}
	var doc struct {
		BlockDevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("blockdev: parse lsblk json: %w", err)
	}
	var vols []Volume
	for i := range doc.BlockDevices {
		collectVolumes(&doc.BlockDevices[i], false, &vols)
	}
	return vols, nil
}

// collectVolumes walks a disk and its partitions, emitting one Volume
// per USB-attached node carrying a mountable filesystem. lsblk reports
// `tran` on the disk node only, so USB-ness is inherited by children.
func collectVolumes(n *lsblkNode, parentUSB bool, out *[]Volume) {
	isUSB := parentUSB || n.Tran == "usb"
	if isUSB && mountable(n.FSType) {
		id := n.UUID
		if id == "" {
			id = n.Label
		}
		if id == "" {
			id = n.Path
		}
		*out = append(*out, Volume{
			ID:         "usbms:" + id,
			DevPath:    n.Path,
			FSType:     n.FSType,
			Label:      n.Label,
			Mountpoint: n.Mountpoint,
		})
	}
	for i := range n.Children {
		collectVolumes(&n.Children[i], isUSB, out)
	}
}

// mountable reports whether a filesystem type is one we'll mount —
// limited to the FAT-family/NTFS filesystems cameras and drones use.
func mountable(fsType string) bool {
	switch strings.ToLower(fsType) {
	case "vfat", "exfat", "ntfs", "ntfs3":
		return true
	default:
		return false
	}
}

// MountRO mounts devPath read-only at target, creating target if
// needed. For FAT-family filesystems it passes uid/gid so the
// (unprivileged) daemon user can read the files. Requires CAP_SYS_ADMIN.
func MountRO(devPath, fsType, target string) error {
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	flags := uintptr(syscall.MS_RDONLY | syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_NOEXEC)
	data := ""
	switch strings.ToLower(fsType) {
	case "vfat", "exfat":
		data = fmt.Sprintf("uid=%d,gid=%d,fmask=0133,dmask=0022", os.Getuid(), os.Getgid())
	}
	// "ntfs" is served by the in-kernel ntfs3 driver on modern kernels.
	kfs := strings.ToLower(fsType)
	if kfs == "ntfs" {
		kfs = "ntfs3"
	}
	if err := syscall.Mount(devPath, target, kfs, flags, data); err != nil {
		return fmt.Errorf("blockdev: mount %s (%s) on %s: %w", devPath, kfs, target, err)
	}
	return nil
}

// Unmount detaches the filesystem at target. It uses a lazy unmount so
// an in-flight read can't make this fail; the mount goes away once the
// last reader closes.
func Unmount(target string) error {
	if err := syscall.Unmount(target, syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("blockdev: unmount %s: %w", target, err)
	}
	return nil
}
