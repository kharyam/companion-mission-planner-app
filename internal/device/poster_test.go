package device

import (
	"bytes"
	"context"
	"image/jpeg"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestExtractPoster exercises the real ffmpeg path: it synthesises a
// short clip with ffmpeg, then extracts a poster frame from it. Skipped
// when ffmpeg isn't installed — poster thumbnails are an optional extra.
func TestExtractPoster(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed — poster extraction is optional")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "clip.mp4")
	gen := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=320x240:rate=10",
		"-pix_fmt", "yuv420p", video)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate test video: %v: %s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	poster, err := extractPoster(ctx, video)
	if err != nil {
		t.Fatalf("extractPoster: %v", err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(poster))
	if err != nil {
		t.Fatalf("poster is not a valid JPEG: %v", err)
	}
	// scale=480:-2 should have resized the 320-wide source to 480 wide.
	if cfg.Width != 480 {
		t.Errorf("poster width = %d, want 480", cfg.Width)
	}
}
