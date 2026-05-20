package display

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
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

// TestUpdateDocImages re-renders the screenshots in docs/images/ that
// the README links to. It is opt-in so a normal `go test` run never
// touches the repo:
//
//	UPDATE_DOC_IMAGES=1 go test ./internal/display/ -run TestUpdateDocImages
func TestUpdateDocImages(t *testing.T) {
	if os.Getenv("UPDATE_DOC_IMAGES") == "" {
		t.Skip("set UPDATE_DOC_IMAGES=1 to refresh docs/images/display-*.png")
	}
	outDir := filepath.Join("..", "..", "docs", "images")
	if _, err := os.Stat(outDir); err != nil {
		t.Fatalf("doc image dir %q: %v", outDir, err)
	}

	ok := Snapshot{
		URL:        "http://192.168.1.42:8765",
		Version:    "1.4.0",
		Battery:    BatteryStatus{Present: true, Percent: 72, Volts: 4.05, ExternalPower: true},
		Net:        NetStatus{Up: true, IP: "192.168.1.42", Iface: "wlan0"},
		Controller: ControllerStatus{Connected: true, Label: "DJI RC 2", State: "online"},
		CPUTempC:   48.0,
		Uptime:     3*time.Hour + 12*time.Minute,
	}
	warn := ok
	warn.Battery = BatteryStatus{Present: true, Percent: 18, Volts: 3.62, ExternalPower: false}
	warn.Controller = ControllerStatus{}
	warn.CPUTempC = 55.0
	warn.Uptime = 47 * time.Minute
	transferring := ok
	transferring.Transferring = true

	images := map[string]*image.RGBA{
		"display-status.png":         renderStatus(ok),
		"display-status-warning.png": renderStatus(warn),
		"display-transfer.png":       renderTransfer(transferring),
		"display-system.png":         renderSystem(ok),
		"display-qr.png":             renderQR(ok.URL),
		"display-shutdown.png":       renderMessage("Shutting down", "It is now safe to remove power"),
	}
	for name, img := range images {
		path := filepath.Join(outDir, name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatalf("encode %s: %v", path, err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close %s: %v", path, err)
		}
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
