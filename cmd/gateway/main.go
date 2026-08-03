package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dangtuananh123456/gateway/internal/config"
	"github.com/dangtuananh123456/gateway/internal/gateway"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.LoadDefault()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	logger.Info(
		"gateway starting",
		"address", cfg.Gateway.Server.Address,
		"protocol", "h2c",
		"routing_mode", cfg.Routing.Mode,
		"discovery_hostname", cfg.Discovery.Hostname,
	)

	application, err := gateway.NewApplication(cfg, logger)
	if err != nil {
		return err
	}
	return application.Run(ctx)
}
