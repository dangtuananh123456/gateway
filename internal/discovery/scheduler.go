package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/dangtuananh123456/gateway/internal/registry"
)

// CandidateRegistry is the subset of registry operations required by discovery.
type CandidateRegistry interface {
	Upsert(netip.AddrPort, time.Time) (bool, error)
	Remove(netip.AddrPort) bool
	Candidates() []registry.Candidate
}

// SchedulerConfig controls DNS polling and scale-down behavior.
type SchedulerConfig struct {
	PollInterval time.Duration
	RoundTimeout time.Duration
	StaleTTL     time.Duration
	OnError      func(error)
}

// Scheduler periodically reconciles DNS addresses with the candidate registry.
type Scheduler struct {
	resolver Resolver
	registry CandidateRegistry
	config   SchedulerConfig
	now      func() time.Time
}

// NewScheduler creates a sequential scheduler that owns no background goroutine.
func NewScheduler(
	resolver Resolver,
	candidateRegistry CandidateRegistry,
	config SchedulerConfig,
) (*Scheduler, error) {
	if resolver == nil {
		return nil, errors.New("create discovery scheduler: resolver must not be nil")
	}
	if candidateRegistry == nil {
		return nil, errors.New("create discovery scheduler: candidate registry must not be nil")
	}
	if config.PollInterval <= 0 {
		return nil, errors.New("create discovery scheduler: poll interval must be greater than zero")
	}
	if config.RoundTimeout <= 0 {
		return nil, errors.New("create discovery scheduler: round timeout must be greater than zero")
	}
	if config.StaleTTL < config.PollInterval {
		return nil, errors.New("create discovery scheduler: stale TTL must be at least the poll interval")
	}

	return &Scheduler{
		resolver: resolver,
		registry: candidateRegistry,
		config:   config,
		now:      time.Now,
	}, nil
}

// Run performs an immediate sync and then polls until ctx is canceled. Resolve
// errors are reported and retried on the next tick without clearing candidates.
func (scheduler *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(scheduler.config.PollInterval)
	defer ticker.Stop()

	for {
		if err := scheduler.Sync(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if scheduler.config.OnError != nil {
				scheduler.config.OnError(err)
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Sync performs one bounded and deterministic DNS reconciliation round.
func (scheduler *Scheduler) Sync(ctx context.Context) error {
	roundCtx, cancel := context.WithTimeout(ctx, scheduler.config.RoundTimeout)
	defer cancel()

	addresses, err := scheduler.resolver.Resolve(roundCtx)
	if err != nil {
		return fmt.Errorf("discovery sync: %w", err)
	}
	if err := roundCtx.Err(); err != nil {
		return fmt.Errorf("discovery sync: %w", err)
	}
	resolved, err := validatedAddressSet(addresses)
	if err != nil {
		return err
	}

	observedAt := scheduler.now().UTC()
	if observedAt.IsZero() {
		return errors.New("discovery sync: clock returned a zero timestamp")
	}
	for address := range resolved {
		if _, err := scheduler.registry.Upsert(address, observedAt); err != nil {
			return fmt.Errorf("discovery sync upsert %s: %w", address, err)
		}
	}
	for _, candidate := range scheduler.registry.Candidates() {
		if _, present := resolved[candidate.Address]; present {
			continue
		}
		if observedAt.Sub(candidate.LastSeenAt) >= scheduler.config.StaleTTL {
			scheduler.registry.Remove(candidate.Address)
		}
	}
	return nil
}

func validatedAddressSet(addresses []netip.AddrPort) (map[netip.AddrPort]struct{}, error) {
	if len(addresses) == 0 {
		return nil, fmt.Errorf("discovery sync: %w", ErrNoAddresses)
	}
	result := make(map[netip.AddrPort]struct{}, len(addresses))
	for index, address := range addresses {
		if !address.IsValid() || address.Port() == 0 {
			return nil, fmt.Errorf("discovery sync: %w at result index %d", ErrInvalidAddress, index)
		}
		result[address] = struct{}{}
	}
	return result, nil
}
