//go:build linux

package display

import (
	"fmt"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
)

// piSugar reads a PiSugar 3 UPS over I2C bus 1. It implements battery.
type piSugar struct {
	bus i2c.BusCloser
	dev *i2c.Dev
}

func openPiSugar() (battery, error) {
	bus, err := i2creg.Open("1")
	if err != nil {
		return nil, fmt.Errorf("open I2C bus 1: %w", err)
	}
	dev := &i2c.Dev{Addr: piSugarAddr, Bus: bus}
	// A successful read of the battery register confirms a PiSugar is
	// actually present on the bus.
	var probe [1]byte
	if err := dev.Tx([]byte{regBatteryPct}, probe[:]); err != nil {
		_ = bus.Close()
		return nil, fmt.Errorf("probe PiSugar at 0x%02X: %w", piSugarAddr, err)
	}
	return &piSugar{bus: bus, dev: dev}, nil
}

// read implements battery.
func (p *piSugar) read() (BatteryStatus, error) {
	var pct, status [1]byte
	var volt [2]byte
	if err := p.dev.Tx([]byte{regBatteryPct}, pct[:]); err != nil {
		return BatteryStatus{}, err
	}
	if err := p.dev.Tx([]byte{regStatus}, status[:]); err != nil {
		return BatteryStatus{}, err
	}
	// A 2-byte read from the high register auto-increments into the low.
	if err := p.dev.Tx([]byte{regVoltageHigh}, volt[:]); err != nil {
		return BatteryStatus{}, err
	}
	return BatteryStatus{
		Present:       true,
		Percent:       pisugarPercent(pct[0]),
		Volts:         pisugarVolts(volt[0], volt[1]),
		ExternalPower: pisugarExternalPower(status[0]),
	}, nil
}

// close implements battery.
func (p *piSugar) close() error { return p.bus.Close() }
