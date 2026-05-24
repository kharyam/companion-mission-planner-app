//go:build !linux

package display

import (
	"context"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/config"
)

// detectHardware is the non-Linux no-op. The Display HAT Mini is driven
// over Raspberry Pi GPIO/SPI/I2C, which only exists on Linux, so every
// other platform compiles this stub and the status screen is disabled.
func detectHardware(config.DisplayConfig) (panel, error) {
	return nil, ErrNoHardware
}

// openPiSugar is the non-Linux no-op. The PiSugar UPS is read over I2C
// (Linux-only), so every other platform reports no battery.
func openPiSugar() (battery, error) {
	return nil, ErrNoHardware
}

// waitForSPIDevice is the non-Linux no-op — there are no spidev nodes to
// wait for, and detectHardware returns ErrNoHardware regardless.
func waitForSPIDevice(context.Context, time.Duration) {}
