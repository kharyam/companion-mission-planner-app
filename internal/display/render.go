package display

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/png"
	"strings"
	"time"

	"github.com/fogleman/gg"
	"github.com/golang/freetype/truetype"
	"github.com/skip2/go-qrcode"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
)

//go:embed assets/kam-logo.png
var kamLogoPNG []byte

var kamLogo = mustDecodePNG(kamLogoPNG)

func mustDecodePNG(b []byte) image.Image {
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		panic("display: decode embedded logo: " + err.Error())
	}
	return img
}

// Palette — a calm dark theme tuned for the IPS panel's high contrast.
type rgb struct{ r, g, b int }

var (
	colBG     = rgb{16, 18, 26}
	colPanel  = rgb{26, 30, 42}
	colAccent = rgb{31, 111, 235}
	colText   = rgb{231, 233, 240}
	colMuted  = rgb{132, 140, 156}
	colGreen  = rgb{46, 160, 67}
	colAmber  = rgb{219, 158, 40}
	colRed    = rgb{218, 54, 51}
	colWhite  = rgb{255, 255, 255}
)

func setCol(dc *gg.Context, c rgb) { dc.SetRGB255(c.r, c.g, c.b) }

// Fonts — parsed once, shared. The regular/bold faces ship with
// golang.org/x/image (already a dependency via the preview renderer).
var (
	fontRegular = mustFont(goregular.TTF)
	fontBold    = mustFont(gobold.TTF)
)

func mustFont(ttf []byte) *truetype.Font {
	f, err := truetype.Parse(ttf)
	if err != nil {
		panic("display: parse embedded font: " + err.Error())
	}
	return f
}

func setFace(dc *gg.Context, f *truetype.Font, px float64) {
	dc.SetFontFace(truetype.NewFace(f, &truetype.Options{Size: px, DPI: 72}))
}

// newCanvas returns a fresh ScreenW x ScreenH RGBA image and a gg
// context bound to it.
func newCanvas() (*image.RGBA, *gg.Context) {
	img := image.NewRGBA(image.Rect(0, 0, ScreenW, ScreenH))
	return img, gg.NewContextForRGBA(img)
}

// render draws one page of the status screen.
func render(s Snapshot, page Page) *image.RGBA {
	switch page {
	case PageQR:
		return renderQR(s.URL)
	case PageTransfer:
		return renderTransfer(s)
	case PageSystem:
		return renderSystem(s)
	default:
		return renderStatus(s)
	}
}

// header draws the top accent bar with the KAM logo, a title and the
// battery widget.
func header(dc *gg.Context, title string, s Snapshot) {
	const h = 34
	setCol(dc, colAccent)
	dc.DrawRectangle(0, 0, ScreenW, h)
	dc.Fill()

	logoW := kamLogo.Bounds().Dx()
	dc.DrawImage(kamLogo, 6, (h-kamLogo.Bounds().Dy())/2)

	setCol(dc, colWhite)
	setFace(dc, fontBold, 14)
	dc.DrawStringAnchored(title, float64(6+logoW+10), h/2, 0, 0.5)

	drawBattery(dc, ScreenW-12, h/2, s.Battery)
}

// drawBattery renders the battery widget with its right edge at xRight.
func drawBattery(dc *gg.Context, xRight, yMid float64, b BatteryStatus) {
	const bodyW, bodyH, nubW, nubH = 30.0, 15.0, 3.0, 7.0
	x0 := xRight - bodyW - nubW
	y0 := yMid - bodyH/2

	// Body outline + terminal nub.
	setCol(dc, colWhite)
	dc.SetLineWidth(1.5)
	dc.DrawRectangle(x0, y0, bodyW, bodyH)
	dc.Stroke()
	dc.DrawRectangle(xRight-nubW, yMid-nubH/2, nubW, nubH)
	dc.Fill()

	if !b.Present {
		setFace(dc, fontBold, 11)
		dc.DrawStringAnchored("--", x0+bodyW/2, yMid, 0.5, 0.5)
		return
	}

	// Fill bar, coloured by charge level.
	fillCol := colGreen
	switch {
	case b.Percent <= 15:
		fillCol = colRed
	case b.Percent <= 35:
		fillCol = colAmber
	}
	inset := 2.0
	fw := (bodyW - 2*inset) * float64(clampPct(b.Percent)) / 100
	setCol(dc, fillCol)
	dc.DrawRectangle(x0+inset, y0+inset, fw, bodyH-2*inset)
	dc.Fill()

	// Percentage label to the left of the icon.
	setCol(dc, colWhite)
	setFace(dc, fontBold, 12)
	dc.DrawStringAnchored(fmt.Sprintf("%d%%", clampPct(b.Percent)), x0-6, yMid, 1, 0.5)

	// Charging bolt when on external power.
	if b.ExternalPower {
		drawBolt(dc, x0+bodyW/2, yMid)
	}
}

// drawBolt draws a small lightning glyph centred at (cx, cy).
func drawBolt(dc *gg.Context, cx, cy float64) {
	setCol(dc, colWhite)
	dc.MoveTo(cx+1, cy-6)
	dc.LineTo(cx-4, cy+1)
	dc.LineTo(cx, cy+1)
	dc.LineTo(cx-1, cy+6)
	dc.LineTo(cx+4, cy-1)
	dc.LineTo(cx, cy-1)
	dc.ClosePath()
	dc.Fill()
}

func footer(dc *gg.Context, parts ...string) {
	const h = 28
	y0 := float64(ScreenH - h)
	setCol(dc, colPanel)
	dc.DrawRectangle(0, y0, ScreenW, h)
	dc.Fill()
	setCol(dc, colMuted)
	setFace(dc, fontRegular, 11)
	dc.DrawStringAnchored(strings.Join(parts, "   ·   "), ScreenW/2, y0+h/2, 0.5, 0.5)
}

// renderStatus is the default headline page.
func renderStatus(s Snapshot) *image.RGBA {
	img, dc := newCanvas()
	setCol(dc, colBG)
	dc.Clear()
	header(dc, "STATUS", s)

	// Headline: where to point the KAM planner.
	setCol(dc, colMuted)
	setFace(dc, fontRegular, 12)
	dc.DrawString("POINT KAM PLANNER AT", 16, 62)

	setCol(dc, colText)
	size := fitText(dc, fontBold, s.URL, ScreenW-32, 26)
	setFace(dc, fontBold, size)
	dc.DrawString(s.URL, 16, 96)

	// Controller status pill.
	drawPill(dc, 16, 118, controllerPill(s.Controller))

	// Network line.
	setFace(dc, fontRegular, 13)
	setCol(dc, colMuted)
	netLabel := "No network"
	if s.Net.Up {
		if s.Net.Wireless() {
			// Surface the network name — that's what an operator looks
			// for. The interface (wlan0) is implied, so drop it here to
			// keep the line short; the System page still lists it.
			if s.Net.SSID != "" {
				netLabel = fmt.Sprintf("Wi-Fi   %s   %s", s.Net.SSID, s.Net.IP)
			} else {
				netLabel = fmt.Sprintf("Wi-Fi   %s   (%s)", s.Net.IP, s.Net.Iface)
			}
		} else {
			netLabel = fmt.Sprintf("Wired   %s   (%s)", s.Net.IP, s.Net.Iface)
		}
	}
	dc.DrawString(netLabel, 16, 168)

	if s.Transferring {
		setCol(dc, colAmber)
		setFace(dc, fontBold, 13)
		dc.DrawString("TRANSFER IN PROGRESS", 16, 196)
	}

	footer(dc,
		"v"+s.Version,
		fmt.Sprintf("CPU %.0f°C", s.CPUTempC),
		"up "+humanDuration(s.Uptime),
	)
	return img
}

// renderTransfer shows whether a mission write is in flight.
func renderTransfer(s Snapshot) *image.RGBA {
	img, dc := newCanvas()
	setCol(dc, colBG)
	dc.Clear()
	header(dc, "TRANSFER", s)

	if s.Transferring {
		setCol(dc, colAmber)
		setFace(dc, fontBold, 24)
		dc.DrawStringAnchored("IN PROGRESS", ScreenW/2, 110, 0.5, 0.5)
		setCol(dc, colMuted)
		setFace(dc, fontRegular, 14)
		dc.DrawStringAnchored("Writing a mission to the controller", ScreenW/2, 142, 0.5, 0.5)
	} else {
		setCol(dc, colGreen)
		setFace(dc, fontBold, 24)
		dc.DrawStringAnchored("IDLE", ScreenW/2, 110, 0.5, 0.5)
		setCol(dc, colMuted)
		setFace(dc, fontRegular, 14)
		dc.DrawStringAnchored("No active transfer", ScreenW/2, 142, 0.5, 0.5)
	}

	footer(dc, "A: next page", "X: rescan", "Y: QR / hold = power off")
	return img
}

// renderSystem is the detail page.
func renderSystem(s Snapshot) *image.RGBA {
	img, dc := newCanvas()
	setCol(dc, colBG)
	dc.Clear()
	header(dc, "SYSTEM", s)

	battLine := "not detected"
	if s.Battery.Present {
		src := "on battery"
		if s.Battery.ExternalPower {
			src = "external power"
		}
		battLine = fmt.Sprintf("%d%%   %.2fV   %s", clampPct(s.Battery.Percent), s.Battery.Volts, src)
	}
	rows := [][2]string{{"Address", s.URL}}
	if s.Tailscale.Up {
		rows = append(rows, [2]string{"Tailscale", s.Tailscale.IP + "  " + ifEmpty(s.Tailscale.Iface, "")})
	}
	// For Wi-Fi, show the network name beside the IP (more useful than
	// the always-"wlan0" interface); wired keeps showing the interface.
	netDetail := ifEmpty(s.Net.Iface, "")
	if s.Net.Wireless() && s.Net.SSID != "" {
		netDetail = s.Net.SSID
	}
	rows = append(rows,
		[2]string{"Network", strings.TrimSpace(ifEmpty(s.Net.IP, "—") + "  " + netDetail)},
		[2]string{"Controller", controllerPill(s.Controller).text},
		[2]string{"Battery", battLine},
		[2]string{"Version", s.Version},
		[2]string{"Uptime", humanDuration(s.Uptime)},
		[2]string{"CPU temp", fmt.Sprintf("%.1f °C", s.CPUTempC)},
	)
	y := 58.0
	for _, row := range rows {
		setCol(dc, colMuted)
		setFace(dc, fontRegular, 12)
		dc.DrawString(strings.ToUpper(row[0]), 16, y)
		setCol(dc, colText)
		setFace(dc, fontBold, 13)
		dc.DrawString(row[1], 120, y)
		y += 22
	}
	return img
}

// renderQR draws a full-screen QR code of the server URL.
func renderQR(url string) *image.RGBA {
	img, dc := newCanvas()
	setCol(dc, colWhite)
	dc.Clear()

	setCol(dc, rgb{0, 0, 0})
	setFace(dc, fontBold, 14)
	dc.DrawStringAnchored("SCAN FOR WEB UI", ScreenW/2, 22, 0.5, 0.5)

	const qrSize = 168
	if q, err := qrcode.New(url, qrcode.Medium); err == nil {
		q.DisableBorder = true
		dc.DrawImage(q.Image(qrSize), (ScreenW-qrSize)/2, 36)
	}

	setFace(dc, fontRegular, 13)
	dc.DrawStringAnchored(url, ScreenW/2, 222, 0.5, 0.5)
	return img
}

// renderMessage is a centred two-line notice (e.g. the shutdown screen).
func renderMessage(title, sub string) *image.RGBA {
	img, dc := newCanvas()
	setCol(dc, colBG)
	dc.Clear()
	setCol(dc, colText)
	setFace(dc, fontBold, 26)
	dc.DrawStringAnchored(title, ScreenW/2, 104, 0.5, 0.5)
	setCol(dc, colMuted)
	setFace(dc, fontRegular, 14)
	dc.DrawStringAnchored(sub, ScreenW/2, 140, 0.5, 0.5)
	return img
}

// renderSplash draws the boot splash: a "starting" banner plus the tail
// of the system journal scrolling beneath it, newest line at the bottom.
// Shown by RunSplash while the Pi boots, before the main daemon takes
// over the screen.
func renderSplash(ver string, lines []string) *image.RGBA {
	img, dc := newCanvas()
	setCol(dc, colBG)
	dc.Clear()

	// Accent banner — a battery-free variant of header() (no Snapshot to
	// draw a battery widget from during early boot).
	const hb = 34
	setCol(dc, colAccent)
	dc.DrawRectangle(0, 0, ScreenW, hb)
	dc.Fill()
	logoW := kamLogo.Bounds().Dx()
	dc.DrawImage(kamLogo, 6, (hb-kamLogo.Bounds().Dy())/2)
	setCol(dc, colWhite)
	setFace(dc, fontBold, 14)
	dc.DrawStringAnchored("STARTING…", float64(6+logoW+10), hb/2, 0, 0.5)
	setFace(dc, fontRegular, 11)
	dc.DrawStringAnchored("v"+ver, ScreenW-10, hb/2, 1, 0.5)

	// Scrolling log region. Keep only the lines that fit, newest last.
	const (
		top     = hb + 7
		lineH   = 13.0
		leftPad = 8.0
		fontPx  = 10.0
	)
	maxW := float64(ScreenW) - 2*leftPad
	rows := (ScreenH - top) / int(lineH) // integer math: avoids a const float→int truncation
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	setFace(dc, fontRegular, fontPx)
	if len(lines) == 0 {
		// No journal lines yet (early boot, or journalctl can't read the
		// journal). Show that rather than a blank black box; it clears as
		// soon as the first line arrives.
		setCol(dc, colMuted)
		dc.DrawString("waiting for system log…", leftPad, float64(top)+lineH)
		return img
	}
	y := float64(top) + lineH
	last := len(lines) - 1
	for i, ln := range lines {
		if i == last {
			setCol(dc, colText) // newest line brightest
		} else {
			setCol(dc, colMuted)
		}
		dc.DrawString(truncateToWidth(dc, ln, maxW), leftPad, y)
		y += lineH
	}
	return img
}

// truncateToWidth shortens s (appending an ellipsis) until it fits maxW
// at the context's current font face. Returns s unchanged when it fits.
func truncateToWidth(dc *gg.Context, s string, maxW float64) string {
	if w, _ := dc.MeasureString(s); w <= maxW {
		return s
	}
	const ell = "…"
	r := []rune(s)
	for len(r) > 1 {
		r = r[:len(r)-1]
		if w, _ := dc.MeasureString(string(r) + ell); w <= maxW {
			return string(r) + ell
		}
	}
	return ell
}

// pill is a coloured status badge.
type pill struct {
	text string
	bg   rgb
}

func controllerPill(c ControllerStatus) pill {
	if !c.Connected {
		if c.Label != "" {
			return pill{strings.ToUpper(c.Label) + " — " + strings.ToUpper(c.State), colMuted}
		}
		return pill{"NO CONTROLLER", colMuted}
	}
	label := c.Label
	if label == "" {
		label = "Controller"
	}
	return pill{strings.ToUpper(label) + " CONNECTED", colGreen}
}

func drawPill(dc *gg.Context, x, y float64, p pill) {
	setFace(dc, fontBold, 13)
	w, _ := dc.MeasureString(p.text)
	const padX, h = 12.0, 26.0
	setCol(dc, p.bg)
	dc.DrawRoundedRectangle(x, y, w+2*padX, h, h/2)
	dc.Fill()
	setCol(dc, colWhite)
	dc.DrawStringAnchored(p.text, x+padX+w/2, y+h/2, 0.5, 0.5)
}

// fitText returns the largest font size ≤ max at which text fits maxW.
func fitText(dc *gg.Context, f *truetype.Font, text string, maxW, max float64) float64 {
	for size := max; size > 10; size -= 1 {
		setFace(dc, f, size)
		if w, _ := dc.MeasureString(text); w <= maxW {
			return size
		}
	}
	return 10
}

func clampPct(p int) int {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func ifEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// humanDuration renders a duration compactly, e.g. "3h12m" or "5m".
func humanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h := d / time.Hour
	m := (d % time.Hour) / time.Minute
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
