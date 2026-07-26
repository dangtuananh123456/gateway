package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	mockserver "github.com/dangtuananh123456/gateway/internal/mock/server"
	"github.com/dangtuananh123456/gateway/internal/mock/users"
)

const defaultAddress = ":8081"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if err := run(logger); err != nil {
		logger.Error("mock users stopped", "error", err)
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

	address := environmentOrDefault("MOCK_USERS_ADDRESS", defaultAddress)
	logger.Info("mock users starting", "address", address)

	return mockserver.Run(ctx, defaultServerConfig(address), users.NewHandler())
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
