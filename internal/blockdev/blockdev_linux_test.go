//go:build linux

package blockdev

import (
	"encoding/json"
	"testing"
)

// TestCollectVolumes checks that USB-ness is inherited by partitions,
// non-USB disks are ignored, and only mountable filesystem types pass.
func TestCollectVolumes(t *testing.T) {
	const sample = `{"blockdevices":[
	  {"path":"/dev/nvme0n1","tran":"nvme","fstype":null,"children":[
	    {"path":"/dev/nvme0n1p1","tran":null,"fstype":"ext4"}]},
	  {"path":"/dev/sda","tran":"usb","fstype":null,"children":[
	    {"path":"/dev/sda1","tran":null,"fstype":"exfat","label":"DJICARD","uuid":"1234-ABCD"}]},
	  {"path":"/dev/sdb","tran":"usb","fstype":"vfat","label":"WHOLE","uuid":"5678-EF01"},
	  {"path":"/dev/sdc","tran":"usb","fstype":"ext4","label":"LINUXUSB"}
	]}`

	var doc struct {
		BlockDevices []lsblkNode `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(sample), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var vols []Volume
	for i := range doc.BlockDevices {
		collectVolumes(&doc.BlockDevices[i], false, &vols)
	}

	// Expect /dev/sda1 (exfat, USB inherited from parent) and /dev/sdb
	// (vfat, whole-disk USB). The NVMe partition is not USB; /dev/sdc is
	// USB but ext4, which we don't mount.
	if len(vols) != 2 {
		t.Fatalf("got %d volumes, want 2: %+v", len(vols), vols)
	}
	byDev := map[string]Volume{}
	for _, v := range vols {
		byDev[v.DevPath] = v
	}
	if v, ok := byDev["/dev/sda1"]; !ok {
		t.Error("/dev/sda1 (exfat USB partition) should be detected")
	} else if v.ID != "usbms:1234-ABCD" || v.FSType != "exfat" {
		t.Errorf("/dev/sda1 = %+v, want ID usbms:1234-ABCD / exfat", v)
	}
	if _, ok := byDev["/dev/sdb"]; !ok {
		t.Error("/dev/sdb (vfat whole-disk USB) should be detected")
	}
	if _, ok := byDev["/dev/sdc"]; ok {
		t.Error("/dev/sdc is ext4 — should not be mountable media")
	}
}

func TestMountable(t *testing.T) {
	for _, fs := range []string{"vfat", "exfat", "EXFAT", "ntfs"} {
		if !mountable(fs) {
			t.Errorf("mountable(%q) = false, want true", fs)
		}
	}
	for _, fs := range []string{"ext4", "btrfs", "squashfs", ""} {
		if mountable(fs) {
			t.Errorf("mountable(%q) = true, want false", fs)
		}
	}
}
