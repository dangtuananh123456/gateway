package discovery

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/registry"
	"go.uber.org/goleak"
)

func TestMain(testMain *testing.M) {
	goleak.VerifyTestMain(testMain)
}

func TestSchedulerSyncDetectsScaleUp(t *testing.T) {
	address1 := netip.MustParseAddrPort("10.0.0.1:8081")
	address2 := netip.MustParseAddrPort("10.0.0.2:8081")
	address3 := netip.MustParseAddrPort("10.0.0.3:8081")
	responses := [][]netip.AddrPort{{address2, address1}, {address3, address1, address2}}
	var call int
	resolver := resolverFunc(func(context.Context) ([]netip.AddrPort, error) {
		response := responses[call]
		call++
		return response, nil
	})
	candidates := registry.New()
	scheduler := newTestScheduler(t, resolver, candidates)
	currentTime := discoveryTestTime()
	scheduler.now = func() time.Time { return currentTime }

	if err := scheduler.Sync(context.Background()); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	assertCandidateAddresses(t, candidates, []netip.AddrPort{address1, address2})
	for _, candidate := range candidates.Candidates() {
		if candidate.Healthy {
			t.Errorf("new candidate %s is healthy before collection", candidate.Address)
		}
	}

	currentTime = currentTime.Add(time.Second)
	if err := scheduler.Sync(context.Background()); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	assertCandidateAddresses(t, candidates, []netip.AddrPort{address1, address2, address3})
}

func TestSchedulerScaleDownUsesStaleTTL(t *testing.T) {
	address1 := netip.MustParseAddrPort("10.0.0.1:8081")
	address2 := netip.MustParseAddrPort("10.0.0.2:8081")
	addresses := []netip.AddrPort{address1, address2}
	resolver := resolverFunc(func(context.Context) ([]netip.AddrPort, error) {
		return addresses, nil
	})
	candidates := registry.New()
	scheduler := newTestScheduler(t, resolver, candidates)
	currentTime := discoveryTestTime()
	scheduler.now = func() time.Time { return currentTime }

	if err := scheduler.Sync(context.Background()); err != nil {
		t.Fatalf("initial Sync() error = %v", err)
	}
	addresses = []netip.AddrPort{address1}
	currentTime = currentTime.Add(5 * time.Second)
	if err := scheduler.Sync(context.Background()); err != nil {
		t.Fatalf("pre-TTL Sync() error = %v", err)
	}
	assertCandidateAddresses(t, candidates, []netip.AddrPort{address1, address2})

	currentTime = currentTime.Add(5 * time.Second)
	if err := scheduler.Sync(context.Background()); err != nil {
		t.Fatalf("post-TTL Sync() error = %v", err)
	}
	assertCandidateAddresses(t, candidates, []netip.AddrPort{address1})
}

func TestSchedulerResolveFailurePreservesCandidates(t *testing.T) {
	address := netip.MustParseAddrPort("10.0.0.1:8081")
	wantErr := errors.New("temporary DNS failure")
	resolveErr := error(nil)
	addresses := []netip.AddrPort{address}
	resolver := resolverFunc(func(context.Context) ([]netip.AddrPort, error) {
		return addresses, resolveErr
	})
	candidates := registry.New()
	scheduler := newTestScheduler(t, resolver, candidates)
	scheduler.now = func() time.Time { return discoveryTestTime() }
	if err := scheduler.Sync(context.Background()); err != nil {
		t.Fatalf("seed Sync() error = %v", err)
	}

	resolveErr = wantErr
	addresses = nil
	if err := scheduler.Sync(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("failed Sync() error = %v, want lookup error", err)
	}
	assertCandidateAddresses(t, candidates, []netip.AddrPort{address})

	resolveErr = nil
	if err := scheduler.Sync(context.Background()); !errors.Is(err, ErrNoAddresses) {
		t.Fatalf("empty Sync() error = %v, want ErrNoAddresses", err)
	}
	assertCandidateAddresses(t, candidates, []netip.AddrPort{address})
}

func TestSchedulerRejectsInvalidResolvedAddressWithoutMutation(t *testing.T) {
	candidates := registry.New()
	scheduler := newTestScheduler(t, resolverFunc(func(context.Context) ([]netip.AddrPort, error) {
		return []netip.AddrPort{{}}, nil
	}), candidates)
	scheduler.now = func() time.Time { return discoveryTestTime() }

	if err := scheduler.Sync(context.Background()); !errors.Is(err, ErrInvalidAddress) {
		t.Fatalf("Sync() error = %v, want ErrInvalidAddress", err)
	}
	if got := len(candidates.Candidates()); got != 0 {
		t.Errorf("candidate count = %d, want 0", got)
	}
}

func TestSchedulerAppliesRoundTimeout(t *testing.T) {
	resolver := resolverFunc(func(ctx context.Context) ([]netip.AddrPort, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	candidates := registry.New()
	scheduler, err := NewScheduler(resolver, candidates, SchedulerConfig{
		PollInterval: time.Second,
		RoundTimeout: 20 * time.Millisecond,
		StaleTTL:     time.Second,
	})
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}

	if err := scheduler.Sync(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Sync() error = %v, want context deadline exceeded", err)
	}
	if got := len(candidates.Candidates()); got != 0 {
		t.Errorf("candidate count = %d, want 0", got)
	}
}

func TestSchedulerRunRetriesWithoutOverlappingRounds(t *testing.T) {
	address := netip.MustParseAddrPort("10.0.0.1:8081")
	wantErr := errors.New("first lookup failed")
	var calls atomic.Int64
	var active atomic.Int64
	var maximum atomic.Int64
	resolver := resolverFunc(func(context.Context) ([]netip.AddrPort, error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		defer active.Add(-1)
		if calls.Add(1) == 1 {
			return nil, wantErr
		}
		return []netip.AddrPort{address}, nil
	})
	candidates := registry.New()
	reportedErrors := make(chan error, 1)
	scheduler, err := NewScheduler(resolver, candidates, SchedulerConfig{
		PollInterval: 5 * time.Millisecond,
		RoundTimeout: 100 * time.Millisecond,
		StaleTTL:     100 * time.Millisecond,
		OnError: func(err error) {
			select {
			case reportedErrors <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, found := candidates.Get(address); found {
			break
		}
		select {
		case <-deadline.C:
			cancel()
			t.Fatal("scheduler did not recover after temporary resolver error")
		case <-ticker.C:
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() error = %v, want nil on cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
	select {
	case err := <-reportedErrors:
		if !errors.Is(err, wantErr) {
			t.Errorf("reported error = %v, want resolver error", err)
		}
	default:
		t.Error("temporary resolver error was not reported")
	}
	if got := maximum.Load(); got != 1 {
		t.Errorf("maximum concurrent resolver calls = %d, want 1", got)
	}
}

func TestSchedulerReturnsRegistryAndClockErrors(t *testing.T) {
	wantErr := errors.New("registry unavailable")
	stub := &candidateRegistryStub{upsertErr: wantErr}
	scheduler := newTestScheduler(t, resolverFunc(func(context.Context) ([]netip.AddrPort, error) {
		return []netip.AddrPort{netip.MustParseAddrPort("10.0.0.1:8081")}, nil
	}), stub)
	scheduler.now = func() time.Time { return discoveryTestTime() }
	if err := scheduler.Sync(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("Sync() error = %v, want registry error", err)
	}

	scheduler.now = func() time.Time { return time.Time{} }
	if err := scheduler.Sync(context.Background()); err == nil {
		t.Error("Sync() error = nil, want zero clock error")
	}
}

func TestNewSchedulerValidation(t *testing.T) {
	validResolver := resolverFunc(func(context.Context) ([]netip.AddrPort, error) { return nil, nil })
	validRegistry := registry.New()
	validConfig := SchedulerConfig{PollInterval: time.Second, RoundTimeout: time.Second, StaleTTL: time.Second}
	tests := []struct {
		name     string
		resolver Resolver
		registry CandidateRegistry
		config   SchedulerConfig
	}{
		{name: "nil resolver", registry: validRegistry, config: validConfig},
		{name: "nil registry", resolver: validResolver, config: validConfig},
		{name: "zero poll interval", resolver: validResolver, registry: validRegistry, config: SchedulerConfig{RoundTimeout: time.Second, StaleTTL: time.Second}},
		{name: "zero round timeout", resolver: validResolver, registry: validRegistry, config: SchedulerConfig{PollInterval: time.Second, StaleTTL: time.Second}},
		{name: "short stale TTL", resolver: validResolver, registry: validRegistry, config: SchedulerConfig{PollInterval: time.Second, RoundTimeout: time.Second, StaleTTL: time.Millisecond}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewScheduler(test.resolver, test.registry, test.config); err == nil {
				t.Fatal("NewScheduler() error = nil, want validation error")
			}
		})
	}
}

type resolverFunc func(context.Context) ([]netip.AddrPort, error)

func (resolve resolverFunc) Resolve(ctx context.Context) ([]netip.AddrPort, error) {
	return resolve(ctx)
}

type candidateRegistryStub struct {
	upsertErr error
}

func (stub *candidateRegistryStub) Upsert(netip.AddrPort, time.Time) (bool, error) {
	return false, stub.upsertErr
}

func (*candidateRegistryStub) Remove(netip.AddrPort) bool {
	return false
}

func (*candidateRegistryStub) Candidates() []registry.Candidate {
	return nil
}

func newTestScheduler(
	t *testing.T,
	resolver Resolver,
	candidateRegistry CandidateRegistry,
) *Scheduler {
	t.Helper()
	scheduler, err := NewScheduler(resolver, candidateRegistry, SchedulerConfig{
		PollInterval: time.Second,
		RoundTimeout: time.Second,
		StaleTTL:     10 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewScheduler() error = %v", err)
	}
	return scheduler
}

func assertCandidateAddresses(
	t *testing.T,
	candidates *registry.Registry,
	want []netip.AddrPort,
) {
	t.Helper()
	gotCandidates := candidates.Candidates()
	if len(gotCandidates) != len(want) {
		t.Fatalf("candidate count = %d, want %d; candidates = %v", len(gotCandidates), len(want), gotCandidates)
	}
	for index, wantAddress := range want {
		if gotCandidates[index].Address != wantAddress {
			t.Errorf("candidate[%d] = %s, want %s", index, gotCandidates[index].Address, wantAddress)
		}
	}
}

func discoveryTestTime() time.Time {
	return time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
}
