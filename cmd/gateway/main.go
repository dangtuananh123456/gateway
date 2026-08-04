package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dangtuananh123456/gateway/internal/config"
	"github.com/dangtuananh123456/gateway/internal/gateway"
	"github.com/dangtuananh123456/gateway/internal/logging"
	"github.com/dangtuananh123456/gateway/internal/profiling"
)

func main() {
	logger := logging.New(os.Stdout)
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
	profilingAddress := os.Getenv("PPROF_ADDRESS")
	if profilingAddress == "" {
		return application.Run(ctx)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	profileDone := make(chan error, 1)
	go func() { profileDone <- profiling.Run(runCtx, profilingAddress) }()
	applicationDone := make(chan error, 1)
	go func() { applicationDone <- application.Run(runCtx) }()

	select {
	case applicationErr := <-applicationDone:
		cancel()
		return errors.Join(applicationErr, <-profileDone)
	case profileErr := <-profileDone:
		cancel()
		applicationErr := <-applicationDone
		if profileErr == nil && ctx.Err() == nil {
			profileErr = errors.New("profiling server stopped unexpectedly")
		}
		return errors.Join(applicationErr, profileErr)
	}
}
