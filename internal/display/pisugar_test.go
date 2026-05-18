package display

import (
	"math"
	"testing"
)

func TestPisugarPercent(t *testing.T) {
	cases := []struct {
		reg  byte
		want int
	}{
		{0, 0},
		{55, 55},
		{100, 100},
		{200, 100}, // out-of-range clamps to 100
	}
	for _, c := range cases {
		if got := pisugarPercent(c.reg); got != c.want {
			t.Errorf("pisugarPercent(%d) = %d, want %d", c.reg, got, c.want)
		}
	}
}

func TestPisugarVolts(t *testing.T) {
	cases := []struct {
		hi, lo byte
		want   float64
	}{
		{0x00, 0x00, 0.0},
		{0x0F, 0xA0, 4.0},   // 0x0FA0 = 4000 mV
		{0x10, 0x68, 4.200}, // 0x1068 = 4200 mV
		{0x0B, 0xB8, 3.0},   // 0x0BB8 = 3000 mV
	}
	for _, c := range cases {
		got := pisugarVolts(c.hi, c.lo)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("pisugarVolts(%#x, %#x) = %v, want %v", c.hi, c.lo, got, c.want)
		}
	}
}

func TestPisugarExternalPower(t *testing.T) {
	if pisugarExternalPower(0x00) {
		t.Error("status 0x00 should report no external power")
	}
	if !pisugarExternalPower(0x80) {
		t.Error("status 0x80 (bit 7 set) should report external power")
	}
	if !pisugarExternalPower(0xC3) {
		t.Error("status 0xC3 (bit 7 set) should report external power")
	}
	if pisugarExternalPower(0x7F) {
		t.Error("status 0x7F (bit 7 clear) should report no external power")
	}
}
