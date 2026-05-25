//go:build linux

package display

import (
	"context"
	"fmt"
	"os"
	"time"

	"periph.io/x/host/v3"

	"github.com/kamdynamics/kam-transfer/internal/config"
)

// detectHardware brings up periph.io and probes for the Display HAT
// Mini. The HAT is mandatory for the screen — without it there is
// nothing to draw on, so the status display stays off. PiSugar probing
// lives in openPiSugar and runs independently (see RunBattery) so the
// API surfaces battery state on Pis that have the UPS but no screen.
func detectHardware(cfg config.DisplayConfig) (panel, error) {
	if _, err := host.Init(); err != nil {
		return nil, fmt.Errorf("periph host init: %w", err)
	}
	hat, err := openHAT(cfg)
	if err != nil {
		return nil, fmt.Errorf("display HAT unavailable: %w", err)
	}
	return hat, nil
}

// spiDevice is the Display HAT Mini's chip-select node (SPI0 CE1), the
// one openST7789 opens as "SPI0.1".
const spiDevice = "/dev/spidev0.1"

// waitForSPIDevice blocks until the SPI device node exists or the timeout
// passes, reporting whether it appeared. The boot splash starts before udev
// is guaranteed to have created it, and periph enumerates SPI ports only
// once — at the first host.Init() inside detectHardware — so the node must
// exist before that call or it is never registered. Returns true immediately
// once present (the normal case for the late-starting status screen) and
// false on timeout or a cancelled ctx.
func waitForSPIDevice(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(spiDevice); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(200 * time.Millisecond):
		}
	}
}
