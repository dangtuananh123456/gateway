// Package loadtest provides the h2c load generator used for performance acceptance.
package loadtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// Config defines one fixed-request-count load-test workload and its acceptance target.
type Config struct {
	Target                    string
	RequestCount              uint64
	MinimumSuccessfulRequests uint64
	Duration                  time.Duration
	Connections               int
	StreamsPerConnection      int
	RequestTimeout            time.Duration
	WarmupConnections         bool
}

// Result is the machine-readable summary of one measurement window.
type Result struct {
	Target                   string            `json:"target"`
	Protocol                 string            `json:"protocol"`
	TargetRequests           uint64            `json:"targetRequests"`
	TargetSuccessfulRequests uint64            `json:"targetSuccessfulRequests"`
	TargetDurationSeconds    float64           `json:"targetDurationSeconds"`
	DispatchDurationSeconds  float64           `json:"dispatchDurationSeconds"`
	TargetMet                bool              `json:"targetMet"`
	DurationSeconds          float64           `json:"durationSeconds"`
	Connections              int               `json:"connections"`
	StreamsPerConnection     int               `json:"streamsPerConnection"`
	ConcurrentStreams        int               `json:"concurrentStreams"`
	TotalRequests            uint64            `json:"totalRequests"`
	SentRequests             uint64            `json:"sentRequests"`
	SuccessfulRequests       uint64            `json:"successfulRequests"`
	FailedRequests           uint64            `json:"failedRequests"`
	SuccessfulTPS            float64           `json:"successfulTps"`
	LatencyP50Millis         float64           `json:"latencyP50Millis"`
	LatencyP95Millis         float64           `json:"latencyP95Millis"`
	LatencyP99Millis         float64           `json:"latencyP99Millis"`
	StatusCodes              map[string]uint64 `json:"statusCodes"`
	Errors                   map[string]uint64 `json:"errors,omitempty"`
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

// Run schedules exactly RequestCount attempts inside Duration unless the caller
// cancels ctx. A fixed worker pool bounds active h2c streams while due jobs wait
// in memory for a free stream.
func (runner *Runner) Run(ctx context.Context, cfg Config) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("run load test: context must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}

	clients, closeTransports, err := runner.createClients(cfg.Connections)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		for _, closeTransport := range closeTransports {
			closeTransport()
		}
	}()
	if cfg.WarmupConnections {
		if err := warmConnections(ctx, cfg, clients); err != nil {
			return Result{}, err
		}
	}

	requestCount := int(cfg.RequestCount)
	workerCount := min(requestCount, cfg.Connections*cfg.StreamsPerConnection)
	jobs := make(chan requestJob, requestCount)
	results := make(chan requestResult, requestCount)

	var workers sync.WaitGroup
	var workersReady sync.WaitGroup
	startWorkers := make(chan struct{})
	workers.Add(workerCount)
	workersReady.Add(workerCount)
	var measurementCtx context.Context
	var startedAt time.Time
	var measurementDeadline time.Time
	for workerIndex := range workerCount {
		// Spread workers across transports first so even workloads smaller than
		// the total stream capacity still reuse all configured h2c connections.
		connectionIndex := workerIndex % cfg.Connections
		go func() {
			defer workers.Done()
			workersReady.Done()
			<-startWorkers
			for job := range jobs {
				results <- sendRequest(
					measurementCtx,
					clients[connectionIndex],
					cfg,
					startedAt,
					measurementDeadline,
					job.scheduledAt,
				)
			}
		}()
	}
	workersReady.Wait()
	startedAt = time.Now()
	measurementDeadline = startedAt.Add(cfg.Duration)
	var stopMeasurement context.CancelFunc
	measurementCtx, stopMeasurement = context.WithDeadline(ctx, measurementDeadline)
	defer stopMeasurement()
	close(startWorkers)

	scheduled := scheduleRequests(measurementCtx, cfg, startedAt, jobs)
	close(jobs)
	workers.Wait()
	close(results)

	measurementDuration := time.Since(startedAt)
	result := aggregate(cfg, workerCount, measurementDuration, results)
	// The scheduler can stop on caller cancellation or at the measurement
	// deadline. Keep the count explicit so a partial run cannot pass.
	if result.TotalRequests != scheduled {
		return result, errors.New("run load test: internal attempt count mismatch")
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return result, nil
}

func warmConnections(ctx context.Context, cfg Config, clients []*http.Client) error {
	warmupCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()
	errorsFound := make(chan error, len(clients))
	var warmups sync.WaitGroup
	warmups.Add(len(clients))
	for _, client := range clients {
		go func() {
			defer warmups.Done()
			// A real create-session request warms both h2c legs: load generator to
			// Gateway and Gateway to PDU. These requests complete before the clock
			// starts and are intentionally excluded from Result.
			request, requestErr := http.NewRequestWithContext(
				warmupCtx,
				http.MethodPost,
				cfg.Target,
				bytes.NewReader(createSessionPayload),
			)
			if requestErr != nil {
				errorsFound <- requestErr
				return
			}
			request.Header.Set("Content-Type", constants.ContentTypeJSON)
			request.Header.Set("X-Loadtest-Warmup", "true")
			response, requestErr := client.Do(request)
			if requestErr != nil {
				errorsFound <- requestErr
				return
			}
			_, copyErr := io.Copy(io.Discard, response.Body)
			response.Body.Close()
			switch {
			case copyErr != nil:
				errorsFound <- copyErr
			case response.ProtoMajor != 2:
				errorsFound <- fmt.Errorf("unexpected protocol %s", response.Proto)
			case response.StatusCode != http.StatusCreated:
				errorsFound <- fmt.Errorf("unexpected status %d", response.StatusCode)
			}
		}()
	}
	warmups.Wait()
	close(errorsFound)
	var warmupErrors []error
	for warmupErr := range errorsFound {
		warmupErrors = append(warmupErrors, warmupErr)
	}
	if err := errors.Join(warmupErrors...); err != nil {
		return fmt.Errorf("warm h2c connections: %w", err)
	}
	return nil
}

func (runner *Runner) createClients(count int) ([]*http.Client, []func(), error) {
	clients := make([]*http.Client, count)
	closeTransports := make([]func(), count)
	for index := range count {
		transport, closeTransport := runner.newTransport()
		if transport == nil || closeTransport == nil {
			for previous := range index {
				closeTransports[previous]()
			}
			return nil, nil, errors.New("run load test: transport factory returned a nil dependency")
		}
		clients[index] = &http.Client{Transport: transport}
		closeTransports[index] = closeTransport
	}
	return clients, closeTransports, nil
}

type requestJob struct {
	scheduledAt time.Time
}

// scheduleRequests uses absolute due times so normal timer drift does not build
// up over 15,000 attempts. The first request is due at t=0. Requests are all
// scheduled during the first 85% of the measurement window so the final jobs
// still have time to reach the socket and produce an in-window response.
func scheduleRequests(
	ctx context.Context,
	cfg Config,
	startedAt time.Time,
	jobs chan<- requestJob,
) uint64 {
	dispatchWindow := cfg.Duration * 85 / 100
	stepCount := min(cfg.RequestCount, uint64(1000))
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()

	var scheduled uint64
	for step := uint64(0); step < stepCount; step++ {
		dueOffset := proportionalDuration(step, stepCount, dispatchWindow)
		wait := time.Until(startedAt.Add(dueOffset))
		if wait > 0 {
			timer.Reset(wait)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return scheduled
			case <-timer.C:
			}
		} else if ctx.Err() != nil {
			return scheduled
		}

		// Batch at most 1,000 pacing ticks instead of allocating/resetting one
		// timer per request. The absolute target count prevents timer drift.
		desired := proportionalCount(step+1, stepCount, cfg.RequestCount)
		for scheduled < desired {
			scheduled++
			// jobs is sized for every configured attempt, so this send cannot make
			// the timing loop wait for a worker.
			jobs <- requestJob{scheduledAt: startedAt.Add(dueOffset)}
		}
	}
	return scheduled
}

func proportionalCount(step, steps, total uint64) uint64 {
	whole := total / steps
	remainder := total % steps
	return step*whole + (step*remainder+steps-1)/steps
}

func proportionalDuration(index, count uint64, duration time.Duration) time.Duration {
	// Splitting duration into quotient and remainder avoids overflowing an int64
	// when calculating index*duration for otherwise valid configurations.
	countDuration := time.Duration(count)
	whole := duration / countDuration
	remainder := duration % countDuration
	high, low := bits.Mul64(index, uint64(remainder))
	fraction, _ := bits.Div64(high, low, count)
	return time.Duration(index)*whole + time.Duration(fraction)
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
	if cfg.RequestCount == 0 {
		return errors.New("validate load-test config: request count must be greater than zero")
	}
	if cfg.RequestCount > uint64(math.MaxInt) {
		return errors.New("validate load-test config: request count is too large for this platform")
	}
	if cfg.MinimumSuccessfulRequests == 0 || cfg.MinimumSuccessfulRequests > cfg.RequestCount {
		return errors.New("validate load-test config: minimum successful requests must be between one and request count")
	}
	if cfg.Connections <= 0 {
		return errors.New("validate load-test config: connections must be greater than zero")
	}
	if cfg.StreamsPerConnection <= 0 {
		return errors.New("validate load-test config: streams per connection must be greater than zero")
	}
	if cfg.Connections > math.MaxInt/cfg.StreamsPerConnection {
		return errors.New("validate load-test config: concurrent stream capacity is too large for this platform")
	}
	if cfg.RequestTimeout <= 0 {
		return errors.New("validate load-test config: request timeout must be greater than zero")
	}
	return nil
}

type requestResult struct {
	status  int
	latency time.Duration
	sent    bool
	sentAt  time.Duration
	err     error
}

func sendRequest(
	ctx context.Context,
	client *http.Client,
	cfg Config,
	measurementStart time.Time,
	measurementDeadline time.Time,
	scheduledAt time.Time,
) (result requestResult) {
	startedAt := time.Now()
	requestCtx := ctx
	cancel := func() {}
	if timeoutDeadline := startedAt.Add(cfg.RequestTimeout); timeoutDeadline.Before(measurementDeadline) {
		requestCtx, cancel = context.WithDeadline(ctx, timeoutDeadline)
	}
	defer cancel()

	var wroteAtNanos atomic.Int64
	trace := &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				return
			}
			wroteAt := time.Now()
			if wroteAt.After(measurementDeadline) {
				return
			}
			elapsed := wroteAt.Sub(measurementStart)
			if elapsed < 0 {
				elapsed = 0
			}
			// The extra nanosecond reserves zero as the "not written" sentinel.
			wroteAtNanos.CompareAndSwap(0, elapsed.Nanoseconds()+1)
		},
	}
	requestCtx = httptrace.WithClientTrace(requestCtx, trace)
	defer func() {
		written := wroteAtNanos.Load()
		if written > 0 {
			result.sent = true
			result.sentAt = time.Duration(written - 1)
		}
	}()

	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		cfg.Target,
		bytes.NewReader(createSessionPayload),
	)
	if err != nil {
		result.err = fmt.Errorf("create request: %w", err)
		return
	}
	request.Header.Set("Content-Type", constants.ContentTypeJSON)

	response, err := client.Do(request)
	if err != nil {
		result.err = fmt.Errorf("send request: %w", err)
		return
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		result.err = fmt.Errorf("read response: %w", err)
		return
	}
	completedAt := time.Now()
	if completedAt.After(measurementDeadline) {
		result.err = fmt.Errorf("complete request outside measurement window: %w", context.DeadlineExceeded)
		return
	}
	result.status = response.StatusCode
	if response.ProtoMajor != 2 {
		result.err = fmt.Errorf("unexpected protocol: %s", response.Proto)
		return
	}
	if response.StatusCode != http.StatusCreated {
		result.err = fmt.Errorf("unexpected status: %d", response.StatusCode)
		return
	}
	// Include time spent waiting for a worker/HTTP2 stream. Measuring only from
	// client.Do would hide queueing latency when the offered load exceeds the
	// available concurrency (coordinated omission).
	result.latency = completedAt.Sub(scheduledAt)
	return
}

func aggregate(
	cfg Config,
	workerCount int,
	duration time.Duration,
	results <-chan requestResult,
) Result {
	result := Result{
		Target:                   cfg.Target,
		Protocol:                 "h2c",
		TargetRequests:           cfg.RequestCount,
		TargetSuccessfulRequests: cfg.MinimumSuccessfulRequests,
		TargetDurationSeconds:    cfg.Duration.Seconds(),
		DurationSeconds:          duration.Seconds(),
		Connections:              cfg.Connections,
		StreamsPerConnection:     cfg.StreamsPerConnection,
		ConcurrentStreams:        workerCount,
		StatusCodes:              make(map[string]uint64),
		Errors:                   make(map[string]uint64),
	}
	var latencies []time.Duration
	var lastWrite time.Duration
	for request := range results {
		result.TotalRequests++
		if request.sent {
			result.SentRequests++
			if request.sentAt > lastWrite {
				lastWrite = request.sentAt
			}
		}
		if request.status != 0 {
			result.StatusCodes[strconv.Itoa(request.status)]++
		}
		if request.err != nil {
			result.Errors[classifyError(request.err)]++
			continue
		}
		result.SuccessfulRequests++
		latencies = append(latencies, request.latency)
	}
	result.DispatchDurationSeconds = lastWrite.Seconds()
	result.FailedRequests = result.TotalRequests - result.SuccessfulRequests
	result.TargetMet = result.TotalRequests == cfg.RequestCount &&
		result.SentRequests == cfg.RequestCount &&
		result.SuccessfulRequests >= cfg.MinimumSuccessfulRequests
	result.SuccessfulTPS = float64(result.SuccessfulRequests) / cfg.Duration.Seconds()
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
