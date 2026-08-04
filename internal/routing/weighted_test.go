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

func TestSmoothWeightedSequenceAndRatio(t *testing.T) {
	snapshot := weightedTestSnapshot(t, map[string]int{
		"pdu-1": 3,
		"pdu-2": 2,
		"pdu-3": 1,
	})
	selector := NewSmoothWeighted()
	want := []string{"pdu-1", "pdu-2", "pdu-1", "pdu-3", "pdu-2", "pdu-1"}
	counts := make(map[string]int, len(want))
	for index, wantID := range want {
		instance, err := selector.Select(snapshot)
		if err != nil {
			t.Fatalf("Select() request %d error = %v", index+1, err)
		}
		counts[instance.InstanceID]++
		if instance.InstanceID != wantID {
			t.Errorf("Select() request %d = %s, want %s", index+1, instance.InstanceID, wantID)
		}
	}
	if counts["pdu-1"] != 3 || counts["pdu-2"] != 2 || counts["pdu-3"] != 1 {
		t.Errorf("selection ratio = %+v, want 3:2:1", counts)
	}
}

func TestSmoothWeightedReturnsNoBackendAndClearsState(t *testing.T) {
	selector := NewSmoothWeighted()
	if _, err := selector.Select(weightedTestSnapshot(t, map[string]int{"pdu-1": 1})); err != nil {
		t.Fatalf("Select(non-empty) error = %v", err)
	}
	instance, err := selector.Select(registry.Snapshot{})
	if !errors.Is(err, ErrNoBackend) {
		t.Errorf("Select(empty) error = %v, want ErrNoBackend", err)
	}
	if instance != (registry.Instance{}) {
		t.Errorf("Select(empty) instance = %+v, want zero value", instance)
	}
	if len(selector.states) != 0 {
		t.Errorf("states after empty snapshot = %d, want 0", len(selector.states))
	}
}

func TestSmoothWeightedReconcilesMembershipAndWeight(t *testing.T) {
	candidates, addresses := weightedTestRegistry(t, map[string]int{
		"pdu-1": 3,
		"pdu-2": 2,
		"pdu-3": 1,
	})
	selector := NewSmoothWeighted()
	if _, err := selector.Select(candidates.HealthySnapshot()); err != nil {
		t.Fatalf("initial Select() error = %v", err)
	}
	base := routingTestTime().Add(10 * time.Second)
	if err := candidates.MarkUnhealthy(addresses["pdu-2"], base); err != nil {
		t.Fatalf("MarkUnhealthy(pdu-2) error = %v", err)
	}
	if err := candidates.MarkHealthy(addresses["pdu-3"], registry.Metadata{
		InstanceID: "pdu-3", Weight: 5,
	}, base.Add(time.Second)); err != nil {
		t.Fatalf("MarkHealthy(pdu-3) error = %v", err)
	}
	if _, err := selector.Select(candidates.HealthySnapshot()); err != nil {
		t.Fatalf("Select(changed snapshot) error = %v", err)
	}
	if _, found := selector.states["pdu-2"]; found {
		t.Error("state for removed pdu-2 was retained")
	}
	if state := selector.states["pdu-3"]; state.weight != 5 {
		t.Errorf("pdu-3 cached weight = %d, want 5", state.weight)
	}
}

func TestSmoothWeightedTieBreaksByInstanceID(t *testing.T) {
	snapshot := weightedTestSnapshot(t, map[string]int{"pdu-b": 1, "pdu-a": 1})
	instance, err := NewSmoothWeighted().Select(snapshot)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if instance.InstanceID != "pdu-a" {
		t.Errorf("Select() = %s, want lexical pdu-a", instance.InstanceID)
	}
}

func TestSmoothWeightedHandlesGenerationOverflow(t *testing.T) {
	selector := NewSmoothWeighted()
	selector.generation = math.MaxUint64
	selector.states["removed-pdu"] = weightedState{currentWeight: 10, weight: 1, generation: 1}

	instance, err := selector.Select(weightedTestSnapshot(t, map[string]int{"pdu-1": 1}))
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if instance.InstanceID != "pdu-1" {
		t.Errorf("Select() = %s, want pdu-1", instance.InstanceID)
	}
	if selector.generation != 1 {
		t.Errorf("generation after overflow = %d, want 1", selector.generation)
	}
	if _, found := selector.states["removed-pdu"]; found {
		t.Error("state removed before generation overflow was retained")
	}
}

func TestSmoothWeightedConcurrentSelectionHasExactRatio(t *testing.T) {
	snapshot := weightedTestSnapshot(t, map[string]int{
		"pdu-1": 3,
		"pdu-2": 2,
		"pdu-3": 1,
	})
	selector := NewSmoothWeighted()
	const goroutineCount = 60
	const selectionsPerGoroutine = 100
	var counts [3]atomic.Int64
	indices := map[string]int{"pdu-1": 0, "pdu-2": 1, "pdu-3": 2}
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
				counts[indices[instance.InstanceID]].Add(1)
			}
		}()
	}
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}

	if got := counts[0].Load(); got != 3_000 {
		t.Errorf("pdu-1 selections = %d, want 3000", got)
	}
	if got := counts[1].Load(); got != 2_000 {
		t.Errorf("pdu-2 selections = %d, want 2000", got)
	}
	if got := counts[2].Load(); got != 1_000 {
		t.Errorf("pdu-3 selections = %d, want 1000", got)
	}
}

func TestSmoothWeightedConcurrentScaleChanges(t *testing.T) {
	candidates, addresses := weightedTestRegistry(t, map[string]int{
		"pdu-1": 3,
		"pdu-2": 2,
		"pdu-3": 1,
	})
	selector := NewSmoothWeighted()
	const iterations = 500
	errorsFound := make(chan error, 16)
	var workers sync.WaitGroup
	workers.Add(9)
	for range 8 {
		go func() {
			defer workers.Done()
			for range iterations {
				if _, err := selector.Select(candidates.HealthySnapshot()); err != nil {
					errorsFound <- err
					return
				}
			}
		}()
	}
	go func() {
		defer workers.Done()
		base := routingTestTime().Add(10 * time.Second)
		for iteration := range iterations {
			observedAt := base.Add(time.Duration(iteration*2) * time.Nanosecond)
			if err := candidates.MarkUnhealthy(addresses["pdu-3"], observedAt); err != nil {
				errorsFound <- err
				return
			}
			if err := candidates.MarkHealthy(addresses["pdu-3"], registry.Metadata{
				InstanceID: "pdu-3", Weight: iteration%3 + 1,
			}, observedAt.Add(time.Nanosecond)); err != nil {
				errorsFound <- err
				return
			}
		}
	}()
	workers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

func BenchmarkSmoothWeightedSelect(benchmark *testing.B) {
	for _, backendCount := range []int{3, 20} {
		benchmark.Run(fmt.Sprintf("Backends_%d", backendCount), func(benchmark *testing.B) {
			weights := make(map[string]int, backendCount)
			for index := range backendCount {
				weights[fmt.Sprintf("pdu-%02d", index+1)] = index%3 + 1
			}
			snapshot := weightedTestSnapshot(benchmark, weights)
			selector := NewSmoothWeighted()
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

func weightedTestSnapshot(test testingHelper, weights map[string]int) registry.Snapshot {
	test.Helper()
	candidates, _ := weightedTestRegistry(test, weights)
	return candidates.HealthySnapshot()
}

func weightedTestRegistry(
	test testingHelper,
	weights map[string]int,
) (*registry.Registry, map[string]netip.AddrPort) {
	test.Helper()
	candidates := registry.New()
	addresses := make(map[string]netip.AddrPort, len(weights))
	base := routingTestTime()
	index := 0
	for instanceID, weight := range weights {
		index++
		address := netip.MustParseAddrPort(fmt.Sprintf("10.0.0.%d:8081", index))
		addresses[instanceID] = address
		if _, err := candidates.Upsert(address, base); err != nil {
			test.Fatalf("Upsert(%s) error = %v", address, err)
		}
		if err := candidates.MarkHealthy(address, registry.Metadata{
			InstanceID: instanceID, Weight: weight,
		}, base.Add(time.Duration(index)*time.Second)); err != nil {
			test.Fatalf("MarkHealthy(%s) error = %v", address, err)
		}
	}
	return candidates, addresses
}
