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

// lsblkNode mirrors one entry of `lsblk --json --list` output. Nullable
// columns (tran, fstype, …) decode to "".
type lsblkNode struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	PKName     string `json:"pkname"` // parent kernel name, e.g. "sdb" for sdb1
	Tran       string `json:"tran"`
	FSType     string `json:"fstype"`
	Label      string `json:"label"`
	UUID       string `json:"uuid"`
	Mountpoint string `json:"mountpoint"`
}

// Scan returns every USB-attached filesystem with a mountable type. It
// shells out to lsblk (always present on Linux) and parses its JSON.
func Scan() ([]Volume, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// --list keeps the output flat (no nested children); PKNAME is what
	// then links a partition back to its disk.
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-l", "-o",
		"NAME,PATH,PKNAME,TRAN,FSTYPE,LABEL,UUID,MOUNTPOINT").Output()
	if err != nil {
		return nil, fmt.Errorf("blockdev: lsblk: %w", err)
	}
	return parseLsblk(out)
}

// parseLsblk turns `lsblk -J -l` output into the USB volumes worth
// mounting. Split out from Scan so it can be unit-tested without lsblk.
func parseLsblk(jsonOut []byte) ([]Volume, error) {
	var doc struct {
		BlockDevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal(jsonOut, &doc); err != nil {
		return nil, fmt.Errorf("blockdev: parse lsblk json: %w", err)
	}
	// lsblk reports the bus transport on the disk node only — and on
	// some versions not on the partition at all — so record every
	// node's transport by kernel name first, then resolve each
	// partition through its parent (PKNAME).
	tranByName := make(map[string]string, len(doc.BlockDevices))
	for _, n := range doc.BlockDevices {
		tranByName[n.Name] = n.Tran
	}
	var vols []Volume
	for _, n := range doc.BlockDevices {
		if !mountable(n.FSType) || !isUSB(n, tranByName) {
			continue
		}
		id := n.UUID
		if id == "" {
			id = n.Label
		}
		if id == "" {
			id = n.Path
		}
		vols = append(vols, Volume{
			ID:         "usbms:" + id,
			DevPath:    n.Path,
			FSType:     n.FSType,
			Label:      n.Label,
			Mountpoint: n.Mountpoint,
		})
	}
	return vols, nil
}

// isUSB reports whether a node sits on the USB bus — directly (a
// whole-disk filesystem) or through its parent disk (a partition).
func isUSB(n lsblkNode, tranByName map[string]string) bool {
	if n.Tran == "usb" {
		return true
	}
	return n.PKName != "" && tranByName[n.PKName] == "usb"
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
