package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dangtuananh123456/gateway/internal/client"
	"github.com/dangtuananh123456/gateway/internal/logging"
	"github.com/dangtuananh123456/gateway/internal/requestlog"
)

func main() {
	logger := logging.New(os.Stdout)
	if err := run(logger); err != nil {
		logger.Error("test client stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := client.ConfigFromEnvironment()
	if err != nil {
		return err
	}
	transport := client.NewH2CTransport()
	defer transport.CloseIdleConnections()
	handler, err := client.NewHandler(cfg.GatewayURL, &http.Client{
		Transport: transport,
		Timeout:   cfg.RequestTimeout,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("test client starting", "address", cfg.Address, "gateway_url", cfg.GatewayURL)
	return client.Run(ctx, cfg, requestlog.New(logger, "client", handler))
}
