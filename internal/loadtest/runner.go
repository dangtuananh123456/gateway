// Package loadtest provides the h2c load generator used for performance acceptance.
package loadtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dangtuananh123456/gateway/internal/requestlog"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// Config defines one fixed-duration load-test workload.
type Config struct {
	Target               string
	Duration             time.Duration
	Connections          int
	StreamsPerConnection int
	RequestTimeout       time.Duration
}

// Result is the machine-readable summary of one measurement window.
type Result struct {
	Target               string            `json:"target"`
	Protocol             string            `json:"protocol"`
	DurationSeconds      float64           `json:"durationSeconds"`
	Connections          int               `json:"connections"`
	StreamsPerConnection int               `json:"streamsPerConnection"`
	ConcurrentStreams    int               `json:"concurrentStreams"`
	TotalRequests        uint64            `json:"totalRequests"`
	SuccessfulRequests   uint64            `json:"successfulRequests"`
	FailedRequests       uint64            `json:"failedRequests"`
	SuccessfulTPS        float64           `json:"successfulTps"`
	LatencyP50Millis     float64           `json:"latencyP50Millis"`
	LatencyP95Millis     float64           `json:"latencyP95Millis"`
	LatencyP99Millis     float64           `json:"latencyP99Millis"`
	StatusCodes          map[string]uint64 `json:"statusCodes"`
	Errors               map[string]uint64 `json:"errors,omitempty"`
}

// TransportFactory creates one reusable transport for one configured connection.
type TransportFactory func() (http.RoundTripper, func())

// Runner executes workloads using an injectable transport boundary.
type Runner struct {
	newTransport TransportFactory
}

// NewRunner creates a load-test runner.
func NewRunner(factory TransportFactory) (*Runner, error) {
	if factory == nil {
		return nil, errors.New("create load-test runner: transport factory must not be nil")
	}
	return &Runner{newTransport: factory}, nil
}

// NewH2CTransport creates an h2c-only transport capped at one live connection.
func NewH2CTransport() (http.RoundTripper, func()) {
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.Protocols = protocols
	transport.DisableCompression = true
	transport.MaxConnsPerHost = 1
	transport.MaxIdleConns = 1
	transport.MaxIdleConnsPerHost = 1
	return transport, transport.CloseIdleConnections
}

// Run sends requests until the measurement context expires.
func (runner *Runner) Run(ctx context.Context, cfg Config) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("run load test: context must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}

	clients := make([]*http.Client, cfg.Connections)
	closeTransports := make([]func(), cfg.Connections)
	for index := range cfg.Connections {
		transport, closeTransport := runner.newTransport()
		if transport == nil || closeTransport == nil {
			for previous := range index {
				closeTransports[previous]()
			}
			return Result{}, errors.New("run load test: transport factory returned a nil dependency")
		}
		clients[index] = &http.Client{Transport: transport}
		closeTransports[index] = closeTransport
	}
	defer func() {
		for _, closeTransport := range closeTransports {
			closeTransport()
		}
	}()

	workerCount := cfg.Connections * cfg.StreamsPerConnection
	results := make(chan workerResult, workerCount)
	start := make(chan struct{})
	runCtx, cancel := context.WithTimeout(ctx, cfg.Duration)
	defer cancel()

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for connectionIndex, client := range clients {
		for streamIndex := range cfg.StreamsPerConnection {
			go func() {
				defer workers.Done()
				<-start
				results <- runWorker(
					runCtx,
					client,
					cfg,
					connectionIndex,
					streamIndex,
				)
			}()
		}
	}

	startedAt := time.Now()
	close(start)
	<-runCtx.Done()
	measurementDuration := time.Since(startedAt)
	workers.Wait()
	close(results)

	result := aggregate(cfg, measurementDuration, results)
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

// Validate rejects workloads that cannot represent an h2c measurement.
func (cfg Config) Validate() error {
	target, err := url.ParseRequestURI(cfg.Target)
	if err != nil || target.Scheme != "http" || target.Host == "" {
		return errors.New("validate load-test config: target must be an absolute http URL")
	}
	if cfg.Duration <= 0 {
		return errors.New("validate load-test config: duration must be greater than zero")
	}
	if cfg.Connections <= 0 {
		return errors.New("validate load-test config: connections must be greater than zero")
	}
	if cfg.StreamsPerConnection <= 0 {
		return errors.New("validate load-test config: streams per connection must be greater than zero")
	}
	if cfg.RequestTimeout <= 0 {
		return errors.New("validate load-test config: request timeout must be greater than zero")
	}
	return nil
}

type workerResult struct {
	total        uint64
	successful   uint64
	latencies    []time.Duration
	statusCodes  map[string]uint64
	errorClasses map[string]uint64
}

func runWorker(
	ctx context.Context,
	client *http.Client,
	cfg Config,
	connectionIndex int,
	streamIndex int,
) workerResult {
	result := workerResult{
		latencies:    make([]time.Duration, 0, 1024),
		statusCodes:  make(map[string]uint64),
		errorClasses: make(map[string]uint64),
	}
	for sequence := uint64(1); ctx.Err() == nil; sequence++ {
		startedAt := time.Now()
		status, err := sendRequest(ctx, client, cfg, connectionIndex, streamIndex, sequence)
		latency := time.Since(startedAt)
		result.total++
		if status != 0 {
			result.statusCodes[strconv.Itoa(status)]++
		}
		if err != nil {
			result.errorClasses[classifyError(err)]++
			continue
		}
		result.successful++
		result.latencies = append(result.latencies, latency)
	}
	return result
}

func sendRequest(
	ctx context.Context,
	client *http.Client,
	cfg Config,
	connectionIndex int,
	streamIndex int,
	sequence uint64,
) (int, error) {
	requestCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		cfg.Target,
		bytes.NewReader(createSessionPayload),
	)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", constants.ContentTypeJSON)
	request.Header.Set(
		requestlog.RequestIDHeader,
		fmt.Sprintf("load-c%d-s%d-r%d", connectionIndex+1, streamIndex+1, sequence),
	)

	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("send request: %w", err)
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		return response.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if response.ProtoMajor != 2 {
		return response.StatusCode, fmt.Errorf("unexpected protocol: %s", response.Proto)
	}
	if response.StatusCode != http.StatusCreated {
		return response.StatusCode, fmt.Errorf("unexpected status: %d", response.StatusCode)
	}
	return response.StatusCode, nil
}

func aggregate(cfg Config, duration time.Duration, results <-chan workerResult) Result {
	result := Result{
		Target:               cfg.Target,
		Protocol:             "h2c",
		DurationSeconds:      duration.Seconds(),
		Connections:          cfg.Connections,
		StreamsPerConnection: cfg.StreamsPerConnection,
		ConcurrentStreams:    cfg.Connections * cfg.StreamsPerConnection,
		StatusCodes:          make(map[string]uint64),
		Errors:               make(map[string]uint64),
	}
	var latencies []time.Duration
	for worker := range results {
		result.TotalRequests += worker.total
		result.SuccessfulRequests += worker.successful
		latencies = append(latencies, worker.latencies...)
		mergeCounts(result.StatusCodes, worker.statusCodes)
		mergeCounts(result.Errors, worker.errorClasses)
	}
	result.FailedRequests = result.TotalRequests - result.SuccessfulRequests
	if duration > 0 {
		result.SuccessfulTPS = float64(result.SuccessfulRequests) / duration.Seconds()
	}
	sort.Slice(latencies, func(first, second int) bool { return latencies[first] < latencies[second] })
	result.LatencyP50Millis = durationMillis(percentile(latencies, 50))
	result.LatencyP95Millis = durationMillis(percentile(latencies, 95))
	result.LatencyP99Millis = durationMillis(percentile(latencies, 99))
	return result
}

func percentile(sorted []time.Duration, percent int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	index := (len(sorted)*percent + 99) / 100
	return sorted[index-1]
}

func durationMillis(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / 1000
}

func mergeCounts(destination, source map[string]uint64) {
	for key, count := range source {
		destination[key] += count
	}
}

func classifyError(err error) string {
	message := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case strings.HasPrefix(message, "unexpected status"):
		return "http_status"
	case strings.HasPrefix(message, "unexpected protocol"):
		return "protocol"
	default:
		return "transport"
	}
}

var createSessionPayload = []byte(`{"supi":"imsi-452040000000001","gpsi":"msisdn-84900000001","pduSessionId":1,"dnn":"v-internet","sNssai":{"sst":1,"sd":"000001"},"servingNfId":"load-test-amf","anType":"3GPP_ACCESS"}`)
