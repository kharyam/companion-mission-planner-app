//go:build linux

package display

import (
	"errors"
	"fmt"
	"image"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"

	"github.com/kamdynamics/kam-transfer/internal/config"
)

// Display HAT Mini GPIO map (BCM numbering), from the constants in
// pimoroni/displayhatmini-python. Buttons are active-low with pull-ups;
// the RGB LED is common-anode (driving a channel low lights it).
const (
	pinButtonA   = "GPIO5"
	pinButtonB   = "GPIO6"
	pinButtonX   = "GPIO16"
	pinButtonY   = "GPIO24"
	pinLEDRed    = "GPIO17"
	pinLEDGreen  = "GPIO27"
	pinLEDBlue   = "GPIO22"
	pinBacklight = "GPIO13"
)

// displayHAT is the whole Display HAT Mini board: an ST7789 LCD, four
// buttons, an RGB LED and a PWM backlight. It implements panel.
type displayHAT struct {
	lcd       *st7789
	rotate180 bool

	backlight        gpio.PinIO
	ledR, ledG, ledB gpio.PinIO
	btns             [4]gpio.PinIO

	events  chan ButtonEvent
	stop    chan struct{}
	stopped chan struct{}
}

func openHAT(cfg config.DisplayConfig) (*displayHAT, error) {
	lcd, err := openST7789()
	if err != nil {
		return nil, err
	}
	h := &displayHAT{
		lcd:       lcd,
		rotate180: cfg.Rotation == 180,
		events:    make(chan ButtonEvent, 8),
		stop:      make(chan struct{}),
		stopped:   make(chan struct{}),
	}

	var bindErr error
	outPin := func(name string, level gpio.Level) gpio.PinIO {
		if bindErr != nil {
			return nil
		}
		p := gpioreg.ByName(name)
		if p == nil {
			bindErr = fmt.Errorf("%s unavailable", name)
			return nil
		}
		if err := p.Out(level); err != nil {
			bindErr = fmt.Errorf("%s as output: %w", name, err)
		}
		return p
	}
	inPin := func(name string) gpio.PinIO {
		if bindErr != nil {
			return nil
		}
		p := gpioreg.ByName(name)
		if p == nil {
			bindErr = fmt.Errorf("%s unavailable", name)
			return nil
		}
		if err := p.In(gpio.PullUp, gpio.NoEdge); err != nil {
			bindErr = fmt.Errorf("%s as input: %w", name, err)
		}
		return p
	}

	h.backlight = outPin(pinBacklight, gpio.Low)
	h.ledR = outPin(pinLEDRed, gpio.High) // High = off (common-anode)
	h.ledG = outPin(pinLEDGreen, gpio.High)
	h.ledB = outPin(pinLEDBlue, gpio.High)
	h.btns[ButtonA] = inPin(pinButtonA)
	h.btns[ButtonB] = inPin(pinButtonB)
	h.btns[ButtonX] = inPin(pinButtonX)
	h.btns[ButtonY] = inPin(pinButtonY)
	if bindErr != nil {
		_ = lcd.close()
		return nil, fmt.Errorf("display HAT GPIO: %w", bindErr)
	}

	go h.pollButtons()
	return h, nil
}

// Blit implements panel.
func (h *displayHAT) Blit(img *image.RGBA) error {
	return h.lcd.blit(img, h.rotate180)
}

// SetBacklight implements panel. It uses hardware PWM for dimming and
// falls back to plain on/off if the pin can't PWM on this host.
func (h *displayHAT) SetBacklight(percent int) error {
	switch {
	case percent <= 0:
		return h.backlight.Out(gpio.Low)
	case percent >= 100:
		return h.backlight.Out(gpio.High)
	default:
		duty := gpio.DutyMax / 100 * gpio.Duty(percent)
		if err := h.backlight.PWM(duty, 1*physic.KiloHertz); err != nil {
			return h.backlight.Out(gpio.High)
		}
		return nil
	}
}

// SetLED implements panel. The LED is common-anode, so a lit channel is
// driven low.
func (h *displayHAT) SetLED(r, g, b bool) error {
	set := func(p gpio.PinIO, on bool) error {
		if on {
			return p.Out(gpio.Low)
		}
		return p.Out(gpio.High)
	}
	return errors.Join(set(h.ledR, r), set(h.ledG, g), set(h.ledB, b))
}

// Buttons implements panel.
func (h *displayHAT) Buttons() <-chan ButtonEvent { return h.events }

// Close implements panel.
func (h *displayHAT) Close() error {
	close(h.stop)
	<-h.stopped
	_ = h.SetLED(false, false, false)
	_ = h.SetBacklight(0)
	return h.lcd.close()
}

// pollButtons samples the four buttons every 20 ms, debounces them, and
// emits a ButtonEvent on release (short press) or once the long-press
// threshold is crossed while still held.
func (h *displayHAT) pollButtons() {
	defer close(h.stopped)

	const interval = 20 * time.Millisecond
	const debounce = 2 // consecutive equal samples needed to accept a change

	type state struct {
		level     bool // debounced: true == pressed
		raw       bool
		stable    int
		pressed   bool
		since     time.Time
		longFired bool
	}
	var st [4]state

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
		}
		for i := range h.btns {
			p := h.btns[i]
			if p == nil {
				continue
			}
			s := &st[i]
			raw := p.Read() == gpio.Low // active-low
			if raw == s.raw {
				if s.stable < debounce {
					s.stable++
				}
			} else {
				s.raw = raw
				s.stable = 1
			}
			if s.stable < debounce {
				continue
			}
			s.level = s.raw

			switch {
			case s.level && !s.pressed:
				s.pressed = true
				s.longFired = false
				s.since = time.Now()
			case s.level && s.pressed:
				if !s.longFired && time.Since(s.since) >= longPressThreshold {
					s.longFired = true
					h.emit(ButtonEvent{Button: Button(i), Long: true})
				}
			case !s.level && s.pressed:
				s.pressed = false
				if !s.longFired {
					h.emit(ButtonEvent{Button: Button(i)})
				}
			}
		}
	}
}

func (h *displayHAT) emit(ev ButtonEvent) {
	ev.Button = rotateButton(ev.Button, h.rotate180) // follow screen rotation
	select {
	case h.events <- ev:
	default: // drop rather than block if the consumer is busy
	}
}
