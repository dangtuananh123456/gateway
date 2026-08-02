package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/dangtuananh123456/gateway/internal/config"
	"github.com/dangtuananh123456/gateway/internal/pdu"
	"github.com/dangtuananh123456/gateway/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("pdu-session stopped", "error", err)
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
		handler,
	)
}
