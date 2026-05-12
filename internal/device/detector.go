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
	"github.com/kamdynamics/kam-transfer/internal/mtp"
	"github.com/kamdynamics/kam-transfer/internal/names"
	"github.com/kamdynamics/kam-transfer/internal/preview"
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

	// previewEnabled gates ESRI fetches. Set from cfg.Map.Provider.
	previewEnabled bool

	// names is the host-side cache of user-set slot names. Optional;
	// nil means "no overrides, use whatever the controller returns".
	names *names.Store
}

// NewRegistry creates a registry. It does not start polling; the API
// server triggers refreshes on demand and on websocket subscription.
func NewRegistry(cfg *config.Config, logger *slog.Logger) (*Registry, error) {
	previewEnabled := cfg.Map.Provider == "esri-world-imagery"
	store, err := names.New(slotNamesPath(cfg))
	if err != nil {
		logger.Warn("slot-name store unavailable", "err", err)
	}
	r := &Registry{
		cfg:            cfg,
		logger:         logger,
		devices:        map[string]Controller{},
		mtpClient:      mtp.New(),
		openMTP:        map[string]*mtp.Device{},
		previewEnabled: previewEnabled,
		names:          store,
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
	r.mu.Lock()
	defer r.mu.Unlock()
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
			// First check: do we already have an open handle for this
			// physical bus/dev pair? List() returns fresh USB-level
			// identifiers like "usb:1-29"; if a previous Refresh saw
			// the same hardware it should be findable.
			usbID := md.Identifier
			if existing := r.findOpenByUSB(usbID); existing != nil {
				seen[existing.Identifier] = true
				if _, dup := r.devices[existing.Identifier]; !dup {
					r.devices[existing.Identifier] = newMTPController(existing, r.logger)
				}
				continue
			}
			if err := r.mtpClient.Open(md); err != nil {
				r.logger.Warn("mtp open failed", "id", usbID, "err", err)
				continue
			}
			// After Open, md.Identifier is the real PTP serial.
			seen[md.Identifier] = true
			r.openMTP[md.Identifier] = md
			r.devices[md.Identifier] = newMTPController(md, r.logger)
		}
		// Close stale handles (device unplugged or claimed by ADB now).
		for id, open := range r.openMTP {
			if !seen[id] {
				r.logger.Debug("closing stale MTP handle", "id", id)
				_ = open.Close()
				delete(r.openMTP, id)
			}
		}

		// Dedup: if we have a working MTP device, drop any ADB
		// entries that are offline DJI devices — they're the same
		// physical hardware and ADB won't ever authorize them.
		if len(r.openMTP) > 0 {
			for id, c := range r.devices {
				info := c.Info()
				if info.ConnectionType == ConnADB && !info.Authorized {
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

// findOpenByUSB looks for an already-open MTP device whose USB-level
// fallback identifier matches usbID. Useful for matching the freshly-
// enumerated "usb:bus-dev" string against handles we opened earlier
// (which now carry the PTP serial as their Identifier).
//
// We use the raw bus_location / devnum from the cgo deviceImpl when
// available; without exposing it here we fall back to a Friendly +
// USB ID string compare. For now there's at most one DJI controller
// in practice so a linear scan is fine.
func (r *Registry) findOpenByUSB(_ string) *mtp.Device {
	// Linear scan: any DJI device with a real PTP serial is a hit,
	// because we filter to one vendor and the user typically has at
	// most one controller plugged in. If we ever need to disambiguate
	// multiple controllers, expose bus/dev on mtp.Device and match.
	for _, d := range r.openMTP {
		return d
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
// call before any device is connected; if adb is unavailable the
// channel is closed immediately.
func (r *Registry) Watch(ctx context.Context) <-chan Event {
	out := make(chan Event, 16)
	if r.adbClient == nil {
		close(out)
		return out
	}
	go func() {
		defer close(out)
		src := r.adbClient.Watch()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				out <- translateStateChange(ev)
			}
		}
	}()
	return out
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
	return r.applySavedNames(deviceID, slots), nil
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
		pm.Waypoints = append(pm.Waypoints, preview.Waypoint{Lat: w.Lat, Lng: w.Lng})
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
	return c.ClearSlot(guid)
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
