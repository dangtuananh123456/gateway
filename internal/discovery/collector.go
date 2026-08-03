package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// CollectorRegistry is the subset of registry operations used by probes.
type CollectorRegistry interface {
	Candidates() []registry.Candidate
	MarkHealthSuccess(netip.AddrPort, string, time.Time) error
	MarkMetricsSuccess(netip.AddrPort, registry.Metadata, time.Time) error
	MarkUnhealthy(netip.AddrPort, time.Time) error
}

// CollectorConfig controls probe cadence, timeout, and total network concurrency.
type CollectorConfig struct {
	HealthInterval  time.Duration
	HealthTimeout   time.Duration
	MetricsInterval time.Duration
	MetricsTimeout  time.Duration
	MaxConcurrency  int
	OnError         func(error)
}

// Collector periodically refreshes health and load metadata with one shared client.
type Collector struct {
	registry  CollectorRegistry
	client    *http.Client
	config    CollectorConfig
	semaphore chan struct{}
	now       func() time.Time
}

// NewCollector creates a collector. The caller owns the shared transport lifecycle.
func NewCollector(
	candidateRegistry CollectorRegistry,
	transport http.RoundTripper,
	config CollectorConfig,
) (*Collector, error) {
	if candidateRegistry == nil {
		return nil, errors.New("create collector: registry must not be nil")
	}
	if transport == nil {
		return nil, errors.New("create collector: transport must not be nil")
	}
	if config.HealthInterval <= 0 || config.HealthTimeout <= 0 {
		return nil, errors.New("create collector: health interval and timeout must be greater than zero")
	}
	if config.MetricsInterval <= 0 || config.MetricsTimeout <= 0 {
		return nil, errors.New("create collector: metrics interval and timeout must be greater than zero")
	}
	if config.MaxConcurrency <= 0 {
		return nil, errors.New("create collector: max concurrency must be greater than zero")
	}

	return &Collector{
		registry:  candidateRegistry,
		client:    &http.Client{Transport: transport},
		config:    config,
		semaphore: make(chan struct{}, config.MaxConcurrency),
		now:       time.Now,
	}, nil
}

// Run starts one fixed health loop and one fixed metrics loop until cancellation.
func (collector *Collector) Run(ctx context.Context) error {
	var loops sync.WaitGroup
	loops.Add(2)
	go func() {
		defer loops.Done()
		collector.runLoop(ctx, collector.config.HealthInterval, collector.CollectHealth)
	}()
	go func() {
		defer loops.Done()
		collector.runLoop(ctx, collector.config.MetricsInterval, collector.CollectMetrics)
	}()
	loops.Wait()
	return nil
}

// CollectHealth performs one bounded health collection round.
func (collector *Collector) CollectHealth(ctx context.Context) error {
	return collector.collect(ctx, collector.config.HealthTimeout, collector.collectHealth)
}

// CollectMetrics performs one bounded metrics collection round.
func (collector *Collector) CollectMetrics(ctx context.Context) error {
	return collector.collect(ctx, collector.config.MetricsTimeout, collector.collectMetrics)
}

func (collector *Collector) runLoop(
	ctx context.Context,
	interval time.Duration,
	collect func(context.Context) error,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := collect(ctx); err != nil && ctx.Err() == nil && collector.config.OnError != nil {
			collector.config.OnError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (collector *Collector) collect(
	ctx context.Context,
	timeout time.Duration,
	probe func(context.Context, registry.Candidate) error,
) error {
	candidates := collector.registry.Candidates()
	if len(candidates) == 0 {
		return nil
	}

	workerCount := min(collector.config.MaxConcurrency, len(candidates))
	jobs := make(chan registry.Candidate)
	errorsFound := make(chan error, len(candidates))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				requestCtx, cancel := context.WithTimeout(ctx, timeout)
				err := collector.withPermit(requestCtx, func() error {
					return probe(requestCtx, candidate)
				})
				cancel()
				if err != nil {
					errorsFound <- fmt.Errorf("collect %s: %w", candidate.Address, err)
				}
			}
		}()
	}

	for _, candidate := range candidates {
		jobs <- candidate
	}
	close(jobs)
	workers.Wait()
	close(errorsFound)

	var roundErrors []error
	for err := range errorsFound {
		roundErrors = append(roundErrors, err)
	}
	return errors.Join(roundErrors...)
}

func (collector *Collector) withPermit(ctx context.Context, probe func() error) error {
	select {
	case collector.semaphore <- struct{}{}:
		defer func() { <-collector.semaphore }()
		return probe()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (collector *Collector) collectHealth(ctx context.Context, candidate registry.Candidate) error {
	var response model.HealthResponse
	if err := collector.getJSON(ctx, candidate.Address, constants.HealthPath, &response); err != nil {
		return collector.fail(ctx, candidate.Address, err)
	}
	if response.Status != constants.ServiceUp {
		return collector.fail(ctx, candidate.Address, fmt.Errorf("health status is %q", response.Status))
	}
	observedAt, err := collector.observedAt()
	if err != nil {
		return err
	}
	if err := collector.registry.MarkHealthSuccess(candidate.Address, response.InstanceID, observedAt); err != nil {
		if errors.Is(err, registry.ErrCandidateNotFound) {
			return nil
		}
		return collector.fail(ctx, candidate.Address, err)
	}
	return nil
}

func (collector *Collector) collectMetrics(ctx context.Context, candidate registry.Candidate) error {
	var response model.MetricsResponse
	if err := collector.getJSON(ctx, candidate.Address, constants.MetricsPath, &response); err != nil {
		return collector.fail(ctx, candidate.Address, err)
	}
	observedAt, err := collector.observedAt()
	if err != nil {
		return err
	}
	err = collector.registry.MarkMetricsSuccess(candidate.Address, registry.Metadata{
		InstanceID:     response.InstanceID,
		Weight:         response.Weight,
		ActiveRequests: response.ActiveRequests,
	}, observedAt)
	if errors.Is(err, registry.ErrCandidateNotFound) {
		return nil
	}
	if err != nil {
		return collector.fail(ctx, candidate.Address, err)
	}
	return nil
}

func (collector *Collector) getJSON(
	ctx context.Context,
	address netip.AddrPort,
	path string,
	destination any,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address.String()+path, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	response, err := collector.client.Do(request)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, constants.CollectorMaxResponseBodyBytes))
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, constants.CollectorMaxResponseBodyBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > constants.CollectorMaxResponseBodyBytes {
		return errors.New("response body exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decode response: body must contain exactly one JSON object")
	}
	return nil
}

func (collector *Collector) fail(ctx context.Context, address netip.AddrPort, cause error) error {
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return cause
	}
	observedAt, err := collector.observedAt()
	if err != nil {
		return errors.Join(cause, err)
	}
	if err := ignoreRemovedCandidate(collector.registry.MarkUnhealthy(address, observedAt)); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (collector *Collector) observedAt() (time.Time, error) {
	observedAt := collector.now().UTC()
	if observedAt.IsZero() {
		return time.Time{}, errors.New("collector clock returned a zero timestamp")
	}
	return observedAt, nil
}

func ignoreRemovedCandidate(err error) error {
	if errors.Is(err, registry.ErrCandidateNotFound) {
		return nil
	}
	return err
}
