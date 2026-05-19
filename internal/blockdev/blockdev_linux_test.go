//go:build linux

package blockdev

import "testing"

// TestParseLsblk uses the real flat-list shape `lsblk -J -l` emits — the
// case that exposed the original bug: lsblk reports the USB transport on
// the disk node only, so a partition (the actual filesystem) carries
// "tran": null and must be linked to its parent disk via PKNAME.
func TestParseLsblk(t *testing.T) {
	// Mirrors a Raspberry Pi with a USB card reader holding a DJI exFAT
	// card (/dev/sdb1), an empty reader slot (/dev/sda), and the Pi's own
	// SD card on the mmc bus.
	const sample = `{"blockdevices":[
	  {"name":"sda","path":"/dev/sda","pkname":null,"tran":"usb","fstype":null},
	  {"name":"sdb","path":"/dev/sdb","pkname":null,"tran":"usb","fstype":null},
	  {"name":"sdb1","path":"/dev/sdb1","pkname":"sdb","tran":null,"fstype":"exfat","uuid":"4A21-0000"},
	  {"name":"mmcblk0","path":"/dev/mmcblk0","pkname":null,"tran":"mmc","fstype":null},
	  {"name":"mmcblk0p2","path":"/dev/mmcblk0p2","pkname":"mmcblk0","tran":null,"fstype":"ext4"},
	  {"name":"sdc","path":"/dev/sdc","pkname":null,"tran":"usb","fstype":"ext4","label":"LINUXUSB"}
	]}`

	vols, err := parseLsblk([]byte(sample))
	if err != nil {
		t.Fatalf("parseLsblk: %v", err)
	}
	// Only /dev/sdb1: USB (via parent sdb) and exFAT. /dev/sda has no
	// filesystem, the mmc partition isn't USB, /dev/sdc is USB but ext4.
	if len(vols) != 1 {
		t.Fatalf("got %d volumes, want 1: %+v", len(vols), vols)
	}
	v := vols[0]
	if v.DevPath != "/dev/sdb1" || v.FSType != "exfat" || v.ID != "usbms:4A21-0000" {
		t.Errorf("volume = %+v, want /dev/sdb1 exfat usbms:4A21-0000", v)
	}
}

// TestParseLsblkWholeDisk covers a card formatted without a partition
// table — the filesystem sits on the USB disk node directly.
func TestParseLsblkWholeDisk(t *testing.T) {
	const sample = `{"blockdevices":[
	  {"name":"sdb","path":"/dev/sdb","pkname":null,"tran":"usb","fstype":"vfat","label":"DRONE"}
	]}`
	vols, err := parseLsblk([]byte(sample))
	if err != nil {
		t.Fatalf("parseLsblk: %v", err)
	}
	if len(vols) != 1 || vols[0].DevPath != "/dev/sdb" || vols[0].FSType != "vfat" {
		t.Fatalf("got %+v, want one /dev/sdb vfat volume", vols)
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
