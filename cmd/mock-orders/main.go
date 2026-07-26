package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dangtuananh123456/gateway/internal/mock/orders"
	mockserver "github.com/dangtuananh123456/gateway/internal/mock/server"
)

const defaultAddress = ":8082"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("mock orders stopped", "error", err)
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

	address := environmentOrDefault("MOCK_ORDERS_ADDRESS", defaultAddress)
	logger.Info("mock orders starting", "address", address)

	return mockserver.Run(ctx, defaultServerConfig(address), orders.NewHandler())
}

func defaultServerConfig(address string) mockserver.Config {
	return mockserver.Config{
		Address:           address,
		ReadHeaderTimeout: 2 * time.Second,
		IdleTimeout:       60 * time.Second,
		ShutdownTimeout:   10 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func environmentOrDefault(key string, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}

	return fallback
}
