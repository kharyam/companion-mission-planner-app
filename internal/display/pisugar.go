package display

// PiSugar 3 I2C register map. The chip lives at 7-bit address 0x57 on
// I2C bus 1. See: github.com/PiSugar/PiSugar/wiki/PiSugar-3-I2C-Datasheet
const (
	piSugarAddr       = 0x57
	regStatus         = 0x02 // bit 7: external power present
	regBatteryPct     = 0x2A // battery charge 0-100
	regVoltageHigh    = 0x22 // battery voltage, millivolts, high byte
	regVoltageLow     = 0x23 // battery voltage, millivolts, low byte (0x22+1)
	statusExtPowerBit = 0x80
)

// The decode helpers below are deliberately split out of the I2C-bound
// pisugar_linux.go so the register math is unit-testable on any host.

// pisugarPercent clamps the raw battery register to a 0-100 percentage.
func pisugarPercent(reg byte) int {
	return clampPct(int(reg))
}

// pisugarVolts combines the two voltage registers (high, low) into volts.
func pisugarVolts(high, low byte) float64 {
	mv := int(high)<<8 | int(low)
	return float64(mv) / 1000.0
}

// pisugarExternalPower reports whether external power is connected, from
// bit 7 of the status register.
func pisugarExternalPower(status byte) bool {
	return status&statusExtPowerBit != 0
}
