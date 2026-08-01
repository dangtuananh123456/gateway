package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dangtuananh123456/gateway/internal/pdu"
)

const defaultAddress = ":8081"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("pdu-session stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	instanceID, err := os.Hostname()
	if err != nil {
		return err
	}

	logger.Info(
		"pdu-session starting",
		"address", defaultAddress,
		"instance_id", instanceID,
	)

	return pdu.Run(
		ctx,
		pdu.ServerConfig{
			Address:           defaultAddress,
			ReadHeaderTimeout: 2 * time.Second,
			IdleTimeout:       60 * time.Second,
			ShutdownTimeout:   10 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
		pdu.NewHandler(instanceID),
	)
}
