package display

import (
	"image"
	"testing"
	"time"
)

func sampleSnapshot() Snapshot {
	return Snapshot{
		URL:          "http://192.168.1.42:8765",
		Version:      "1.2.3",
		Battery:      BatteryStatus{Present: true, Percent: 72, Volts: 3.97, ExternalPower: true},
		Net:          NetStatus{Up: true, IP: "192.168.1.42", Iface: "wlan0"},
		Controller:   ControllerStatus{Connected: true, Label: "DJI RC 2", State: "online"},
		Transferring: false,
		CPUTempC:     48.3,
		Uptime:       3*time.Hour + 12*time.Minute,
		Now:          time.Now(),
	}
}

// uniform reports whether every pixel of img is identical — a renderer
// that drew nothing.
func uniform(img *image.RGBA) bool {
	if len(img.Pix) < 4 {
		return true
	}
	first := img.Pix[:4]
	for o := 4; o+4 <= len(img.Pix); o += 4 {
		px := img.Pix[o : o+4]
		if px[0] != first[0] || px[1] != first[1] || px[2] != first[2] || px[3] != first[3] {
			return false
		}
	}
	return true
}

func TestRenderPages(t *testing.T) {
	s := sampleSnapshot()
	for _, page := range []Page{PageStatus, PageTransfer, PageSystem, pageQR} {
		img := render(s, page)
		if img == nil {
			t.Fatalf("render(page %d) returned nil", page)
		}
		if b := img.Bounds(); b.Dx() != ScreenW || b.Dy() != ScreenH {
			t.Errorf("render(page %d) size = %dx%d, want %dx%d", page, b.Dx(), b.Dy(), ScreenW, ScreenH)
		}
		if uniform(img) {
			t.Errorf("render(page %d) produced a blank image", page)
		}
	}
}

// TestRenderEmpty exercises the renderer with a zero-value snapshot —
// no battery, no network, no controller — which is what a bare Pi
// shows. It must not panic and must still draw something.
func TestRenderEmpty(t *testing.T) {
	for _, page := range []Page{PageStatus, PageTransfer, PageSystem} {
		img := render(Snapshot{}, page)
		if img == nil || uniform(img) {
			t.Errorf("render(empty, page %d) produced no output", page)
		}
	}
}

func TestRenderMessage(t *testing.T) {
	img := renderMessage("Shutting down", "Safe to remove power")
	if img == nil || uniform(img) {
		t.Fatal("renderMessage produced no output")
	}
	if b := img.Bounds(); b.Dx() != ScreenW || b.Dy() != ScreenH {
		t.Errorf("renderMessage size = %dx%d, want %dx%d", b.Dx(), b.Dy(), ScreenW, ScreenH)
	}
}

func TestHealthLED(t *testing.T) {
	cases := []struct {
		name    string
		snap    Snapshot
		r, g, b bool
	}{
		{
			name: "all good",
			snap: Snapshot{
				Controller: ControllerStatus{Connected: true},
				Battery:    BatteryStatus{Present: true, Percent: 90, ExternalPower: true},
			},
			r: false, g: true, b: false,
		},
		{
			name: "no controller is amber",
			snap: Snapshot{
				Controller: ControllerStatus{Connected: false},
				Battery:    BatteryStatus{Present: true, Percent: 90, ExternalPower: true},
			},
			r: true, g: true, b: false,
		},
		{
			name: "critical battery is red",
			snap: Snapshot{
				Controller: ControllerStatus{Connected: true},
				Battery:    BatteryStatus{Present: true, Percent: 5, ExternalPower: false},
			},
			r: true, g: false, b: false,
		},
		{
			name: "no battery hardware, controller up is green",
			snap: Snapshot{Controller: ControllerStatus{Connected: true}},
			r:    false, g: true, b: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, g, b := healthLED(c.snap)
			if r != c.r || g != c.g || b != c.b {
				t.Errorf("healthLED = (%v,%v,%v), want (%v,%v,%v)", r, g, b, c.r, c.g, c.b)
			}
		})
	}
}
