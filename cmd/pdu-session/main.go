package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dangtuananh123456/gateway/internal/config"
	"github.com/dangtuananh123456/gateway/internal/logging"
	"github.com/dangtuananh123456/gateway/internal/pdu"
	"github.com/dangtuananh123456/gateway/internal/requestlog"
	"github.com/dangtuananh123456/gateway/internal/store"
)

func main() {
	cfg, err := config.LoadDefault()
	if err != nil {
		fmt.Fprintln(os.Stderr, "load PDU configuration:", err)
		os.Exit(1)
	}
	logger := logging.NewWithEnabled(os.Stdout, cfg.Logging.Enabled)
	if err := run(cfg, logger); err != nil {
		logger.Error("pdu-session stopped", "error", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	var err error
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	instanceID := cfg.PDU.InstanceID
	if instanceID == "" {
		instanceID, err = os.Hostname()
		if err != nil {
			return err
		}
	}

	logger.Info(
		"pdu-session starting",
		"address", cfg.PDU.Server.Address,
		"instance_id", instanceID,
		"weight", cfg.PDU.Weight,
		"access_log_enabled", cfg.Logging.Enabled && cfg.Logging.AccessLogEnabled,
	)
	handler, err := pdu.NewHandler(
		pdu.HandlerConfig{
			InstanceID:       instanceID,
			Weight:           cfg.PDU.Weight,
			PublicGatewayURL: cfg.Gateway.PublicURL,
			ProcessingDelay:  cfg.PDU.ProcessingDelay,
		},
		store.NewLocal(),
	)
	if err != nil {
		return err
	}

	return pdu.Run(
		ctx,
		pdu.ServerConfig{
			Address:           cfg.PDU.Server.Address,
			ReadHeaderTimeout: cfg.PDU.Server.ReadHeaderTimeout,
			IdleTimeout:       cfg.PDU.Server.IdleTimeout,
			ShutdownTimeout:   cfg.PDU.Server.ShutdownTimeout,
			MaxHeaderBytes:    cfg.PDU.Server.MaxHeaderBytes,
		},
		requestlog.Wrap(
			cfg.Logging.Enabled && cfg.Logging.AccessLogEnabled,
			logger,
			"pdu-session",
			handler,
		),
	)
}
