package registry

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

func BenchmarkHealthySnapshot(benchmark *testing.B) {
	for _, backendCount := range []int{3, 20} {
		benchmark.Run(fmt.Sprintf("Backends_%d", backendCount), func(benchmark *testing.B) {
			candidates, _ := benchmarkRegistry(benchmark, backendCount)
			benchmark.ReportAllocs()
			benchmark.ResetTimer()
			for range benchmark.N {
				registryBenchmarkSnapshot = candidates.HealthySnapshot()
			}
		})
	}
}

func BenchmarkMarkMetricsSuccess(benchmark *testing.B) {
	for _, backendCount := range []int{3, 20} {
		benchmark.Run(fmt.Sprintf("Backends_%d", backendCount), func(benchmark *testing.B) {
			candidates, addresses := benchmarkRegistry(benchmark, backendCount)
			metadata := make([]Metadata, backendCount)
			for index := range backendCount {
				metadata[index] = Metadata{
					InstanceID:     fmt.Sprintf("pdu-%02d", index+1),
					Weight:         index%3 + 1,
					ActiveRequests: int64(index),
				}
			}
			base := testTime().Add(24 * time.Hour)
			benchmark.ReportAllocs()
			benchmark.ResetTimer()
			for iteration := range benchmark.N {
				index := iteration % backendCount
				registryBenchmarkErr = candidates.MarkMetricsSuccess(
					addresses[index],
					metadata[index],
					base.Add(time.Duration(iteration+1)*time.Nanosecond),
				)
			}
			if registryBenchmarkErr != nil {
				benchmark.Fatalf("MarkMetricsSuccess() error = %v", registryBenchmarkErr)
			}
		})
	}
}

func benchmarkRegistry(benchmark *testing.B, backendCount int) (*Registry, []netip.AddrPort) {
	benchmark.Helper()
	candidates := New()
	addresses := make([]netip.AddrPort, backendCount)
	base := testTime()
	for index := range backendCount {
		address := netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 0, 0, byte(index + 1)}), 8081)
		addresses[index] = address
		if _, err := candidates.Upsert(address, base); err != nil {
			benchmark.Fatalf("Upsert(%s) error = %v", address, err)
		}
		if err := candidates.MarkHealthy(address, Metadata{
			InstanceID:     fmt.Sprintf("pdu-%02d", index+1),
			Weight:         index%3 + 1,
			ActiveRequests: int64(index),
		}, base.Add(time.Duration(index+1)*time.Second)); err != nil {
			benchmark.Fatalf("MarkHealthy(%s) error = %v", address, err)
		}
	}
	return candidates, addresses
}

var (
	registryBenchmarkSnapshot Snapshot
	registryBenchmarkErr      error
)
