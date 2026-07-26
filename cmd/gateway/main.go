package main

import (
	"log/slog"
	"os"

	"github.com/dangtuananh123456/gateway/internal/config"
)

func main() {
	if _, err := config.LoadDefault(); err != nil {
		slog.Error("load gateway configuration", "error", err)
		os.Exit(1)
	}
}
