//go:build linux

package display

import (
	"fmt"
	"log/slog"

	"periph.io/x/host/v3"

	"github.com/kamdynamics/kam-transfer/internal/config"
)

// detectHardware brings up periph.io and probes for the Display HAT
// Mini and a PiSugar 3. The HAT (panel) is mandatory — without a screen
// there is nothing to drive, so a missing HAT returns an error and the
// status display stays off. The PiSugar is optional: when absent, the
// returned battery is nil and the screen simply omits the battery
// widget.
func detectHardware(cfg config.DisplayConfig, logger *slog.Logger) (panel, battery, error) {
	if _, err := host.Init(); err != nil {
		return nil, nil, fmt.Errorf("periph host init: %w", err)
	}

	hat, err := openHAT(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("display HAT unavailable: %w", err)
	}

	var batt battery
	if ps, err := openPiSugar(); err != nil {
		logger.Debug("no PiSugar battery detected", "err", err)
	} else {
		batt = ps
	}
	return hat, batt, nil
}
