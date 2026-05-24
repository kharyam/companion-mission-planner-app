package display

import (
	"bufio"
	"context"
	"log/slog"
	"os/exec"
	"strconv"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/config"
	"github.com/kamdynamics/kam-transfer/internal/version"
)

// splashMaxLines is how many journal lines the boot splash keeps and
// renders — roughly what fits in the scroll region under the banner.
const splashMaxLines = 14

// RunSplash drives a boot splash on the Display HAT Mini: a "starting"
// banner with the tail of the system journal scrolling beneath it. It is
// meant to run as an early systemd service (kam-transfer-splash.service),
// before the main daemon, bridging the gap between power-on and the
// daemon drawing its status page.
//
// It returns when ctx is cancelled — systemd's stop of this unit (which
// the daemon does just before it claims the SPI bus) delivers SIGTERM —
// or immediately and cleanly when the HAT is absent, so it never blocks
// boot on a non-HAT Pi.
func RunSplash(ctx context.Context, cfg config.DisplayConfig, logger *slog.Logger) error {
	logger = logger.With("component", "splash")
	if cfg.Enabled != nil && !*cfg.Enabled {
		logger.Debug("boot splash disabled by config")
		return nil
	}
	// The splash unit starts as soon as udev is up — before local-fs.target,
	// to skip the /boot fsck wait — so the SPI device node may not exist
	// yet. periph enumerates SPI ports exactly once, at the first
	// host.Init() (inside detectHardware), so we must wait for the node to
	// appear BEFORE that call; otherwise it is never registered and
	// detection fails permanently no matter how often we retry.
	waitForSPIDevice(ctx, 10*time.Second)
	hw, err := detectHardware(cfg)
	if err != nil {
		// No HAT (or not this platform): a clean no-op, like Run.
		logger.Info("boot splash inactive", "reason", err)
		return nil
	}
	defer hw.Close()
	logger.Info("boot splash active")

	brightness := cfg.Brightness
	if brightness <= 0 {
		brightness = 80
	}
	_ = hw.SetBacklight(brightness)
	_ = hw.SetLED(false, false, true) // blue = booting

	ring := newLogRing(splashMaxLines)
	journal := tailJournal(ctx)

	draw := func() {
		if err := hw.Blit(renderSplash(version.Version, ring.snapshot())); err != nil {
			logger.Warn("splash blit failed", "err", err)
		}
	}
	draw()

	// Coalesce redraws: mark dirty as lines arrive, repaint on a tick.
	// Repainting the whole 320×240 frame on every line would hammer the
	// SPI bus during the noisy early-boot burst for no visible benefit.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	dirty := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-journal:
			if !ok {
				journal = nil // tail ended (no journalctl / exited); banner stays
				continue
			}
			ring.push(line)
			dirty = true
		case <-ticker.C:
			if dirty {
				draw()
				dirty = false
			}
		}
	}
}

// tailJournal follows this boot's journal and streams each line. It
// shells out to journalctl (already present on any systemd host) seeded
// with the last splashMaxLines lines so the screen fills immediately.
// The returned channel closes when journalctl exits or ctx is cancelled
// — e.g. journalctl missing, or the process killed when ctx ends.
func tailJournal(ctx context.Context) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		cmd := exec.CommandContext(ctx, "journalctl",
			"-b", "-f", "-n", strconv.Itoa(splashMaxLines), "-o", "cat", "--no-pager")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return
		}
		if err := cmd.Start(); err != nil {
			return
		}
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			select {
			case out <- sc.Text():
			case <-ctx.Done():
				return // CommandContext kills journalctl on ctx end
			}
		}
		_ = cmd.Wait()
	}()
	return out
}

// logRing is a fixed-capacity FIFO of the most recent log lines. It is
// only touched from RunSplash's single goroutine, so it needs no lock.
type logRing struct {
	buf []string
	max int
}

func newLogRing(max int) *logRing { return &logRing{max: max} }

func (r *logRing) push(s string) {
	r.buf = append(r.buf, s)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
}

func (r *logRing) snapshot() []string {
	out := make([]string, len(r.buf))
	copy(out, r.buf)
	return out
}
