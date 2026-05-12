// Package kamtransfer is the public embedding API for kam-transfer.
//
// Most callers should run the binary instead of importing this package.
// It exists for the case where KAM Mission Planner (or another Go app)
// wants to run the daemon in-process — for example, an Electron-wrapped
// build that bundles the API into the same binary as the UI.
package kamtransfer

import (
	"context"
	"log/slog"

	"github.com/kamdynamics/kam-transfer/internal/api"
	"github.com/kamdynamics/kam-transfer/internal/config"
	"github.com/kamdynamics/kam-transfer/internal/device"
)

// Config is the public mirror of the internal config. Re-exported so
// embedders don't have to import internal/.
type Config = config.Config

// LoadConfig loads from path, or platform default if path is empty.
func LoadConfig(path string) (*Config, error) { return config.Load(path) }

// Daemon bundles the registry + server so embedders only need one type.
type Daemon struct {
	cfg    *Config
	reg    *device.Registry
	server *api.Server
}

// New constructs a Daemon. It does not start listening; call Run.
func New(cfg *Config, logger *slog.Logger) (*Daemon, error) {
	if logger == nil {
		logger = slog.Default()
	}
	reg, err := device.NewRegistry(cfg, logger)
	if err != nil {
		return nil, err
	}
	return &Daemon{
		cfg:    cfg,
		reg:    reg,
		server: api.New(cfg, reg, logger),
	}, nil
}

// Run blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error { return d.server.Run(ctx) }

// Address returns the bound address (for tests / embedders that need to
// know what URL to point a UI at).
func (d *Daemon) Address() string { return d.server.Address() }
