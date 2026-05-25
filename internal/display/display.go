package display

import (
	"context"
	"errors"
	"fmt"
	"image"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/config"
	"github.com/kamdynamics/kam-transfer/internal/device"
	"github.com/kamdynamics/kam-transfer/internal/version"
)

// ErrShutdownDisabled is returned by Shutdown when the config option
// display.allowShutdown is not set.
var ErrShutdownDisabled = errors.New("display: allowShutdown is not enabled in config")

// longPressThreshold is how long button Y must be held to count as a
// long press (the safe-shutdown gesture).
const longPressThreshold = 3 * time.Second

// Controller owns the front-panel status screen. It is constructed
// unconditionally by the API server; Run is a no-op unless the Display
// HAT Mini hardware is actually present.
type Controller struct {
	cfg          config.DisplayConfig
	bind         string
	port         int
	registry     *device.Registry
	transferBusy func() bool
	logger       *slog.Logger
	started      time.Time

	// latestBattery holds the most recent successful PiSugar reading so
	// the HTTP API can surface it without taking on its own I2C polling.
	// nil while no PiSugar has been detected (or before the first read).
	latestBattery atomic.Pointer[BatteryStatus]
}

// New builds a Controller. It touches no hardware — detection happens
// in Run, on the daemon's background goroutine.
func New(cfg *config.Config, reg *device.Registry, transferBusy func() bool, logger *slog.Logger) *Controller {
	return &Controller{
		cfg:          cfg.Display,
		bind:         cfg.Server.Bind,
		port:         cfg.Server.Port,
		registry:     reg,
		transferBusy: transferBusy,
		logger:       logger.With("component", "display"),
		started:      time.Now(),
	}
}

// Run drives the status screen until ctx is cancelled. It is meant to
// be launched as a goroutine. When the hardware is absent (a bare Pi or
// a desktop) it logs once and returns; the daemon is unaffected.
//
// Battery polling is independent (see RunBattery) — the screen reads
// whatever the poller has cached.
func (c *Controller) Run(ctx context.Context) {
	if c.cfg.Enabled != nil && !*c.cfg.Enabled {
		c.logger.Debug("status display disabled by config")
		return
	}
	hw, err := detectHardware(c.cfg)
	if err != nil {
		c.logger.Info("status display inactive", "reason", err)
		return
	}
	defer hw.Close()
	c.logger.Info("status display active")
	c.loop(ctx, hw)
}

// RunBattery probes for a PiSugar 3 UPS and, when present, polls it for
// as long as ctx is alive. The latest reading is exposed via Battery().
// This runs independently of the front-panel display so the HTTP API
// surfaces battery state on Pis that have the UPS but not the screen.
func (c *Controller) RunBattery(ctx context.Context) {
	if c.cfg.Enabled != nil && !*c.cfg.Enabled {
		return
	}
	batt, err := openPiSugar()
	if err != nil {
		c.logger.Info("battery monitor inactive", "reason", err)
		return
	}
	defer batt.close()
	c.logger.Info("battery monitor active")

	poll := func() {
		if bs, err := batt.read(); err == nil {
			snap := bs
			c.latestBattery.Store(&snap)
		} else {
			c.logger.Debug("battery read failed", "err", err)
		}
	}
	poll()

	ticker := time.NewTicker(c.refreshInterval())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}

// loop is the redraw/input loop. It runs only once hardware is confirmed.
func (c *Controller) loop(ctx context.Context, hw panel) {
	page := PageStatus
	backlightOn := true
	_ = hw.SetBacklight(c.cfg.Brightness)

	ticker := time.NewTicker(c.refreshInterval())
	defer ticker.Stop()
	events := c.registry.Watch(ctx)
	buttons := hw.Buttons()

	draw := func() {
		snap := c.snapshot()
		var img = render(snap, page)
		if err := hw.Blit(img); err != nil {
			c.logger.Warn("screen blit failed", "err", err)
		}
		r, g, b := healthLED(snap)
		_ = hw.SetLED(r, g, b)
	}
	draw()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			draw()

		case _, ok := <-events:
			if !ok {
				// Watcher died (e.g. adb-server restart). Resubscribe;
				// the ticker keeps the screen fresh meanwhile.
				if ctx.Err() != nil {
					return
				}
				events = c.registry.Watch(ctx)
				continue
			}
			draw()

		case be, ok := <-buttons:
			if !ok {
				return
			}
			switch be.Button {
			case ButtonA, ButtonX:
				if page == PageQR {
					page = PageStatus
				} else {
					page = (page + 1) % pageCount
				}
			case ButtonB:
				backlightOn = !backlightOn
				level := 0
				if backlightOn {
					level = c.cfg.Brightness
				}
				_ = hw.SetBacklight(level)
			case ButtonY:
				if be.Long && c.cfg.AllowShutdown {
					c.shutdown(hw)
					return
				}
				if page == PageQR {
					page = PageStatus
				} else {
					page = PageQR
				}
			}
			draw()
		}
	}
}

// shutdown renders a farewell screen and powers the Pi off. Only
// reachable when cfg.AllowShutdown is set and button Y is long-pressed.
func (c *Controller) shutdown(hw panel) {
	c.logger.Warn("safe shutdown requested (button Y long-press)")
	_ = hw.Blit(renderMessage("Shutting down", "It is now safe to remove power"))
	// Non-interactive sudo: the service user needs a NOPASSWD sudoers
	// rule for `systemctl poweroff` (see docs/INSTALLATION.md).
	cmd := exec.Command("sudo", "-n", "systemctl", "poweroff")
	if err := cmd.Run(); err != nil {
		c.logger.Error("shutdown command failed", "err", err,
			"hint", "allowShutdown needs a NOPASSWD sudoers rule for `systemctl poweroff`")
	}
}

func (c *Controller) refreshInterval() time.Duration {
	d := c.cfg.RefreshInterval.Std()
	if d <= 0 {
		d = 5 * time.Second
	}
	return d
}

// Snapshot is the rendered state of the daemon at one instant.
type Snapshot struct {
	URL          string // headline URL — prefers Tailscale when present
	Version      string
	Battery      BatteryStatus
	Net          NetStatus
	Tailscale    TailscaleInfo
	Controller   ControllerStatus
	Transferring bool
	CPUTempC     float64
	Uptime       time.Duration
	Now          time.Time
}

// NetStatus is the best reachable network interface.
type NetStatus struct {
	Up    bool
	IP    string
	Iface string
	SSID  string // associated Wi-Fi network name; empty when wired or unknown
}

// Wireless reports whether the active interface looks like Wi-Fi.
func (n NetStatus) Wireless() bool { return strings.HasPrefix(n.Iface, "wlan") }

// ControllerStatus summarises the connected DJI controller, if any.
type ControllerStatus struct {
	Connected bool
	Label     string // device model, e.g. "DJI RC 2"
	State     string // "online" | "offline" | "unauthorized" | ...
}

// snapshot gathers all live data the screen renders. Battery state
// comes from whatever the RunBattery poller has cached.
func (c *Controller) snapshot() Snapshot {
	s := Snapshot{
		Version:      version.Version,
		Transferring: c.transferBusy(),
		CPUTempC:     cpuTemp(),
		Uptime:       time.Since(c.started),
		Now:          time.Now(),
	}
	s.Net = readNet()
	s.Tailscale = readTailscale()
	s.URL = c.serverURL(s.Net, s.Tailscale)
	s.Controller = controllerStatus(c.registry.Snapshot())
	if b := c.latestBattery.Load(); b != nil {
		s.Battery = *b
	}
	return s
}

// Battery returns the most recent PiSugar reading taken by the
// RunBattery poller, or nil if no battery hardware was detected.
// Safe to call from any goroutine.
func (c *Controller) Battery() *BatteryStatus {
	return c.latestBattery.Load()
}

// SystemInfo is a minimal slice of host telemetry the screen-render
// loop already gathers. Exposed for the HTTP API.
type SystemInfo struct {
	Version   string
	Hostname  string
	Uptime    time.Duration
	CPUTempC  float64
	Net       NetStatus
	Tailscale TailscaleInfo // zero-valued when no tailscale interface is up
}

// TailscaleInfo reports the host's Tailscale address, if any.
type TailscaleInfo struct {
	Up    bool   // a tailscale interface is up with an IPv4
	IP    string // 100.x.x.x assigned by Tailscale
	Iface string // typically "tailscale0"
}

// System returns a fresh telemetry snapshot. Safe to call from any
// goroutine.
func (c *Controller) System() SystemInfo {
	hostname, _ := os.Hostname()
	return SystemInfo{
		Version:   version.Version,
		Hostname:  hostname,
		Uptime:    time.Since(c.started),
		CPUTempC:  cpuTemp(),
		Net:       readNet(),
		Tailscale: readTailscale(),
	}
}

// RenderPage produces a 320×240 RGBA image of the named display page,
// using a fresh snapshot. Used by the web UI's screen mirror so an
// operator can preview what the front panel would show without
// physical access to the device.
func (c *Controller) RenderPage(page Page) image.Image {
	return render(c.snapshot(), page)
}

// ShutdownAllowed reports whether the display.allowShutdown gate is
// open. The UI uses this to surface (or hide) the remote-shutdown
// affordance.
func (c *Controller) ShutdownAllowed() bool {
	return c.cfg.AllowShutdown
}

// Shutdown runs the same `sudo systemctl poweroff` the front-panel
// button-Y long-press uses, but driven from the HTTP API. Gated by
// display.allowShutdown; the service user still needs the NOPASSWD
// sudoers rule documented in docs/INSTALLATION.md.
func (c *Controller) Shutdown(ctx context.Context) error {
	if !c.cfg.AllowShutdown {
		return ErrShutdownDisabled
	}
	c.logger.Warn("safe shutdown requested via API")
	cmd := exec.CommandContext(ctx, "sudo", "-n", "systemctl", "poweroff")
	if err := cmd.Run(); err != nil {
		c.logger.Error("shutdown command failed", "err", err,
			"hint", "allowShutdown needs a NOPASSWD sudoers rule for `systemctl poweroff`")
		return fmt.Errorf("systemctl poweroff: %w", err)
	}
	return nil
}

// serverURL is the address an operator should point the KAM web UI at.
// On a wildcard bind we prefer the Tailscale IP when one is up — it's
// routable from anywhere on the operator's tailnet and stable across
// Wi-Fi networks — and fall back to the LAN IP otherwise.
func (c *Controller) serverURL(n NetStatus, ts TailscaleInfo) string {
	host := c.bind
	switch c.bind {
	case "", "0.0.0.0", "::":
		switch {
		case ts.Up && ts.IP != "":
			host = ts.IP
		default:
			host = n.IP
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d", host, c.port)
}

// controllerStatus reduces the registry's device list to a single
// headline. It prefers an online, authorized device.
func controllerStatus(infos []device.Info) ControllerStatus {
	var best device.Info
	bestRank := -1
	for _, in := range infos {
		rank := 0
		if in.State == "online" {
			rank += 2
		}
		if in.Authorized {
			rank++
		}
		if rank > bestRank {
			bestRank, best = rank, in
		}
	}
	if bestRank < 0 {
		return ControllerStatus{}
	}
	label := best.Model
	if label == "" {
		label = best.ID
	}
	return ControllerStatus{
		Connected: best.State == "online",
		Label:     label,
		State:     best.State,
	}
}

// readNet picks the most useful up, non-loopback IPv4 interface,
// preferring Wi-Fi, then wired.
func readNet() NetStatus {
	ifaces, err := net.Interfaces()
	if err != nil {
		return NetStatus{}
	}
	rank := func(name string) int {
		switch {
		case strings.HasPrefix(name, "wlan"):
			return 3
		case strings.HasPrefix(name, "eth"), strings.HasPrefix(name, "en"):
			return 2
		default:
			return 1
		}
	}
	best, bestRank := NetStatus{}, 0
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			if r := rank(ifc.Name); r > bestRank {
				bestRank = r
				best = NetStatus{Up: true, IP: ip4.String(), Iface: ifc.Name}
			}
		}
	}
	if best.Up && best.Wireless() {
		best.SSID = wifiSSID(best.Iface)
	}
	return best
}

// wifiSSID returns the network name the given wireless interface is
// associated with, or "" when it can't be determined — not connected,
// not actually Wi-Fi, or none of the query tools are installed. It's
// best-effort: we shell out to whichever of the standard Pi-OS wireless
// tools is present and tolerate the others being absent.
func wifiSSID(iface string) string {
	// iwgetid (wireless-tools) is the most direct: prints just the SSID.
	if out, err := exec.Command("iwgetid", iface, "-r").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	// NetworkManager (default on Bookworm): the active connection's SSID.
	// Terse output is "active:ssid" per line, e.g. "yes:MyNetwork".
	if out, err := exec.Command("nmcli", "-t", "-f", "active,ssid", "dev", "wifi").Output(); err == nil {
		for line := range strings.SplitSeq(string(out), "\n") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && parts[0] == "yes" {
				// nmcli -t escapes literal ':' in the SSID as '\:'.
				if s := strings.TrimSpace(strings.ReplaceAll(parts[1], `\:`, ":")); s != "" {
					return s
				}
			}
		}
	}
	// iw is the low-level fallback present on most modern images.
	if out, err := exec.Command("iw", "dev", iface, "link").Output(); err == nil {
		for line := range strings.SplitSeq(string(out), "\n") {
			line = strings.TrimSpace(line)
			if rest, ok := strings.CutPrefix(line, "SSID:"); ok {
				if s := strings.TrimSpace(rest); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

// readTailscale picks the first IPv4 found on an interface whose name
// starts with "tailscale" (typically tailscale0). Returns a zero
// TailscaleInfo when Tailscale isn't installed/up.
func readTailscale() TailscaleInfo {
	ifaces, err := net.Interfaces()
	if err != nil {
		return TailscaleInfo{}
	}
	for _, ifc := range ifaces {
		if !strings.HasPrefix(ifc.Name, "tailscale") {
			continue
		}
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil {
				return TailscaleInfo{Up: true, IP: ip4.String(), Iface: ifc.Name}
			}
		}
	}
	return TailscaleInfo{}
}

// cpuTemp reads the SoC temperature in °C, or 0 if unavailable.
func cpuTemp() float64 {
	b, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp")
	if err != nil {
		return 0
	}
	milli, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return float64(milli) / 1000.0
}

// healthLED maps the snapshot to an RGB status colour:
//
//	green  — all good
//	amber  — warning (no controller, running on battery, or battery low)
//	red    — critical (battery nearly empty)
func healthLED(s Snapshot) (r, g, b bool) {
	if s.Battery.Present && !s.Battery.ExternalPower && s.Battery.Percent <= 10 {
		return true, false, false // red
	}
	warn := !s.Controller.Connected
	if s.Battery.Present && (s.Battery.Percent <= 20 || !s.Battery.ExternalPower) {
		warn = true
	}
	if warn {
		return true, true, false // amber (red + green)
	}
	return false, true, false // green
}
