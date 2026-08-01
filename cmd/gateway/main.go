package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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

	return gateway.Run(
		ctx,
		gateway.ServerConfig{
			Address:           cfg.Gateway.Server.Address,
			ReadHeaderTimeout: cfg.Gateway.Server.ReadHeaderTimeout,
			IdleTimeout:       cfg.Gateway.Server.IdleTimeout,
			ShutdownTimeout:   cfg.Gateway.Server.ShutdownTimeout,
			MaxHeaderBytes:    cfg.Gateway.Server.MaxHeaderBytes,
		},
		newHandler(),
	)
}

func newHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"protocol": "h2c",
			"status":   "UP",
		})
	})

	return mux
}
