package device

import (
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
)

// ErrUnknownDevice is returned when an API call references a device the
// registry doesn't currently know about.
var ErrUnknownDevice = errors.New("unknown device")

// Registry tracks all currently connected devices and routes API calls
// to the correct Controller. It owns the ADB transport.
type Registry struct {
	cfg    *config.Config
	logger *slog.Logger

	mu      sync.RWMutex
	devices map[string]Controller

	adbClient *adb.Client
}

// NewRegistry creates a registry. It does not start polling; the API
// server triggers refreshes on demand and on websocket subscription.
func NewRegistry(cfg *config.Config, logger *slog.Logger) (*Registry, error) {
	t, err := adb.Dial(cfg.ADB.ServerHost, cfg.ADB.ServerPort)
	if err != nil {
		// We don't fail registry construction if adb-server is unreachable;
		// the API will simply report zero devices until the user starts adb.
		logger.Warn("adb unavailable at startup", "err", err)
		return &Registry{cfg: cfg, logger: logger, devices: map[string]Controller{}}, nil
	}
	return &Registry{
		cfg:       cfg,
		logger:    logger,
		devices:   map[string]Controller{},
		adbClient: adb.NewClient(t),
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
	if err := r.Refresh(ctx); err != nil {
		return nil, err
	}
	c, err := r.Lookup(deviceID)
	if err != nil {
		return nil, err
	}
	meta := &PreviewMetadata{Name: name}
	return c.WriteKMZ(guid, kmz, meta)
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
	dji, _ := c.dev.HasDJIFly()
	return Info{
		ID:             c.dev.Serial,
		Model:          modelLabel(c.dev),
		ConnectionType: ConnADB,
		Authorized:     auth,
		DJIFlyDetected: dji,
	}
}

func (c *adbController) ListSlots() ([]Slot, error) {
	// TODO: real implementation walks WaypointDir on the device and
	// parses each <GUID>/<GUID>.kmz. For now we shell out to `ls -l`
	// so the wiring is testable end-to-end.
	out, err := c.dev.Shell(fmt.Sprintf("ls -l %q 2>/dev/null", WaypointDir))
	if err != nil {
		return nil, err
	}
	slots := []Slot{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "total") {
			continue
		}
		// We expect directory entries named <GUID>.
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[len(fields)-1]
		if !looksLikeGUID(name) {
			continue
		}
		slots = append(slots, Slot{
			GUID:             name,
			Name:             "Slot " + name[:8],
			LastModified:     time.Time{},
			FileSize:         0,
			PreviewAvailable: false,
		})
	}
	return slots, nil
}

func (c *adbController) ReadPreview(guid string) (io.ReadCloser, error) {
	// TODO: pull bytes via sync; for now signal not-implemented.
	return nil, fmt.Errorf("preview read not yet implemented")
}

func (c *adbController) WriteKMZ(guid string, kmz io.Reader, meta *PreviewMetadata) (*TransferResult, error) {
	paths := PathsFor(guid)
	if err := c.dev.Push(kmz, paths.KMZ, 0o644); err != nil {
		return nil, err
	}
	return &TransferResult{
		Success:       true,
		GUID:          guid,
		TransferredAt: time.Now(),
	}, nil
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
