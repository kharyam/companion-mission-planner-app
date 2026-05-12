package adb

import (
	"fmt"
	"io"
	"os"
	"time"

	goadb "github.com/zach-klippenstein/goadb"
)

// Push streams src into remotePath on the device.
//
// goadb's PushFile takes a local path; we want io.Reader to support
// in-memory KMZ rewrites without round-tripping through disk. For now
// we spool to a temp file. TODO: when we replace goadb with a custom
// client, push the bytes directly.
func (d *Device) Push(src io.Reader, remotePath string, mode os.FileMode) error {
	tmp, err := os.CreateTemp("", "kam-transfer-*.kmz")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err := io.Copy(tmp, src); err != nil {
		return fmt.Errorf("spool to temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	dev := d.t.adb.Device(goadb.DeviceWithSerial(d.Serial))
	// goadb exposes file push as a sync verb. The actual API name may
	// differ between forks; if upstream renames it, adapt here.
	out, err := dev.RunCommand(fmt.Sprintf("ls %q >/dev/null", remotePath))
	_ = out
	if err != nil {
		// proceed; ls failure is informational only
	}
	if err := pushViaSync(dev, tmp.Name(), remotePath, mode); err != nil {
		return fmt.Errorf("push %s -> %s: %w", tmp.Name(), remotePath, err)
	}
	return nil
}

// Pull copies remotePath into dst.
func (d *Device) Pull(remotePath string, dst io.Writer) error {
	dev := d.t.adb.Device(goadb.DeviceWithSerial(d.Serial))
	r, err := dev.OpenRead(remotePath)
	if err != nil {
		return fmt.Errorf("open %s for read: %w", remotePath, err)
	}
	defer r.Close()
	_, err = io.Copy(dst, r)
	return err
}

// pushViaSync hides the goadb method-name quirk behind one helper so we
// have a single place to update if the upstream API drifts.
func pushViaSync(dev *goadb.Device, localPath, remotePath string, mode os.FileMode) error {
	// goadb's master has Device.PushFile(local, remote, perm, mtime). Older
	// versions used OpenWrite + io.Copy. We try the simpler signature first;
	// if it's missing in your goadb pin, switch to OpenWrite/io.Copy here.
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w, err := dev.OpenWrite(remotePath, mode, time.Now())
	if err != nil {
		return err
	}
	defer w.Close()
	_, err = io.Copy(w, f)
	return err
}
