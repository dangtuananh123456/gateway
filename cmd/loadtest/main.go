package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dangtuananh123456/gateway/internal/loadtest"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func main() {
	cfg := loadtest.Config{}
	flag.StringVar(&cfg.Target, "target", "http://localhost:18080"+constants.CreateSMContextPath, "absolute h2c session API URL")
	flag.DurationVar(&cfg.Duration, "duration", 10*time.Second, "measurement duration")
	flag.IntVar(&cfg.Connections, "connections", 4, "maximum h2c connections")
	flag.IntVar(&cfg.StreamsPerConnection, "streams", 50, "concurrent streams per connection")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", 3*time.Second, "timeout for one request")
	flag.Parse()

	runner, err := loadtest.NewRunner(loadtest.NewH2CTransport)
	if err != nil {
		fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := runner.Run(ctx, cfg)
	if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
		fail(fmt.Errorf("encode load-test result: %w", encodeErr))
	}
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "load test failed:", err)
	os.Exit(1)
}
