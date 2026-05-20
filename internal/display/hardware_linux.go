//go:build linux

package display

import (
	"fmt"

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
