package device

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// writeFile is a test helper that creates a file with the given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newTestMSController(t *testing.T) (*massStorageController, string) {
	t.Helper()
	root := t.TempDir()
	dcim := filepath.Join(root, "DCIM", "DJI_001")
	// A photo, two videos (one with an .LRF proxy, one without), plus
	// sidecar files that must be ignored.
	writeFile(t, filepath.Join(dcim, "DJI_0001.JPG"), "jpeg-bytes")
	writeFile(t, filepath.Join(dcim, "DJI_0002.MP4"), "mp4-full-res")
	writeFile(t, filepath.Join(dcim, "DJI_0002.LRF"), "lrf-proxy-clip")
	writeFile(t, filepath.Join(dcim, "DJI_0002.SRT"), "subtitles")
	writeFile(t, filepath.Join(dcim, "DJI_0003.MP4"), "mp4-no-proxy")
	writeFile(t, filepath.Join(root, "DCIM", "notes.txt"), "not media")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return newMassStorageController("usbms:test", "Test Card", root, logger), root
}

func TestMassStorageListMedia(t *testing.T) {
	c, _ := newTestMSController(t)
	items, err := c.ListMedia()
	if err != nil {
		t.Fatalf("ListMedia: %v", err)
	}
	// Only the photo and the two videos — .LRF, .SRT and .txt excluded.
	if len(items) != 3 {
		t.Fatalf("got %d media items, want 3: %+v", len(items), items)
	}
	byName := map[string]MediaItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	if got := byName["DJI_0001.JPG"].Kind; got != "photo" {
		t.Errorf("DJI_0001.JPG kind = %q, want photo", got)
	}
	if got := byName["DJI_0002.MP4"].Kind; got != "video" {
		t.Errorf("DJI_0002.MP4 kind = %q, want video", got)
	}
	if !byName["DJI_0002.MP4"].HasPreview {
		t.Error("DJI_0002.MP4 should have a preview (sibling .LRF)")
	}
	if byName["DJI_0003.MP4"].HasPreview {
		t.Error("DJI_0003.MP4 has no .LRF — HasPreview should be false")
	}
}

func TestMassStorageReadMediaAndPreview(t *testing.T) {
	c, _ := newTestMSController(t)
	items, err := c.ListMedia()
	if err != nil {
		t.Fatalf("ListMedia: %v", err)
	}
	var videoID string
	for _, it := range items {
		if it.Name == "DJI_0002.MP4" {
			videoID = it.ID
		}
	}
	if videoID == "" {
		t.Fatal("DJI_0002.MP4 not listed")
	}

	// ReadMedia streams the full original file.
	var full bytes.Buffer
	name, err := c.ReadMedia(videoID, &full)
	if err != nil {
		t.Fatalf("ReadMedia: %v", err)
	}
	if name != "DJI_0002.MP4" || full.String() != "mp4-full-res" {
		t.Errorf("ReadMedia = %q/%q, want DJI_0002.MP4/mp4-full-res", name, full.String())
	}

	// ReadVideoPreview streams the .LRF proxy, not the original.
	var preview bytes.Buffer
	if _, err := c.ReadVideoPreview(videoID, &preview); err != nil {
		t.Fatalf("ReadVideoPreview: %v", err)
	}
	if preview.String() != "lrf-proxy-clip" {
		t.Errorf("ReadVideoPreview = %q, want lrf-proxy-clip", preview.String())
	}
}

func TestMassStorageUnknownID(t *testing.T) {
	c, _ := newTestMSController(t)
	if _, err := c.ReadMedia("9999", io.Discard); err != ErrMediaNotFound {
		t.Errorf("ReadMedia(unknown) err = %v, want ErrMediaNotFound", err)
	}
	if _, err := c.ReadMedia("not-a-number", io.Discard); err != ErrMediaNotFound {
		t.Errorf("ReadMedia(malformed) err = %v, want ErrMediaNotFound", err)
	}
}
