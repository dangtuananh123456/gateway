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
	flag.Uint64Var(&cfg.RequestCount, "requests", 15000, "exact number of request attempts to schedule")
	flag.Uint64Var(&cfg.MinimumSuccessfulRequests, "min-success", 12000, "minimum successful responses required inside the measurement window")
	flag.DurationVar(&cfg.Duration, "duration", time.Second, "measurement window used to pace requests and calculate TPS")
	flag.IntVar(&cfg.Connections, "connections", 15, "maximum h2c connections")
	flag.IntVar(&cfg.StreamsPerConnection, "streams", 32, "maximum concurrent streams per h2c connection")
	flag.DurationVar(&cfg.RequestTimeout, "request-timeout", 3*time.Second, "timeout for one request")
	flag.BoolVar(&cfg.WarmupConnections, "warm-connections", true, "establish every h2c connection before the measurement window")
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
	if !result.TargetMet {
		fail(fmt.Errorf(
			"target not met: attempted %d/%d, wrote %d/%d in %.3fs, completed %d/%d required successes inside %.3fs",
			result.TotalRequests,
			result.TargetRequests,
			result.SentRequests,
			result.TargetRequests,
			result.DispatchDurationSeconds,
			result.SuccessfulRequests,
			result.TargetSuccessfulRequests,
			result.TargetDurationSeconds,
		))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "load test failed:", err)
	os.Exit(1)
}
