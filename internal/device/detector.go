package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/adb"
	"github.com/kamdynamics/kam-transfer/internal/config"
	"github.com/kamdynamics/kam-transfer/internal/kmz"
	"github.com/kamdynamics/kam-transfer/internal/managed"
	"github.com/kamdynamics/kam-transfer/internal/mtp"
	"github.com/kamdynamics/kam-transfer/internal/names"
	"github.com/kamdynamics/kam-transfer/internal/preview"
	"github.com/kamdynamics/kam-transfer/internal/slotorder"
)

// ErrUnknownDevice is returned when an API call references a device the
// registry doesn't currently know about.
var ErrUnknownDevice = errors.New("unknown device")

// ErrPreviewNotFound is returned when a slot has no preview JPEG on device.
var ErrPreviewNotFound = errors.New("preview not found")

// ErrSlotNotFound is returned when the requested GUID doesn't exist on device.
var ErrSlotNotFound = errors.New("slot not found")

// Registry tracks all currently connected devices and routes API calls
// to the correct Controller. It owns the ADB transport.
type Registry struct {
	cfg    *config.Config
	logger *slog.Logger

	mu      sync.RWMutex
	devices map[string]Controller

	adbClient *adb.Client
	mtpClient *mtp.Client

	// openMTP tracks currently-open MTP devices so Close can release
	// them on Refresh + shutdown. Keyed by Device.Identifier.
	openMTP map[string]*mtp.Device

	// mtpControllers caches per-device mtpController instances across
	// Refreshes. Each Refresh wipes r.devices and rebuilds it, but the
	// controllers hold expensive caches (waypointDir + previewDir
	// FileEntries that take a multi-second PTP path walk to populate)
	// that we want to preserve while the underlying *mtp.Device handle
	// is still valid. Keyed by Device.Identifier; entries are dropped
	// when openMTP drops the matching handle.
	mtpControllers map[string]*mtpController

	// previewEnabled gates ESRI fetches. Set from cfg.Map.Provider.
	previewEnabled bool

	// names is the host-side cache of user-set slot names. Optional;
	// nil means "no overrides, use whatever the controller returns".
	names *names.Store

	// order is the per-device user-chosen slot ordering. Optional;
	// nil means "use whatever the controller returns".
	order *slotorder.Store

	// managed records the per-slot user opt-out (default true; only
	// false when the user has explicitly unchecked "managed").
	managed *managed.Store

	// eventBus carries internally-produced events (e.g. an MTP
	// controller's background locateWaypointDir finishing) to whoever
	// is currently consuming Watch's channel. Buffered so producers
	// never block; drops on overflow with a debug log.
	eventBus chan Event
}

// NewRegistry creates a registry. It does not start polling; the API
// server triggers refreshes on demand and on websocket subscription.
func NewRegistry(cfg *config.Config, logger *slog.Logger) (*Registry, error) {
	previewEnabled := cfg.Map.Provider == "esri-world-imagery"
	store, err := names.New(slotNamesPath(cfg))
	if err != nil {
		logger.Warn("slot-name store unavailable", "err", err)
	}
	orderStore, err := slotorder.New(slotOrderPath(cfg))
	if err != nil {
		logger.Warn("slot-order store unavailable", "err", err)
	}
	managedStore, err := managed.New(managedPath(cfg))
	if err != nil {
		logger.Warn("managed-flag store unavailable", "err", err)
	}
	r := &Registry{
		cfg:            cfg,
		logger:         logger,
		devices:        map[string]Controller{},
		mtpClient:      mtp.New(),
		openMTP:        map[string]*mtp.Device{},
		mtpControllers: map[string]*mtpController{},
		eventBus:       make(chan Event, 64),
		previewEnabled: previewEnabled,
		names:          store,
		order:          orderStore,
		managed:        managedStore,
	}
	t, err := adb.Dial(cfg.ADB.ServerHost, cfg.ADB.ServerPort)
	if err != nil {
		// We don't fail registry construction if adb-server is unreachable;
		// the API will simply report zero devices until the user starts adb.
		logger.Warn("adb unavailable at startup", "err", err)
		return r, nil
	}
	r.adbClient = adb.NewClient(t)
	return r, nil
}

// Refresh re-scans the underlying transports and rebuilds the device map.
//
// Discovery order: ADB first, MTP second. If a device shows up on both
// transports (rare for DJI hardware, but common for Android phones
// running DJI Fly with USB debugging on), ADB wins — it's faster and
// has richer file ops.
func (r *Registry) Refresh(ctx context.Context) error {
	start := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() {
		r.logger.Info("registry refresh", "elapsed", time.Since(start))
	}()
	r.devices = map[string]Controller{}

	// ADB — best effort. The MTP path on DJI RC 2 kills adb-server to
	// free the USB interface, which makes subsequent ListDevices fail
	// with ServerNotAvailable. That's expected; we don't want the UI
	// to show "Could not list devices" just because ADB is down.
	// Log once per Refresh and continue to MTP. If the user starts
	// adb-server manually later, the next Refresh will pick it up.
	if r.adbClient != nil {
		devs, err := r.adbClient.ListDevices()
		if err != nil {
			r.logger.Debug("adb list failed (continuing with MTP)", "err", err)
		} else {
			for _, d := range devs {
				r.devices[d.Serial] = newADBController(d, r.logger)
			}
		}
	}

	// MTP: enumerate raw devices and (re)open them. After both
	// transports report in, we run a dedup pass below: any ADB device
	// that's offline AND a DJI vendor device is treated as a shadow of
	// whichever MTP entry has the same hardware (vendor 0x2ca3 means
	// "this is a DJI controller; ADB will never authorize"), and the
	// dead-weight ADB entry is removed.
	if r.mtpClient != nil {
		mtpDevs, err := r.mtpClient.List()
		if err != nil {
			r.logger.Warn("mtp list failed", "err", err)
		}
		// Track which post-open serials we touched this Refresh, so the
		// cleanup at the end only closes truly-stale handles. The map
		// is keyed on the *post-Open* Identifier (the real PTP serial),
		// because that's what openMTP is keyed on.
		seen := map[string]bool{}
		for _, md := range mtpDevs {
			if !isDJI(md) {
				continue
			}
			usbID := md.Identifier
			// Reuse an existing handle only if it's the same physical
			// connection AND it still answers on the wire. The
			// bus/devnum check catches unplug+replug between refreshes
			// (kernel assigns a new devnum on replug), and Ping catches
			// the rarer case where the device went away without the
			// usb id changing (cable bump, suspend/resume, another
			// process stealing the USB interface mid-session).
			if existing := r.findOpenByBusDev(md.USBBus, md.USBDev); existing != nil {
				// If the controller's background locateWaypointDir walk
				// is in flight, skip Ping. Ping locks d.mu, and
				// mtp.LookupPath holds d.mu for the entire ~10s path
				// traversal — so Pinging here would serialize every
				// /api/devices call behind the walk, even though the
				// walk's own USB traffic already proves the handle is
				// alive. This is the dominant source of cascading
				// 10s+ Refresh latencies after a replug.
				if c, ok := r.mtpControllers[existing.Identifier]; ok && c.isLocating() {
					seen[existing.Identifier] = true
					r.devices[existing.Identifier] = c
					continue
				}
				if err := existing.Ping(); err == nil {
					seen[existing.Identifier] = true
					r.devices[existing.Identifier] = r.controllerFor(existing)
					continue
				} else {
					r.logger.Info("MTP handle stale, reopening", "id", existing.Identifier, "err", err)
					_ = existing.Close()
					delete(r.openMTP, existing.Identifier)
					delete(r.mtpControllers, existing.Identifier)
				}
			}
			openStart := time.Now()
			if err := r.mtpClient.Open(md); err != nil {
				r.logger.Warn("mtp open failed", "id", usbID, "elapsed", time.Since(openStart), "err", err)
				continue
			}
			r.logger.Info("mtp open", "id", md.Identifier, "elapsed", time.Since(openStart))
			seen[md.Identifier] = true
			r.openMTP[md.Identifier] = md
			r.devices[md.Identifier] = r.controllerFor(md)
		}
		// Close stale handles (device unplugged or claimed by ADB now).
		// LIBMTP_Release_Device sends a PTP CloseSession before tearing
		// down the libusb claim, and on a dead device that hits the
		// libusb per-endpoint timeout — up to ~10s per handle. We don't
		// want the API response (the user's UI badge update) to wait on
		// that, so we hand off Close to a goroutine after removing the
		// entry from openMTP. The Device struct has no other references
		// at this point — r.devices was rebuilt above and openMTP no
		// longer holds it — so the goroutine has exclusive ownership.
		for id, open := range r.openMTP {
			if !seen[id] {
				delete(r.openMTP, id)
				delete(r.mtpControllers, id)
				go func(d *mtp.Device, deviceID string) {
					closeStart := time.Now()
					_ = d.Close()
					r.logger.Info("stale mtp handle closed", "id", deviceID, "elapsed", time.Since(closeStart))
				}(open, id)
			}
		}

		// Dedup: if we have a working MTP device, drop any ADB
		// entries that are offline DJI devices — they're the same
		// physical hardware and ADB won't ever authorize them.
		//
		// Only consider ADB entries here. Calling Info() on the MTP
		// controller would trigger locateWaypointDir's multi-second
		// PTP path walk for no benefit — MTP devices are never
		// candidates for the dedup-by-shadow check.
		if len(r.openMTP) > 0 {
			for id, c := range r.devices {
				adbDev, ok := c.(*adbController)
				if !ok {
					continue
				}
				info := adbDev.Info()
				if !info.Authorized {
					// Heuristic: ADB serials for DJI controllers are
					// short alphanumeric (e.g. 6UZTN78001TD1T). If
					// this entry is offline and we have a live MTP DJI
					// device, treat it as a shadow of the MTP one.
					r.logger.Debug("hiding shadow ADB entry (same hardware as MTP device)", "adb_id", id)
					delete(r.devices, id)
				}
			}
		}
	}
	return nil
}

// controllerFor returns the cached mtpController for d, creating one
// on first use. The cache is what lets locateWaypointDir's expensive
// PTP path walk run at most once per connection — without it, every
// Refresh creates a fresh controller with a nil waypointDir cache and
// the walk repeats. Caller must hold r.mu.
//
// On first creation we also kick off the walk in a background goroutine
// (kickoffLocate is idempotent). The onLocated callback emits a
// device.refreshed event when the walk finishes so the UI's WebSocket
// handler re-fetches /api/devices and the DJIFlyDetected indicator
// flips from false to its true value without the user waiting on the
// initial /api/devices call.
func (r *Registry) controllerFor(d *mtp.Device) *mtpController {
	if c, ok := r.mtpControllers[d.Identifier]; ok {
		return c
	}
	deviceID := d.Identifier
	c := newMTPController(d, r.logger, func() {
		r.emit(Event{Type: "device.refreshed", DeviceID: deviceID})
	})
	r.mtpControllers[deviceID] = c
	c.kickoffLocate()
	return c
}

// emit pushes an event onto the internal bus that Watch drains into
// its output channel. Non-blocking — drops on overflow rather than
// blocking the caller (typically a goroutine completing a background
// walk; we'd rather lose a refresh event than wedge the walk).
func (r *Registry) emit(ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	select {
	case r.eventBus <- ev:
	default:
		r.logger.Debug("eventBus full, dropping event", "type", ev.Type, "deviceId", ev.DeviceID)
	}
}

// findOpenByBusDev looks for an already-open MTP device whose current
// USB bus_location + devnum match. After unplug+replug the kernel
// hands out a new devnum, so a mismatch is how we tell "this is a
// fresh connection that needs its own Open" from "same controller as
// last refresh, reuse the handle." The post-Open Identifier becomes
// the PTP serial — stable across reconnects — which is why we can't
// just compare Identifiers to detect a replug.
func (r *Registry) findOpenByBusDev(bus, dev uint32) *mtp.Device {
	for _, d := range r.openMTP {
		if d.USBBus == bus && d.USBDev == dev {
			return d
		}
	}
	return nil
}

// slotNamesPath derives the slot-name sidecar location from the
// config path. We sit it next to config.yaml so a user backing up one
// gets the other automatically.
func slotNamesPath(cfg *config.Config) string {
	cfgPath, err := config.DefaultPath()
	if err != nil || cfg == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), "slot-names.json")
}

// slotOrderPath is the parallel sidecar for user-chosen slot ordering.
func slotOrderPath(cfg *config.Config) string {
	cfgPath, err := config.DefaultPath()
	if err != nil || cfg == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), "slot-order.json")
}

// managedPath is the parallel sidecar for the per-slot managed flag.
func managedPath(cfg *config.Config) string {
	cfgPath, err := config.DefaultPath()
	if err != nil || cfg == nil {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), "slot-managed.json")
}

// SetSlotManaged persists the user's per-slot managed flag.
func (r *Registry) SetSlotManaged(deviceID, guid string, val bool) error {
	if r.managed == nil {
		return fmt.Errorf("managed store unavailable")
	}
	return r.managed.Set(deviceID, guid, val)
}

// SetSlotOrder persists a user-chosen ordering of slot GUIDs for the
// given device. Unknown GUIDs in the order list are ignored when
// applied; missing GUIDs sort to the end.
func (r *Registry) SetSlotOrder(deviceID string, order []string) error {
	if r.order == nil {
		return fmt.Errorf("order store unavailable")
	}
	return r.order.Set(deviceID, order)
}

// RegeneratePreview pulls the KMZ off the device for the named slot,
// renders a fresh preview JPEG, and pushes it back. Useful when DJI
// Fly's editor-Save regen has overwritten our previous push.
func (r *Registry) RegeneratePreview(ctx context.Context, deviceID, guid string) error {
	if err := r.Refresh(ctx); err != nil {
		return err
	}
	c, err := r.Lookup(deviceID)
	if err != nil {
		return err
	}
	puller, ok := c.(KMZReader)
	if !ok {
		return fmt.Errorf("controller does not support KMZ read")
	}
	uploader, ok := c.(PreviewWriter)
	if !ok {
		return fmt.Errorf("controller does not support preview write")
	}
	var kmzBuf bytes.Buffer
	if err := puller.ReadKMZ(guid, &kmzBuf); err != nil {
		return fmt.Errorf("read kmz: %w", err)
	}
	mission, err := kmz.ExtractMission(bytes.NewReader(kmzBuf.Bytes()), int64(kmzBuf.Len()))
	if err != nil {
		return fmt.Errorf("parse kmz: %w", err)
	}
	// Use the saved slot name if available, else the KMZ's author label,
	// else the truncated GUID.
	displayName := r.names.Get(deviceID, guid)
	if displayName == "" {
		displayName = mission.Name
	}
	if displayName == "" {
		displayName = "Slot " + guid[:8]
	}
	pm := &preview.Metadata{Name: displayName}
	for _, w := range mission.Waypoints {
		pm.Waypoints = append(pm.Waypoints, preview.Waypoint{
			Lat:       w.Lat,
			Lng:       w.Lng,
			HasAction: w.HasMeaningfulAction(),
		})
	}
	if mission.Date != nil {
		pm.Date = *mission.Date
	}
	jpg, err := preview.Generate(ctx, pm, preview.Options{Width: r.cfg.Map.Width, Height: r.cfg.Map.Height})
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	return uploader.WritePreview(guid, bytes.NewReader(jpg))
}

// KMZReader is implemented by controllers that can stream a slot's
// KMZ back to the host. Used by RegeneratePreview.
type KMZReader interface {
	ReadKMZ(guid string, w io.Writer) error
}

// WaypointImageWriter is implemented by controllers that can populate
// the slot's per-waypoint image/ folder.
type WaypointImageWriter interface {
	WriteWaypointImages(guid string, images []WaypointImage) error
}

// PushWaypointImages pulls the slot's KMZ, renders a small satellite
// tile per waypoint, and writes them all into the slot's image/
// folder along with a regenerated ShotSnap.json index. Returns the
// number of waypoints whose images were pushed.
func (r *Registry) PushWaypointImages(ctx context.Context, deviceID, guid string) (int, error) {
	if err := r.Refresh(ctx); err != nil {
		return 0, err
	}
	c, err := r.Lookup(deviceID)
	if err != nil {
		return 0, err
	}
	puller, ok := c.(KMZReader)
	if !ok {
		return 0, fmt.Errorf("controller does not support KMZ read")
	}
	writer, ok := c.(WaypointImageWriter)
	if !ok {
		return 0, fmt.Errorf("controller does not support waypoint image write")
	}
	var kmzBuf bytes.Buffer
	if err := puller.ReadKMZ(guid, &kmzBuf); err != nil {
		return 0, fmt.Errorf("read kmz: %w", err)
	}
	mission, err := kmz.ExtractMission(bytes.NewReader(kmzBuf.Bytes()), int64(kmzBuf.Len()))
	if err != nil {
		return 0, fmt.Errorf("parse kmz: %w", err)
	}

	ts := time.Now().UnixMilli()
	images := make([]WaypointImage, 0, len(mission.Waypoints))
	for i, wp := range mission.Waypoints {
		jpg, err := preview.RenderWaypoint(ctx, wp.Lat, wp.Lng, i+1, wp.Actions, preview.WaypointOptions{})
		if err != nil {
			return i, fmt.Errorf("render waypoint %d: %w", i+1, err)
		}
		// Pretend-millis-style naming, prefixed so we recognize our own
		// files (and so DJI Fly's collision detection sees them as new).
		name := fmt.Sprintf("WP_kam_%d_%d.jpg", i, ts+int64(i))
		images = append(images, WaypointImage{Index: i, Name: name, Bytes: jpg})
	}
	if err := writer.WriteWaypointImages(guid, images); err != nil {
		return 0, err
	}
	return len(images), nil
}

// ReadKMZ streams the slot's on-device KMZ into w. Used by the
// /api/devices/.../slots/.../kmz endpoint so users can pull the
// current mission off the controller.
func (r *Registry) ReadKMZ(deviceID, guid string, w io.Writer) error {
	c, err := r.Lookup(deviceID)
	if err != nil {
		return err
	}
	puller, ok := c.(KMZReader)
	if !ok {
		return fmt.Errorf("controller does not support KMZ read")
	}
	return puller.ReadKMZ(guid, w)
}

// SetSlotName persists a user-assigned name for a slot.
func (r *Registry) SetSlotName(deviceID, guid, name string) error {
	if r.names == nil {
		return fmt.Errorf("name store unavailable")
	}
	return r.names.Set(deviceID, guid, name)
}

// applySavedNames overlays user-set names onto the slot list. The
// controller returns its default ("Slot XXXXXXXX"); we replace with
// whatever the user assigned, if anything.
func (r *Registry) applySavedNames(deviceID string, slots []Slot) []Slot {
	if r.names == nil {
		return slots
	}
	for i := range slots {
		if n := r.names.Get(deviceID, slots[i].GUID); n != "" {
			slots[i].Name = n
		}
	}
	return slots
}

// isDJI filters MTP devices down to DJI hardware by USB vendor ID.
// 0x2ca3 is DJI Technology Co., Ltd. Other vendors detected over MTP
// (random Android phones, cameras) aren't useful for this app.
func isDJI(d *mtp.Device) bool {
	return d.Vendor == 0x2ca3
}

// List returns one Info per connected device.
func (r *Registry) List(ctx context.Context) ([]Info, error) {
	if err := r.Refresh(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.devices))
	for _, c := range r.devices {
		out = append(out, c.Info())
	}
	return out, nil
}

// Snapshot returns one Info per currently-known device without
// triggering a Refresh. Unlike List it performs no I/O — it reads the
// cached registry state under the read lock. Intended for callers that
// poll frequently (e.g. the front-panel status display) and rely on
// Watch events to keep that cache fresh.
func (r *Registry) Snapshot() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.devices))
	for _, c := range r.devices {
		out = append(out, c.Info())
	}
	return out
}

// Lookup returns the Controller for id, or ErrUnknownDevice.
func (r *Registry) Lookup(id string) (Controller, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.devices[id]
	if !ok {
		return nil, ErrUnknownDevice
	}
	return c, nil
}

// Event is the registry-level event emitted by Watch.
type Event struct {
	Type     string    `json:"type"` // "device.connected" | "device.disconnected" | "device.authorized" | "device.unauthorized"
	DeviceID string    `json:"deviceId"`
	At       time.Time `json:"at"`
}

// Watch streams device-state events until ctx is cancelled. Safe to
// call before any device is connected.
//
// Two sources feed the channel:
//   - The ADB watcher (adb-server's own hotplug notifications), which is
//     instantaneous but only sees ADB-visible devices.
//   - An MTP hotplug poller, because MTP-only devices like the DJI RC 2
//     never surface through ADB and libmtp has no native hotplug API.
//
// The MTP poller does a periodic libusb-level scan (the same one
// `LIBMTP_Detect_Raw_Devices` performs at startup), diffs the result
// against the previous tick, and emits synthetic device.connected /
// device.disconnected events. Without it the UI's WebSocket sees no
// signal when an RC 2 is unplugged and the badge stays green until
// the user does a manual refresh.
func (r *Registry) Watch(ctx context.Context) <-chan Event {
	out := make(chan Event, 16)

	var wg sync.WaitGroup
	if r.adbClient != nil {
		wg.Go(func() {
			src := r.adbClient.Watch()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-src:
					if !ok {
						return
					}
					select {
					case out <- translateStateChange(ev):
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	if r.mtpClient != nil {
		wg.Go(func() {
			r.pollMTPHotplug(ctx, out)
		})
	}
	// Drain the internal event bus into the same output channel. This
	// is how goroutines that don't have a direct handle on `out` (e.g.
	// the locateWaypointDir background walker) publish events.
	wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-r.eventBus:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	})

	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

// mtpHotplugInterval is how often we re-scan the USB bus for MTP
// devices. Short enough to feel snappy in the UI, long enough that
// libusb doesn't dominate any CPU profile. The scan is the same one
// LIBMTP_Detect_Raw_Devices runs and doesn't touch open PTP sessions.
const mtpHotplugInterval = 2 * time.Second

// pollMTPHotplug ticks at mtpHotplugInterval and emits synthetic
// connect/disconnect events whenever the set of DJI MTP devices on
// the bus changes. The diff key is the pre-Open Identifier
// ("usb:<bus>-<dev>"), which is what mtp.Client.List() returns before
// Open replaces it with the PTP serial — stable for a single
// connection, distinct across replugs (the kernel hands out a new
// devnum each time). The PTP serial in r.devices isn't usable here
// because we may not have Opened the device yet on the first tick.
func (r *Registry) pollMTPHotplug(ctx context.Context, out chan<- Event) {
	ticker := time.NewTicker(mtpHotplugInterval)
	defer ticker.Stop()

	prev := map[string]struct{}{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		devs, err := r.mtpClient.List()
		if err != nil {
			// Don't reset prev — a transient libusb hiccup shouldn't
			// fabricate a wave of disconnect events.
			r.logger.Debug("mtp hotplug list failed", "err", err)
			continue
		}
		curr := make(map[string]struct{}, len(devs))
		for _, d := range devs {
			if !isDJI(d) {
				continue
			}
			curr[d.Identifier] = struct{}{}
		}

		now := time.Now()
		for id := range prev {
			if _, still := curr[id]; !still {
				select {
				case out <- Event{Type: "device.disconnected", DeviceID: id, At: now}:
				case <-ctx.Done():
					return
				}
			}
		}
		for id := range curr {
			if _, was := prev[id]; !was {
				select {
				case out <- Event{Type: "device.connected", DeviceID: id, At: now}:
				case <-ctx.Done():
					return
				}
			}
		}
		prev = curr
	}
}

func translateStateChange(ev adb.StateChange) Event {
	now := time.Now()
	switch {
	case ev.Current == adb.StateDevice && ev.Previous != adb.StateDevice:
		return Event{Type: "device.connected", DeviceID: ev.Serial, At: now}
	case ev.Current != adb.StateDevice && ev.Previous == adb.StateDevice:
		return Event{Type: "device.disconnected", DeviceID: ev.Serial, At: now}
	case ev.Current == adb.StateUnauthorized:
		return Event{Type: "device.unauthorized", DeviceID: ev.Serial, At: now}
	case ev.Current == adb.StateDevice && ev.Previous == adb.StateUnauthorized:
		return Event{Type: "device.authorized", DeviceID: ev.Serial, At: now}
	default:
		return Event{Type: "device.statechange", DeviceID: ev.Serial, At: now}
	}
}

// Convenience wrappers used by the CLI.

func (r *Registry) ListSlots(ctx context.Context, deviceID string) ([]Slot, error) {
	if err := r.Refresh(ctx); err != nil {
		return nil, err
	}
	c, err := r.Lookup(deviceID)
	if err != nil {
		return nil, err
	}
	slots, err := c.ListSlots()
	if err != nil {
		return nil, err
	}
	slots = r.applySavedNames(deviceID, slots)
	for i := range slots {
		if r.managed != nil {
			slots[i].Managed = r.managed.Get(deviceID, slots[i].GUID)
		} else {
			slots[i].Managed = true
		}
	}
	if r.order != nil {
		slots = slotorder.Reorder(r.order.Get(deviceID), slots, func(s Slot) string { return s.GUID })
	}
	return slots, nil
}

func (r *Registry) Transfer(ctx context.Context, deviceID, guid, name string, kmz io.Reader) (*TransferResult, error) {
	return r.TransferWithMeta(ctx, deviceID, guid, kmz, &PreviewMetadata{Name: name})
}

// TransferWithMeta is the full-fidelity transfer entry point. If meta has
// waypoints and previews are enabled, the registry renders + uploads a
// preview JPEG alongside the KMZ.
func (r *Registry) TransferWithMeta(ctx context.Context, deviceID, guid string, kmz io.Reader, meta *PreviewMetadata) (*TransferResult, error) {
	if err := r.Refresh(ctx); err != nil {
		return nil, err
	}
	c, err := r.Lookup(deviceID)
	if err != nil {
		return nil, err
	}
	res, err := c.WriteKMZ(guid, kmz, meta)
	if err != nil {
		return nil, err
	}
	// Persist user-assigned name so subsequent slot listings (and the
	// next preview render) reflect it.
	if meta != nil && meta.Name != "" && r.names != nil {
		if perr := r.names.Set(deviceID, guid, meta.Name); perr != nil {
			r.logger.Warn("could not save slot name", "guid", guid, "err", perr)
		}
	}
	if r.previewEnabled && meta != nil && len(meta.Waypoints) > 0 {
		if perr := r.uploadPreview(ctx, c, guid, meta); perr != nil {
			r.logger.Warn("preview upload failed", "guid", guid, "err", perr)
			// Don't fail the transfer — the KMZ already landed.
		}
	}
	return res, nil
}

// uploadPreview renders meta into a JPEG via internal/preview and pushes
// it to the device's map_preview/<GUID>.jpg location.
func (r *Registry) uploadPreview(ctx context.Context, c Controller, guid string, meta *PreviewMetadata) error {
	pm := &preview.Metadata{
		Name: meta.Name,
		Date: meta.Date,
	}
	for _, w := range meta.Waypoints {
		pm.Waypoints = append(pm.Waypoints, preview.Waypoint{Lat: w.Lat, Lng: w.Lng, HasAction: w.HasAction})
	}
	jpg, err := preview.Generate(ctx, pm, preview.Options{
		Width:  r.cfg.Map.Width,
		Height: r.cfg.Map.Height,
	})
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	uploader, ok := c.(PreviewWriter)
	if !ok {
		return fmt.Errorf("controller does not support preview write")
	}
	return uploader.WritePreview(guid, bytes.NewReader(jpg))
}

// PreviewWriter is implemented by controllers that can push a preview JPEG.
// adbController satisfies it; future MTP controllers may not.
type PreviewWriter interface {
	WritePreview(guid string, jpg io.Reader) error
}

func (r *Registry) ClearSlot(ctx context.Context, deviceID, guid string) error {
	if err := r.Refresh(ctx); err != nil {
		return err
	}
	c, err := r.Lookup(deviceID)
	if err != nil {
		return err
	}
	if err := c.ClearSlot(guid); err != nil {
		return err
	}
	// Drop the saved name + per-slot user data on our side too.
	// Slot-order entries are left alone — the slot still exists, the
	// user just emptied its mission.
	if r.names != nil {
		_ = r.names.Set(deviceID, guid, "")
	}
	return nil
}

func (r *Registry) ReadPreview(deviceID, guid string) (io.ReadCloser, error) {
	c, err := r.Lookup(deviceID)
	if err != nil {
		return nil, err
	}
	return c.ReadPreview(guid)
}

// --- ADB-backed Controller --------------------------------------------------

type adbController struct {
	dev    *adb.Device
	logger *slog.Logger
}

func newADBController(d *adb.Device, logger *slog.Logger) *adbController {
	return &adbController{dev: d, logger: logger}
}

func (c *adbController) Info() Info {
	auth, _ := c.dev.Authorized()
	state := string(c.dev.State)
	if !auth {
		// Refresh state in case the cached value is stale.
		if st, err := c.dev.RawState(); err == nil {
			state = string(st)
			c.dev.State = st
		}
	}
	info := Info{
		ID:             c.dev.Serial,
		Model:          modelLabel(c.dev),
		ConnectionType: ConnADB,
		Authorized:     auth,
		State:          state,
	}
	if auth {
		info.DJIFlyDetected, _ = c.dev.HasDJIFly()
	} else {
		switch state {
		case string(adb.StateUnauthorized):
			info.Hint = "USB debugging authorization required: tap 'Allow' on the controller screen."
		case string(adb.StateOffline):
			info.Hint = "Controller is offline. Approve USB debugging on the device, or unplug + replug if no prompt appears."
		}
	}
	return info
}

func (c *adbController) ListSlots() ([]Slot, error) {
	entries, err := c.dev.ListDir(WaypointDir)
	if err != nil {
		return nil, err
	}
	slots := make([]Slot, 0, len(entries))
	for _, e := range entries {
		if !looksLikeGUID(e.Name) {
			// Skip non-slot entries (e.g. map_preview/).
			continue
		}
		paths := PathsFor(e.Name)
		kmzStat, kmzExists, _ := c.dev.Stat(paths.KMZ)
		_, previewExists, _ := c.dev.Stat(paths.Preview)
		slot := Slot{
			GUID:             e.Name,
			Name:             "Slot " + e.Name[:8],
			LastModified:     e.ModifiedAt,
			PreviewAvailable: previewExists,
		}
		if kmzExists {
			slot.FileSize = kmzStat.Size
			if kmzStat.ModifiedAt.After(slot.LastModified) {
				slot.LastModified = kmzStat.ModifiedAt
			}
		}
		slots = append(slots, slot)
	}
	return slots, nil
}

func (c *adbController) ReadPreview(guid string) (io.ReadCloser, error) {
	paths := PathsFor(guid)
	if _, ok, err := c.dev.Stat(paths.Preview); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrPreviewNotFound
	}
	return c.dev.OpenRead(paths.Preview)
}

func (c *adbController) WriteKMZ(guid string, kmz io.Reader, meta *PreviewMetadata) (*TransferResult, error) {
	paths := PathsFor(guid)
	if err := c.dev.Push(kmz, paths.KMZ, 0o644); err != nil {
		return nil, err
	}
	// Best-effort: refresh size after push.
	var size int64
	if stat, ok, _ := c.dev.Stat(paths.KMZ); ok {
		size = stat.Size
	}
	return &TransferResult{
		Success:       true,
		GUID:          guid,
		FileSize:      size,
		TransferredAt: time.Now(),
	}, nil
}

// WritePreview pushes a JPEG into the device's map_preview directory.
func (c *adbController) WritePreview(guid string, jpg io.Reader) error {
	paths := PathsFor(guid)
	return c.dev.Push(jpg, paths.Preview, 0o644)
}

func (c *adbController) ClearSlot(guid string) error {
	// TODO: decide on placeholder strategy. For now we just refuse,
	// since deleting outright would break DJI Fly's slot.
	return fmt.Errorf("clear-slot not yet implemented (see TROUBLESHOOTING.md)")
}

func looksLikeGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	return strings.Count(s, "-") == 4
}

func modelLabel(d *adb.Device) string {
	if d.Model != "" {
		return d.Model
	}
	return "Unknown DJI device"
}
