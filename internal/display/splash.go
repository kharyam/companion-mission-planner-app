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

const (
	// spiNodeTimeout caps how long we wait for /dev/spidev0.1 to appear
	// before the first host.Init(). It is a cap, not a cost: waitForSPIDevice
	// returns the instant the node shows up, so a warm boot pays nothing.
	// Generous on purpose — this unit starts very early, and a cold Pi Zero
	// 2 W can take a few seconds to create the node; giving up too soon is
	// the difference between a splash and a blank screen for the whole boot.
	spiNodeTimeout = 20 * time.Second
	// hatRetryWindow is how long, after the node exists, we keep retrying the
	// HAT bring-up for transient (uncached) failures — a GPIO line or the SPI
	// handshake not yet ready this early — before concluding the HAT is
	// genuinely absent.
	hatRetryWindow = 5 * time.Second
)

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
	// to skip the /boot fsck wait — so the device nodes may not exist yet.
	// Two early-boot races have to be absorbed, both rooted in periph
	// enumerating its buses exactly once, at the first host.Init():
	//
	//   1. SPI ports are registered from whatever /dev/spidev* nodes exist at
	//      that first host.Init(). If /dev/spidev0.1 is missing then, zero
	//      ports register and the result is cached — no later retry can
	//      recover it. So wait for the node to appear BEFORE detectHardware
	//      makes that first call.
	//   2. The rest of the bring-up (GPIO lines, the SPI handshake) can still
	//      fail on the first pass while udev finishes settling. Those failures
	//      are NOT cached, so retry detectHardware for a short window once the
	//      node is up. (Dropping this retry is what broke the splash before:
	//      a single transient miss left a blank screen for the whole boot.)
	//
	// waitForSPIDevice returns the instant the node appears, so on a normal
	// boot this adds no latency — the splash comes up as soon as the HAT is
	// ready, which is the whole point of starting this unit so early.
	if waitForSPIDevice(ctx, spiNodeTimeout) {
		logger.Debug("SPI device node present; bringing up HAT")
	} else {
		// Non-Linux build, SPI disabled, or no HAT: the node never appears,
		// and detectWithRetry concludes cleanly below. Logged so a missing
		// splash on real hardware is diagnosable from this boot's journal.
		logger.Info("SPI device node not seen before timeout; trying anyway", "timeout", spiNodeTimeout)
	}
	hw, err := detectWithRetry(ctx, cfg, hatRetryWindow)
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

// detectWithRetry calls detectHardware until it succeeds or the window
// elapses, polling every 250ms. Once waitForSPIDevice has confirmed the
// SPI node — so periph's one-shot port enumeration sees it — the only
// failure modes left are transient and uncached (a GPIO line or the SPI
// handshake not yet ready this early in boot), so a retry recovers them.
// Returns the last error if nothing comes up in time. Honors ctx so the
// daemon's SIGTERM at handoff stops it promptly.
func detectWithRetry(ctx context.Context, cfg config.DisplayConfig, window time.Duration) (panel, error) {
	deadline := time.Now().Add(window)
	for {
		hw, err := detectHardware(cfg)
		if err == nil {
			return hw, nil
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(250 * time.Millisecond):
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
