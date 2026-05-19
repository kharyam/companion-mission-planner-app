package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// errFFmpegUnavailable means ffmpeg isn't installed. Video poster
// thumbnails are an optional extra — without ffmpeg the gallery simply
// shows a film icon for videos, and everything else still works.
var errFFmpegUnavailable = errors.New("ffmpeg not found")

// extractPoster decodes the first frame of a video into a small JPEG
// for use as a gallery thumbnail. It shells out to ffmpeg — decoding a
// single frame is cheap even for 4K HEVC, the case where full
// transcoding to a proxy is not feasible on low-power hardware.
func extractPoster(ctx context.Context, videoPath string) ([]byte, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, errFFmpegUnavailable
	}
	cmd := exec.CommandContext(ctx, bin,
		"-nostdin", "-loglevel", "error",
		"-i", videoPath,
		"-frames:v", "1", // first frame only — bounded, cheap work
		"-vf", "scale=480:-2", // small thumbnail, aspect preserved
		"-f", "mjpeg", "-")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg poster (%s): %w: %s", videoPath, err, stderr.String())
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg poster (%s): no output", videoPath)
	}
	return stdout.Bytes(), nil
}
