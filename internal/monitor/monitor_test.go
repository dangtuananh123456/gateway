package monitor

import (
	"testing"
	"time"
)

func TestSamplerCollectsStats(t *testing.T) {
	sampler := NewSampler()

	// Busy loop to burn some CPU cycles
	start := time.Now()
	for time.Since(start) < 30*time.Millisecond {
		_ = time.Now().UnixNano()
	}

	stats := sampler.Snapshot()

	if stats.RAMUsageBytes == 0 {
		t.Errorf("RAMUsageBytes = 0, want > 0")
	}
	if stats.RAMUsageMiB <= 0 {
		t.Errorf("RAMUsageMiB = %f, want > 0", stats.RAMUsageMiB)
	}
	if stats.Goroutines <= 0 {
		t.Errorf("Goroutines = %d, want > 0", stats.Goroutines)
	}
	if stats.CPUPercent < 0 {
		t.Errorf("CPUPercent = %f, want >= 0", stats.CPUPercent)
	}
}
