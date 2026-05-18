//go:build !linux

package display

import (
	"log/slog"

	"github.com/kamdynamics/kam-transfer/internal/config"
)

// detectHardware is the non-Linux no-op. The Display HAT Mini is driven
// over Raspberry Pi GPIO/SPI/I2C, which only exists on Linux, so every
// other platform compiles this stub and the status screen is disabled.
func detectHardware(config.DisplayConfig, *slog.Logger) (panel, battery, error) {
	return nil, nil, ErrNoHardware
}
