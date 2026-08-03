package routing

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/registry"
)

func TestRoundRobinRepeatsSnapshotOrder(t *testing.T) {
	snapshot := routingTestSnapshot(t, "pdu-1", "pdu-2", "pdu-3")
	selector := NewRoundRobin()
	want := []string{"pdu-1", "pdu-2", "pdu-3", "pdu-1", "pdu-2", "pdu-3"}
	for index, wantID := range want {
		instance, err := selector.Select(snapshot)
		if err != nil {
			t.Fatalf("Select() request %d error = %v", index+1, err)
		}
		if instance.InstanceID != wantID {
			t.Errorf("Select() request %d = %s, want %s", index+1, instance.InstanceID, wantID)
		}
	}
}

func TestRoundRobinReturnsNoBackendForEmptySnapshot(t *testing.T) {
	instance, err := NewRoundRobin().Select(registry.Snapshot{})
	if !errors.Is(err, ErrNoBackend) {
		t.Errorf("Select(empty) error = %v, want ErrNoBackend", err)
	}
	if instance != (registry.Instance{}) {
		t.Errorf("Select(empty) instance = %+v, want zero value", instance)
	}
}

func TestRoundRobinUsesCurrentMembership(t *testing.T) {
	candidates := registry.New()
	base := routingTestTime()
	addresses := make(map[string]netip.AddrPort, 3)
	for index, instanceID := range []string{"pdu-1", "pdu-2", "pdu-3"} {
		address := netip.MustParseAddrPort(fmt.Sprintf("10.0.0.%d:8081", index+1))
		addresses[instanceID] = address
		if _, err := candidates.Upsert(address, base); err != nil {
			t.Fatalf("Upsert(%s) error = %v", address, err)
		}
		if err := candidates.MarkHealthy(address, registry.Metadata{
			InstanceID: instanceID, Weight: 1,
		}, base.Add(time.Duration(index+1)*time.Second)); err != nil {
			t.Fatalf("MarkHealthy(%s) error = %v", address, err)
		}
	}

	selector := NewRoundRobin()
	for range 2 {
		if _, err := selector.Select(candidates.HealthySnapshot()); err != nil {
			t.Fatalf("Select() before scale down error = %v", err)
		}
	}
	if err := candidates.MarkUnhealthy(addresses["pdu-2"], base.Add(10*time.Second)); err != nil {
		t.Fatalf("MarkUnhealthy() error = %v", err)
	}
	for request := range 20 {
		instance, err := selector.Select(candidates.HealthySnapshot())
		if err != nil {
			t.Fatalf("Select() after scale down request %d error = %v", request+1, err)
		}
		if instance.InstanceID == "pdu-2" {
			t.Fatal("Select() returned unhealthy pdu-2")
		}
	}
}

func TestRoundRobinHandlesCounterOverflow(t *testing.T) {
	snapshot := routingTestSnapshot(t, "pdu-1", "pdu-2", "pdu-3")
	selector := NewRoundRobin()
	selector.counter.Store(math.MaxUint64 - 1)
	want := []string{"pdu-3", "pdu-1", "pdu-1"}
	for index, wantID := range want {
		instance, err := selector.Select(snapshot)
		if err != nil {
			t.Fatalf("Select() at overflow step %d error = %v", index, err)
		}
		if instance.InstanceID != wantID {
			t.Errorf("Select() at overflow step %d = %s, want %s", index, instance.InstanceID, wantID)
		}
	}
}

func TestRoundRobinConcurrentSelection(t *testing.T) {
	snapshot := routingTestSnapshot(t, "pdu-1", "pdu-2", "pdu-3", "pdu-4")
	selector := NewRoundRobin()
	const goroutineCount = 64
	const selectionsPerGoroutine = 1_000
	var counts [4]atomic.Int64
	indices := map[string]int{"pdu-1": 0, "pdu-2": 1, "pdu-3": 2, "pdu-4": 3}
	errorsFound := make(chan error, goroutineCount)

	var workers sync.WaitGroup
	workers.Add(goroutineCount)
	for range goroutineCount {
		go func() {
			defer workers.Done()
			for range selectionsPerGoroutine {
				instance, err := selector.Select(snapshot)
				if err != nil {
					errorsFound <- err
					return
				}
				index, found := indices[instance.InstanceID]
				if !found {
					errorsFound <- fmt.Errorf("unexpected instance %q", instance.InstanceID)
					return
				}
				counts[index].Add(1)
			}
		}()
	}
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}

	wantPerBackend := int64(goroutineCount * selectionsPerGoroutine / len(counts))
	for index := range counts {
		if got := counts[index].Load(); got != wantPerBackend {
			t.Errorf("backend %d selections = %d, want %d", index+1, got, wantPerBackend)
		}
	}
}

func BenchmarkRoundRobinSelect(benchmark *testing.B) {
	snapshot := routingTestSnapshot(benchmark, "pdu-1", "pdu-2", "pdu-3")
	selector := NewRoundRobin()
	benchmark.ReportAllocs()
	benchmark.ResetTimer()
	for range benchmark.N {
		_, _ = selector.Select(snapshot)
	}
}

type testingHelper interface {
	Helper()
	Fatalf(string, ...any)
}

func routingTestSnapshot(test testingHelper, instanceIDs ...string) registry.Snapshot {
	test.Helper()
	candidates := registry.New()
	base := routingTestTime()
	for index, instanceID := range instanceIDs {
		address := netip.MustParseAddrPort(fmt.Sprintf("10.0.0.%d:8081", index+1))
		if _, err := candidates.Upsert(address, base); err != nil {
			test.Fatalf("Upsert(%s) error = %v", address, err)
		}
		if err := candidates.MarkHealthy(address, registry.Metadata{
			InstanceID: instanceID, Weight: 1,
		}, base.Add(time.Duration(index+1)*time.Second)); err != nil {
			test.Fatalf("MarkHealthy(%s) error = %v", address, err)
		}
	}
	return candidates.HealthySnapshot()
}

func routingTestTime() time.Time {
	return time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
}
