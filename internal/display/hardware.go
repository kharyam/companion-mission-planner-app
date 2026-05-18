// Package display drives an optional front-panel status screen for a
// Raspberry Pi fitted with a Pimoroni Display HAT Mini (a 320x240 IPS
// LCD with four buttons and an RGB LED) and, optionally, a PiSugar 3
// battery UPS.
//
// The feature auto-detects its hardware at startup and is a clean no-op
// when absent: the same pure-Go binary runs unchanged on a HAT-equipped
// Pi, a bare Pi, and a desktop. The hardware-touching code lives behind
// a `linux` build tag (hardware_linux.go and friends); every other
// platform compiles hardware_stub.go, whose detectHardware returns
// ErrNoHardware. No cgo is involved — the Linux path uses periph.io,
// which is pure Go.
package display

import (
	"errors"
	"image"
)

// ErrNoHardware reports that the status-screen hardware is not present,
// or that this build cannot drive it. Controller.Run treats it as a
// clean no-op rather than a failure.
var ErrNoHardware = errors.New("display: status-screen hardware not detected")

// ScreenW and ScreenH are the Display HAT Mini's panel dimensions.
const (
	ScreenW = 320
	ScreenH = 240
)

// panel abstracts the Pimoroni Display HAT Mini board: a 320x240 LCD,
// four buttons, an RGB status LED and a controllable backlight.
type panel interface {
	// Blit pushes a ScreenW x ScreenH image to the LCD.
	Blit(img *image.RGBA) error
	// SetBacklight sets backlight brightness 0-100 (0 turns it off).
	SetBacklight(percent int) error
	// SetLED sets the RGB status LED; each channel is simply on or off.
	SetLED(r, g, b bool) error
	// Buttons delivers debounced button events until Close is called.
	Buttons() <-chan ButtonEvent
	// Close blanks the screen, turns off the backlight and LED, and
	// releases the GPIO/SPI handles.
	Close() error
}

// battery abstracts a PiSugar 3 UPS read over I2C.
type battery interface {
	read() (BatteryStatus, error)
	close() error
}

// BatteryStatus is one PiSugar 3 reading.
type BatteryStatus struct {
	Present       bool    // a PiSugar was detected
	Percent       int     // 0-100
	Volts         float64 // battery voltage
	ExternalPower bool    // external/USB power is connected
}

// Button identifies one of the four Display HAT Mini buttons.
type Button int

const (
	ButtonA Button = iota // top-left
	ButtonB               // bottom-left
	ButtonX               // top-right
	ButtonY               // bottom-right
)

// ButtonEvent is a single debounced press.
type ButtonEvent struct {
	Button Button
	Long   bool // true when held past the long-press threshold
}

// Page is one screen of the status display, cycled with button A.
type Page int

const (
	PageStatus   Page = iota // headline: server URL, battery, wifi, controller
	PageTransfer             // transfer activity
	PageSystem               // network + system detail
	pageCount

	// pageQR is an ephemeral overlay (button Y), not part of the cycle.
	pageQR Page = -1
)
