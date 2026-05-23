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
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
)

// st7789 is a minimal driver for the ST7789V2 LCD controller on the
// Pimoroni Display HAT Mini. The init sequence and 320x240 geometry are
// taken from pimoroni/st7789-python (the library the HAT ships with).
// The Display HAT Mini wires SPI chip-select to CE1, the data/command
// line to GPIO9, and exposes no GPIO reset pin.
type st7789 struct {
	port spi.PortCloser
	conn spi.Conn
	dc   gpio.PinIO
}

// st7789 SPI clock — the Display HAT Mini's Python library runs at 60 MHz.
const st7789ClockHz = 60 * physic.MegaHertz

func openST7789() (*st7789, error) {
	port, err := spireg.Open("SPI0.1")
	if err != nil {
		// The usual cause on a Pi is that the SPI bus isn't enabled, so
		// periph registered no ports at all and returns a bare "no port
		// found". Replace its generic "did you forget to call Init()?"
		// tail with an actionable fix rather than leaving the operator
		// guessing — we do call host.Init() (see detectHardware).
		if len(spireg.All()) == 0 {
			return nil, fmt.Errorf("open SPI0.1: %w — SPI bus not enabled; run `sudo raspi-config nonint do_spi 0` and reboot, then confirm /dev/spidev0.1 exists", err)
		}
		return nil, fmt.Errorf("open SPI0.1: %w", err)
	}
	conn, err := port.Connect(st7789ClockHz, spi.Mode0, 8)
	if err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("connect SPI: %w", err)
	}
	dc := gpioreg.ByName("GPIO9")
	if dc == nil {
		_ = port.Close()
		return nil, errors.New("GPIO9 (data/command) unavailable")
	}
	if err := dc.Out(gpio.Low); err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("init DC pin: %w", err)
	}
	d := &st7789{port: port, conn: conn, dc: dc}
	if err := d.init(); err != nil {
		_ = port.Close()
		return nil, err
	}
	return d, nil
}

// cmd writes a single command byte (DC low).
func (d *st7789) cmd(b byte) error {
	if err := d.dc.Out(gpio.Low); err != nil {
		return err
	}
	return d.conn.Tx([]byte{b}, nil)
}

// data writes payload bytes for the preceding command (DC high).
func (d *st7789) data(b ...byte) error {
	if err := d.dc.Out(gpio.High); err != nil {
		return err
	}
	return d.conn.Tx(b, nil)
}

// init runs the ST7789V2 power-on sequence.
func (d *st7789) init() error {
	steps := []struct {
		cmd   byte
		data  []byte
		sleep time.Duration
	}{
		{cmd: 0x01, sleep: 150 * time.Millisecond},              // SWRESET
		{cmd: 0x36, data: []byte{0x70}},                         // MADCTL — landscape
		{cmd: 0xB2, data: []byte{0x0C, 0x0C, 0x00, 0x33, 0x33}}, // PORCTRL
		{cmd: 0x3A, data: []byte{0x05}},                         // COLMOD — 16 bit/px
		{cmd: 0xB7, data: []byte{0x14}},                         // GCTRL
		{cmd: 0xBB, data: []byte{0x37}},                         // VCOMS
		{cmd: 0xC0, data: []byte{0x2C}},                         // LCMCTRL
		{cmd: 0xC2, data: []byte{0x01}},                         // VDVVRHEN
		{cmd: 0xC3, data: []byte{0x12}},                         // VRHS
		{cmd: 0xC4, data: []byte{0x20}},                         // VDVS
		{cmd: 0xD0, data: []byte{0xA4, 0xA1}},                   // PWRCTRL1
		{cmd: 0xC6, data: []byte{0x0F}},                         // FRCTRL2
		{cmd: 0xE0, data: []byte{0xD0, 0x04, 0x0D, 0x11, 0x13, 0x2B, // PVGAMCTRL
			0x3F, 0x54, 0x4C, 0x18, 0x0D, 0x0B, 0x1F, 0x23}},
		{cmd: 0xE1, data: []byte{0xD0, 0x04, 0x0C, 0x11, 0x13, 0x2C, // NVGAMCTRL
			0x3F, 0x44, 0x51, 0x2F, 0x1F, 0x1F, 0x20, 0x23}},
		{cmd: 0x21}, // INVON — ST7789 panels need inversion
		{cmd: 0x11, sleep: 120 * time.Millisecond}, // SLPOUT
		{cmd: 0x29}, // DISPON
	}
	for _, s := range steps {
		if err := d.cmd(s.cmd); err != nil {
			return fmt.Errorf("st7789 cmd 0x%02X: %w", s.cmd, err)
		}
		if len(s.data) > 0 {
			if err := d.data(s.data...); err != nil {
				return fmt.Errorf("st7789 data for 0x%02X: %w", s.cmd, err)
			}
		}
		if s.sleep > 0 {
			time.Sleep(s.sleep)
		}
	}
	return nil
}

// blit pushes a full ScreenW x ScreenH frame to the panel.
func (d *st7789) blit(img *image.RGBA, rotate180 bool) error {
	// Address window: the whole panel (columns 0..319, rows 0..239).
	if err := d.cmd(0x2A); err != nil { // CASET
		return err
	}
	if err := d.data(0x00, 0x00, 0x01, 0x3F); err != nil {
		return err
	}
	if err := d.cmd(0x2B); err != nil { // RASET
		return err
	}
	if err := d.data(0x00, 0x00, 0x00, 0xEF); err != nil {
		return err
	}
	if err := d.cmd(0x2C); err != nil { // RAMWR
		return err
	}

	pix := toRGB565(img, rotate180)
	if err := d.dc.Out(gpio.High); err != nil {
		return err
	}
	const chunk = 4096 // ST7789 SPI transactions are chunked, per Pimoroni
	for s := 0; s < len(pix); s += chunk {
		e := min(s+chunk, len(pix))
		if err := d.conn.Tx(pix[s:e], nil); err != nil {
			return fmt.Errorf("st7789 pixel tx: %w", err)
		}
	}
	return nil
}

// close turns the display off and releases the SPI port.
func (d *st7789) close() error {
	_ = d.cmd(0x28) // DISPOFF
	return d.port.Close()
}

// toRGB565 packs an RGBA image into big-endian RGB565 wire bytes,
// optionally rotating 180° to match how the HAT is mounted.
func toRGB565(img *image.RGBA, rotate180 bool) []byte {
	out := make([]byte, ScreenW*ScreenH*2)
	i := 0
	for y := range ScreenH {
		for x := range ScreenW {
			sx, sy := x, y
			if rotate180 {
				sx, sy = ScreenW-1-x, ScreenH-1-y
			}
			o := img.PixOffset(sx, sy)
			r := img.Pix[o]
			g := img.Pix[o+1]
			b := img.Pix[o+2]
			v := uint16(r&0xF8)<<8 | uint16(g&0xFC)<<3 | uint16(b)>>3
			out[i] = byte(v >> 8)
			out[i+1] = byte(v)
			i += 2
		}
	}
	return out
}
