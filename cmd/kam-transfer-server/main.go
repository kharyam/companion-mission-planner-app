// Package main is a thin entrypoint that just runs `kam-transfer serve`.
// Kept as a separate binary so package managers can offer a server-only
// install path without the CLI subcommands. The implementation lives in
// the kam-transfer CLI; here we just exec into it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kamdynamics/kam-transfer/internal/api"
	"github.com/kamdynamics/kam-transfer/internal/config"
	"github.com/kamdynamics/kam-transfer/internal/device"
	"github.com/kamdynamics/kam-transfer/internal/version"
)

func main() {
	cfg, err := config.Load(os.Getenv("KAM_TRANSFER_CONFIG"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	reg, err := device.NewRegistry(cfg, logger)
	if err != nil {
		fmt.Fprintln(os.Stderr, "registry:", err)
		os.Exit(1)
	}
	srv := api.New(cfg, reg, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting kam-transfer-server", "version", version.Version)
	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
