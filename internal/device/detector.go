package device

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kamdynamics/kam-transfer/internal/adb"
	"github.com/kamdynamics/kam-transfer/internal/config"
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

	// previewEnabled gates ESRI fetches. Set from cfg.Map.Provider.
	previewEnabled bool
}

// NewRegistry creates a registry. It does not start polling; the API
// server triggers refreshes on demand and on websocket subscription.
func NewRegistry(cfg *config.Config, logger *slog.Logger) (*Registry, error) {
	previewEnabled := cfg.Map.Provider == "esri-world-imagery"
	t, err := adb.Dial(cfg.ADB.ServerHost, cfg.ADB.ServerPort)
	if err != nil {
		// We don't fail registry construction if adb-server is unreachable;
		// the API will simply report zero devices until the user starts adb.
		logger.Warn("adb unavailable at startup", "err", err)
		return &Registry{cfg: cfg, logger: logger, devices: map[string]Controller{}, previewEnabled: previewEnabled}, nil
	}
	return &Registry{
		cfg:            cfg,
		logger:         logger,
		devices:        map[string]Controller{},
		adbClient:      adb.NewClient(t),
		previewEnabled: previewEnabled,
	}, nil
}

// Refresh re-scans the underlying transports and rebuilds the device map.
func (r *Registry) Refresh(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices = map[string]Controller{}
	if r.adbClient == nil {
		return nil
	}
	devs, err := r.adbClient.ListDevices()
	if err != nil {
		return err
	}
	for _, d := range devs {
		ctrl := newADBController(d, r.logger)
		r.devices[d.Serial] = ctrl
	}
	return nil
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
	return c.ListSlots()
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
