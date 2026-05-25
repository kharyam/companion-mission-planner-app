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
		Logs: []string{
			"registry refresh elapsed=812ms",
			"Device 0 (VID=2ca3 and PID=1021) is a DJI Controller 2.",
			"mtp open id=usb:1-7 elapsed=143ms",
			"classified MTP device as controller",
		},
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
	for _, page := range []Page{PageStatus, PageTransfer, PageSystem, PageLogs, PageQR} {
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
	for _, page := range []Page{PageStatus, PageTransfer, PageSystem, PageLogs} {
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
	withLogs := ok
	withLogs.Logs = []string{
		"registry refresh elapsed=812ms",
		"Device 0 (VID=2ca3 and PID=1021) is a DJI Controller 2.",
		"mtp open id=usb:1-7 elapsed=143ms",
		"classified MTP device as controller",
		"GET /api/devices 200 12ms",
		"GET /api/devices/.../slots 200 38ms",
	}

	images := map[string]*image.RGBA{
		"display-status.png":         renderStatus(ok),
		"display-status-warning.png": renderStatus(warn),
		"display-transfer.png":       renderTransfer(transferring),
		"display-system.png":         renderSystem(ok),
		"display-logs.png":           renderLogs(withLogs),
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

func TestRenderSplash(t *testing.T) {
	lines := []string{
		"Starting Network Manager...",
		"Reached target Network.",
		"Started Avahi mDNS/DNS-SD Stack.",
		"this is a very long boot log line that should get truncated to fit the panel width without panicking",
	}
	for _, c := range []struct {
		name  string
		lines []string
	}{
		{"with lines", lines},
		{"empty", nil}, // before any journal output arrives — banner only
	} {
		t.Run(c.name, func(t *testing.T) {
			img := renderSplash("1.2.3", c.lines)
			if img == nil || uniform(img) {
				t.Fatal("renderSplash produced no output")
			}
			if b := img.Bounds(); b.Dx() != ScreenW || b.Dy() != ScreenH {
				t.Errorf("renderSplash size = %dx%d, want %dx%d", b.Dx(), b.Dy(), ScreenW, ScreenH)
			}
		})
	}
}

func TestLogRing(t *testing.T) {
	r := newLogRing(3)
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		r.push(s)
	}
	got := r.snapshot()
	want := []string{"c", "d", "e"}
	if len(got) != len(want) {
		t.Fatalf("ring len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ring[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRotateButton(t *testing.T) {
	// Default mounting (rotation 180): identity.
	for _, b := range []Button{ButtonA, ButtonB, ButtonX, ButtonY} {
		if got := rotateButton(b, true); got != b {
			t.Errorf("rotateButton(%d, true) = %d, want %d (identity)", b, got, b)
		}
	}
	// Flipped mounting (rotation 0): diagonal swap A↔Y, B↔X. Applying it
	// twice must round-trip back to the physical key.
	swap := map[Button]Button{ButtonA: ButtonY, ButtonY: ButtonA, ButtonB: ButtonX, ButtonX: ButtonB}
	for in, want := range swap {
		if got := rotateButton(in, false); got != want {
			t.Errorf("rotateButton(%d, false) = %d, want %d", in, got, want)
		}
		if got := rotateButton(rotateButton(in, false), false); got != in {
			t.Errorf("rotateButton twice (%d) = %d, want %d (involution)", in, got, in)
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
