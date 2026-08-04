package routing

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dangtuananh123456/gateway/internal/registry"
)

func TestLoadBasedSelectsLowestCachedLoad(t *testing.T) {
	snapshot := loadTestSnapshot(t, []loadTestInstance{
		{id: "pdu-1", activeRequests: 5},
		{id: "pdu-2", activeRequests: 1},
		{id: "pdu-3", activeRequests: 3},
	})
	instance, err := NewLoadBased().Select(snapshot)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if instance.InstanceID != "pdu-2" {
		t.Errorf("Select() = %s with load %d, want pdu-2 with load 1", instance.InstanceID, instance.ActiveRequests)
	}
}

func TestLoadBasedReturnsNoBackendForEmptySnapshot(t *testing.T) {
	instance, err := NewLoadBased().Select(registry.Snapshot{})
	if !errors.Is(err, ErrNoBackend) {
		t.Errorf("Select(empty) error = %v, want ErrNoBackend", err)
	}
	if instance != (registry.Instance{}) {
		t.Errorf("Select(empty) instance = %+v, want zero value", instance)
	}
}

func TestLoadBasedTieBreaksByInstanceID(t *testing.T) {
	snapshot := loadTestSnapshot(t, []loadTestInstance{
		{id: "pdu-c", activeRequests: 4},
		{id: "pdu-a", activeRequests: 2},
		{id: "pdu-b", activeRequests: 2},
	})
	instance, err := NewLoadBased().Select(snapshot)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if instance.InstanceID != "pdu-a" {
		t.Errorf("Select() tie = %s, want lexical pdu-a", instance.InstanceID)
	}
}

func TestLoadBasedUsesNextMetricsSnapshot(t *testing.T) {
	candidates, addresses := loadTestRegistry(t, []loadTestInstance{
		{id: "pdu-1", activeRequests: 5},
		{id: "pdu-2", activeRequests: 1},
	})
	selector := NewLoadBased()
	first, err := selector.Select(candidates.HealthySnapshot())
	if err != nil {
		t.Fatalf("first Select() error = %v", err)
	}
	if first.InstanceID != "pdu-2" {
		t.Fatalf("first Select() = %s, want pdu-2", first.InstanceID)
	}

	if err := candidates.MarkMetricsSuccess(addresses["pdu-1"], registry.Metadata{
		InstanceID: "pdu-1", Weight: 1, ActiveRequests: 0,
	}, routingTestTime().Add(10*time.Second)); err != nil {
		t.Fatalf("MarkMetricsSuccess() error = %v", err)
	}
	second, err := selector.Select(candidates.HealthySnapshot())
	if err != nil {
		t.Fatalf("second Select() error = %v", err)
	}
	if second.InstanceID != "pdu-1" {
		t.Errorf("Select() after metrics refresh = %s, want pdu-1", second.InstanceID)
	}
}

func TestLoadBasedDoesNotReserveOrMutateLoad(t *testing.T) {
	snapshot := loadTestSnapshot(t, []loadTestInstance{
		{id: "pdu-1", activeRequests: 1},
		{id: "pdu-2", activeRequests: 2},
	})
	before := snapshot.All()
	selector := NewLoadBased()
	for range 100 {
		instance, err := selector.Select(snapshot)
		if err != nil {
			t.Fatalf("Select() error = %v", err)
		}
		if instance.InstanceID != "pdu-1" {
			t.Fatalf("Select() = %s, want pdu-1 without reservation counter", instance.InstanceID)
		}
	}
	if after := snapshot.All(); !slices.Equal(after, before) {
		t.Errorf("snapshot mutated: before=%+v after=%+v", before, after)
	}
}

func TestLoadBasedExcludesUnhealthyInstance(t *testing.T) {
	candidates, addresses := loadTestRegistry(t, []loadTestInstance{
		{id: "pdu-1", activeRequests: 0},
		{id: "pdu-2", activeRequests: 2},
	})
	if err := candidates.MarkUnhealthy(addresses["pdu-1"], routingTestTime().Add(10*time.Second)); err != nil {
		t.Fatalf("MarkUnhealthy() error = %v", err)
	}
	instance, err := NewLoadBased().Select(candidates.HealthySnapshot())
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if instance.InstanceID != "pdu-2" {
		t.Errorf("Select() = %s, want remaining healthy pdu-2", instance.InstanceID)
	}
}

func TestLoadBasedConcurrentSelection(t *testing.T) {
	snapshot := loadTestSnapshot(t, []loadTestInstance{
		{id: "pdu-1", activeRequests: 3},
		{id: "pdu-2", activeRequests: 1},
		{id: "pdu-3", activeRequests: 2},
	})
	selector := NewLoadBased()
	const goroutineCount = 64
	const selectionsPerGoroutine = 1_000
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
				if instance.InstanceID != "pdu-2" {
					errorsFound <- errors.New("selected a backend other than pdu-2")
					return
				}
			}
		}()
	}
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func BenchmarkLoadBasedSelect(benchmark *testing.B) {
	for _, backendCount := range []int{3, 20} {
		benchmark.Run(fmt.Sprintf("Backends_%d", backendCount), func(benchmark *testing.B) {
			instances := make([]loadTestInstance, backendCount)
			for index := range backendCount {
				instances[index] = loadTestInstance{
					id:             fmt.Sprintf("pdu-%02d", index+1),
					activeRequests: int64((index*7 + 3) % backendCount),
				}
			}
			snapshot := loadTestSnapshot(benchmark, instances)
			selector := NewLoadBased()
			benchmark.ReportAllocs()
			benchmark.ResetTimer()
			for range benchmark.N {
				routingBenchmarkInstance, routingBenchmarkErr = selector.Select(snapshot)
			}
			if routingBenchmarkErr != nil {
				benchmark.Fatalf("Select() error = %v", routingBenchmarkErr)
			}
		})
	}
}

type loadTestInstance struct {
	id             string
	activeRequests int64
}

func loadTestSnapshot(test testingHelper, instances []loadTestInstance) registry.Snapshot {
	test.Helper()
	candidates, _ := loadTestRegistry(test, instances)
	return candidates.HealthySnapshot()
}

func loadTestRegistry(
	test testingHelper,
	instances []loadTestInstance,
) (*registry.Registry, map[string]netip.AddrPort) {
	test.Helper()
	candidates := registry.New()
	addresses := make(map[string]netip.AddrPort, len(instances))
	base := routingTestTime()
	for index, instance := range instances {
		address := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, 0, byte(index + 1)}), 8081)
		addresses[instance.id] = address
		if _, err := candidates.Upsert(address, base); err != nil {
			test.Fatalf("Upsert(%s) error = %v", address, err)
		}
		if err := candidates.MarkHealthy(address, registry.Metadata{
			InstanceID: instance.id, Weight: 1, ActiveRequests: instance.activeRequests,
		}, base.Add(time.Duration(index+1)*time.Second)); err != nil {
			test.Fatalf("MarkHealthy(%s) error = %v", address, err)
		}
	}
	return candidates, addresses
}
