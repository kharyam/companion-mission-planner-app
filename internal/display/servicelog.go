package display

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// serviceUnit is the systemd unit whose journal the Logs page tails. It's
// the running daemon's own unit, so the page mirrors exactly what
// `journalctl -u kam-transfer` would show over SSH — including the libmtp
// device-detection lines that are handy when a controller won't appear.
const serviceUnit = "kam-transfer.service"

// logBufferMax is how many recent journal lines the Logs page keeps. A
// bit more than fits on the 320×240 panel so the tail is stable between
// the (refreshInterval-paced) redraws and the web-UI mirror has context.
const logBufferMax = 40

// logBuffer is a concurrency-safe fixed-capacity FIFO of recent journal
// lines. The journal-tail goroutine pushes while the render loop and the
// HTTP screen-mirror read, so — unlike splash's single-goroutine logRing
// — it needs a lock.
//
// Consecutive identical lines are collapsed into a single entry with a
// repeat count rather than stored individually. libmtp re-prints its
// "Device N … is a DJI Controller 2." line on every hotplug poll (~every
// 2s), which would otherwise fill the whole buffer and evict the useful
// startup/refresh history within a minute or two; collapsing keeps that
// history visible and surfaces the repeat as a "(×N)" suffix.
type logBuffer struct {
	mu  sync.Mutex
	buf []logEntry
	max int
}

// logEntry is one buffered line plus how many consecutive identical lines
// have been folded into it (count == 1 for a normal, un-repeated line).
type logEntry struct {
	text  string
	count int
}

func newLogBuffer(max int) *logBuffer { return &logBuffer{max: max} }

func (b *logBuffer) push(s string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n := len(b.buf); n > 0 && b.buf[n-1].text == s {
		b.buf[n-1].count++
		return
	}
	b.buf = append(b.buf, logEntry{text: s, count: 1})
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
}

func (b *logBuffer) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.buf))
	for i, e := range b.buf {
		if e.count > 1 {
			out[i] = fmt.Sprintf("%s  (×%d)", e.text, e.count)
		} else {
			out[i] = e.text
		}
	}
	return out
}

// runServiceLogTail follows the kam-transfer unit's journal and feeds each
// line into the controller's log buffer until ctx is cancelled. It is
// launched as a background goroutine for the lifetime of the display.
//
// Unlike the boot splash (which tails the whole-boot journal extremely
// early and has to ride out journald's ACL race), this runs from the
// already-up daemon, so a plain reconnect loop is enough. The service user
// must still be able to read the system journal — the systemd unit grants
// that via SupplementaryGroups=systemd-journal. Without that group
// journalctl exits with "No journal files were opened due to insufficient
// permissions" and the page stays empty; we log the reason once so it's
// diagnosable from the journal itself.
func (c *Controller) runServiceLogTail(ctx context.Context) {
	if _, err := exec.LookPath("journalctl"); err != nil {
		c.logger.Info("logs page inactive: journalctl not found on PATH", "err", err)
		return
	}
	logged := false
	for ctx.Err() == nil {
		err := c.streamServiceJournal(ctx)
		if ctx.Err() != nil {
			return // display shutting down — expected
		}
		if err != nil && !logged {
			c.logger.Warn("logs page: journalctl unavailable, retrying", "err", err)
			logged = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// streamServiceJournal runs one `journalctl -u <unit> -f` and pumps its
// lines into the log buffer until it exits or ctx is cancelled. It returns
// journalctl's exit error with any stderr folded in so the caller can log
// why and retry. -o cat drops the timestamp/host prefix (we only have ~52
// chars of panel width); -q drops the "not seeing other users" hint.
func (c *Controller) streamServiceJournal(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "journalctl",
		"-u", serviceUnit, "-b", "-f", "-n", strconv.Itoa(logBufferMax),
		"-o", "cat", "-q", "--no-pager")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		c.logs.push(sc.Text())
	}
	if err := cmd.Wait(); err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return fmt.Errorf("%w: %s", err, s)
		}
		return err
	}
	return nil
}
