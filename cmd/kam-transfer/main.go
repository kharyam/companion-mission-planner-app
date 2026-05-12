package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/kamdynamics/kam-transfer/internal/api"
	"github.com/kamdynamics/kam-transfer/internal/config"
	"github.com/kamdynamics/kam-transfer/internal/device"
	"github.com/kamdynamics/kam-transfer/internal/version"
)

var (
	cfgFile  string
	logLevel string
)

func main() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "kam-transfer",
		Short: "Companion daemon for KAM Mission Planner",
		Long:  "kam-transfer pushes KMZ waypoint missions from KAM Mission Planner onto USB-connected DJI controllers and phones running DJI Fly.",
		// Default action: run the server.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd, args)
		},
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&cfgFile, "config", "", "path to config file (default: platform-appropriate location)")
	root.PersistentFlags().StringVar(&logLevel, "log-level", "", "log level: debug|info|warn|error")

	root.AddCommand(
		newServeCmd(),
		newListDevicesCmd(),
		newListSlotsCmd(),
		newTransferCmd(),
		newClearSlotCmd(),
		newProbeTreeCmd(),
		newProbeCatCmd(),
		newVersionCmd(),
	)
	return root
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, err
	}
	if logLevel != "" {
		cfg.Logging.Level = logLevel
	}
	return cfg, nil
}

func newServeCmd() *cobra.Command {
	var port int
	var bind string
	cmd := &cobra.Command{
		Use:          "serve",
		Short:        "Start the local HTTP API server",
		SilenceUsage: true,
		RunE:         runServe,
	}
	cmd.Flags().IntVar(&port, "port", 0, "override config server.port")
	cmd.Flags().StringVar(&bind, "bind", "", "override config server.bind")
	return cmd
}

func runServe(cmd *cobra.Command, _ []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if v, _ := cmd.Flags().GetInt("port"); v > 0 {
		cfg.Server.Port = v
	}
	if v, _ := cmd.Flags().GetString("bind"); v != "" {
		cfg.Server.Bind = v
	}
	logger := newLogger(cfg.Logging.Level)
	registry, err := device.NewRegistry(cfg, logger)
	if err != nil {
		return err
	}
	srv := api.New(cfg, registry, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting kam-transfer", "version", version.Version, "bind", cfg.Server.Bind, "port", cfg.Server.Port)
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func newListDevicesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-devices",
		Short: "List connected DJI devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			logger := newLogger(cfg.Logging.Level)
			reg, err := device.NewRegistry(cfg, logger)
			if err != nil {
				return err
			}
			devs, err := reg.List(cmd.Context())
			if err != nil {
				return err
			}
			if len(devs) == 0 {
				fmt.Println("No devices found.")
				return nil
			}
			for _, d := range devs {
				fmt.Printf("%s\t%s\tstate=%s\tauthorized=%t\tdji-fly=%t\n",
					d.ID, d.Model, d.State, d.Authorized, d.DJIFlyDetected)
				if d.Hint != "" {
					fmt.Printf("  hint: %s\n", d.Hint)
				}
			}
			return nil
		},
	}
}

func newListSlotsCmd() *cobra.Command {
	var deviceID string
	cmd := &cobra.Command{
		Use:   "list-slots",
		Short: "List waypoint slots on a device",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			logger := newLogger(cfg.Logging.Level)
			reg, err := device.NewRegistry(cfg, logger)
			if err != nil {
				return err
			}
			slots, err := reg.ListSlots(cmd.Context(), deviceID)
			if err != nil {
				return err
			}
			for _, s := range slots {
				fmt.Printf("%s\t%s\t%d bytes\tpreview=%t\n",
					s.GUID, s.Name, s.FileSize, s.PreviewAvailable)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&deviceID, "device", "", "device ID (required)")
	_ = cmd.MarkFlagRequired("device")
	return cmd
}

func newTransferCmd() *cobra.Command {
	var deviceID, slotGUID, name string
	cmd := &cobra.Command{
		Use:   "transfer <kmz-file>",
		Short: "Transfer a KMZ to a slot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			logger := newLogger(cfg.Logging.Level)
			reg, err := device.NewRegistry(cfg, logger)
			if err != nil {
				return err
			}
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			res, err := reg.Transfer(cmd.Context(), deviceID, slotGUID, name, f)
			if err != nil {
				return err
			}
			fmt.Printf("transferred %d bytes to slot %s on device %s\n", res.FileSize, res.GUID, deviceID)
			return nil
		},
	}
	cmd.Flags().StringVar(&deviceID, "device", "", "device ID (required)")
	cmd.Flags().StringVar(&slotGUID, "slot", "", "target slot GUID (required)")
	cmd.Flags().StringVar(&name, "name", "", "optional mission display name")
	_ = cmd.MarkFlagRequired("device")
	_ = cmd.MarkFlagRequired("slot")
	return cmd
}

func newClearSlotCmd() *cobra.Command {
	var deviceID, slotGUID string
	cmd := &cobra.Command{
		Use:   "clear-slot",
		Short: "Mark a slot as available",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			logger := newLogger(cfg.Logging.Level)
			reg, err := device.NewRegistry(cfg, logger)
			if err != nil {
				return err
			}
			return reg.ClearSlot(cmd.Context(), deviceID, slotGUID)
		},
	}
	cmd.Flags().StringVar(&deviceID, "device", "", "device ID (required)")
	cmd.Flags().StringVar(&slotGUID, "slot", "", "slot GUID (required)")
	_ = cmd.MarkFlagRequired("device")
	_ = cmd.MarkFlagRequired("slot")
	return cmd
}

// probe-tree: recursive directory listing on an MTP device. Used while
// reverse-engineering DJI Fly's on-disk layout — not part of the
// shipping API surface, but kept around because it's handy.
func newProbeTreeCmd() *cobra.Command {
	var deviceID, root string
	var depth int
	cmd := &cobra.Command{
		Use:   "probe-tree",
		Short: "Walk an MTP device's file tree (debugging)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			logger := newLogger(cfg.Logging.Level)
			reg, err := device.NewRegistry(cfg, logger)
			if err != nil {
				return err
			}
			if _, err := reg.List(cmd.Context()); err != nil {
				return err
			}
			entries, err := reg.WalkTree(deviceID, root, depth)
			if err != nil {
				return err
			}
			for _, e := range entries {
				prefix := strings.Repeat("  ", e.Depth)
				kind := "-"
				if e.IsFolder {
					kind = "d"
				}
				mtime := ""
				if !e.Mtime.IsZero() {
					mtime = e.Mtime.Format("2006-01-02 15:04")
				}
				fmt.Printf("%s%s %10d  %s  %s\n", prefix, kind, e.Size, mtime, e.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&deviceID, "device", "", "device ID (required)")
	cmd.Flags().StringVar(&root, "root", "Internal shared storage/Android/data/dji.go.v5/files", "root path on the device")
	cmd.Flags().IntVar(&depth, "depth", 0, "max recursion depth (0 = unlimited)")
	_ = cmd.MarkFlagRequired("device")
	return cmd
}

// probe-cat: dump a single file from an MTP device to stdout. Use with
// `--limit` for large files; pipe through xxd / file / less as needed.
func newProbeCatCmd() *cobra.Command {
	var deviceID, path string
	var limit int64
	cmd := &cobra.Command{
		Use:   "probe-cat",
		Short: "Dump an MTP file's contents to stdout (debugging)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			logger := newLogger(cfg.Logging.Level)
			reg, err := device.NewRegistry(cfg, logger)
			if err != nil {
				return err
			}
			if _, err := reg.List(cmd.Context()); err != nil {
				return err
			}
			var w io.Writer = os.Stdout
			if limit > 0 {
				w = &limitedWriter{w: os.Stdout, remaining: limit}
			}
			return reg.ReadDeviceFile(deviceID, path, w)
		},
	}
	cmd.Flags().StringVar(&deviceID, "device", "", "device ID (required)")
	cmd.Flags().StringVar(&path, "path", "", "absolute file path on device (required)")
	cmd.Flags().Int64Var(&limit, "limit", 0, "stop after N bytes (0 = no limit)")
	_ = cmd.MarkFlagRequired("device")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

type limitedWriter struct {
	w         io.Writer
	remaining int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.w.Write(p)
	l.remaining -= int64(n)
	return n, err
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version and exit",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println(version.Version)
		},
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
