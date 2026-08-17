package monitor

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"time"
)

const (
	defaultMemoryLimitBytes = 1024 * 1024 * 1024 // 1 GiB limit
	historyRetention        = 60
)

// Stats captures a point-in-time snapshot of process resource consumption.
type Stats struct {
	CPUPercent         float64 `json:"cpuPercent"`
	CPUMinPercent      float64 `json:"cpuMinPercent"`
	CPUMaxPercent      float64 `json:"cpuMaxPercent"`
	CPURange           string  `json:"cpuRange"`
	RAMUsageBytes      uint64  `json:"ramUsageBytes"`
	RAMUsageMiB        float64 `json:"ramUsageMiB"`
	RAMPeakMiB         float64 `json:"ramPeakMiB"`
	RAMPercent         float64 `json:"ramPercent"`
	TotalCPUTimeMillis int64   `json:"totalCpuTimeMillis"`
	Goroutines         int     `json:"goroutines"`
	UptimeSeconds      float64 `json:"uptimeSeconds"`
}

// Sampler monitors CPU and memory consumption on demand without background goroutines.
type Sampler struct {
	mu           sync.Mutex
	startedAt    time.Time
	lastSampleAt time.Time
	lastCPUTime  time.Duration
	peakRAMBytes uint64
	recentCPUs   []float64
	stats        Stats
}

// NewSampler creates a resource sampler.
func NewSampler() *Sampler {
	now := time.Now()
	cpuTime, _ := processCPUTime()
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	ramBytes := memStats.Sys
	ramMiB := float64(ramBytes) / (1024 * 1024)
	ramPercent := (float64(ramBytes) / float64(defaultMemoryLimitBytes)) * 100.0

	initialStats := Stats{
		CPUPercent:         0,
		CPUMinPercent:      0,
		CPUMaxPercent:      0,
		CPURange:           "0.00% – 0.00%",
		RAMUsageBytes:      ramBytes,
		RAMUsageMiB:        math.Round(ramMiB*10) / 10,
		RAMPeakMiB:         math.Round(ramMiB*10) / 10,
		RAMPercent:         math.Round(ramPercent*100) / 100,
		TotalCPUTimeMillis: cpuTime.Milliseconds(),
		Goroutines:         runtime.NumGoroutine(),
		UptimeSeconds:      0,
	}

	return &Sampler{
		startedAt:    now,
		lastSampleAt: now,
		lastCPUTime:  cpuTime,
		peakRAMBytes: ramBytes,
		recentCPUs:   make([]float64, 0, historyRetention),
		stats:        initialStats,
	}
}

// Snapshot calculates delta metrics and returns the current live statistics.
func (s *Sampler) Snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	wallDelta := now.Sub(s.lastSampleAt)
	cpuTime, err := processCPUTime()

	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	ramBytes := memStats.Sys
	if ramBytes > s.peakRAMBytes {
		s.peakRAMBytes = ramBytes
	}

	ramMiB := float64(ramBytes) / (1024 * 1024)
	peakMiB := float64(s.peakRAMBytes) / (1024 * 1024)
	ramPercent := (float64(ramBytes) / float64(defaultMemoryLimitBytes)) * 100.0

	if err == nil && wallDelta >= 10*time.Millisecond {
		cpuDelta := cpuTime - s.lastCPUTime
		if cpuDelta >= 0 {
			cpuPercent := (float64(cpuDelta.Nanoseconds()) / float64(wallDelta.Nanoseconds())) * 100.0
			if len(s.recentCPUs) >= historyRetention {
				s.recentCPUs = s.recentCPUs[1:]
			}
			s.recentCPUs = append(s.recentCPUs, cpuPercent)

			minCPU := s.recentCPUs[0]
			maxCPU := s.recentCPUs[0]
			var sumCPU float64
			for _, v := range s.recentCPUs {
				if v < minCPU {
					minCPU = v
				}
				if v > maxCPU {
					maxCPU = v
				}
				sumCPU += v
			}
			avgCPU := sumCPU / float64(len(s.recentCPUs))

			s.lastSampleAt = now
			s.lastCPUTime = cpuTime

			s.stats = Stats{
				CPUPercent:         math.Round(avgCPU*100) / 100,
				CPUMinPercent:      math.Round(minCPU*100) / 100,
				CPUMaxPercent:      math.Round(maxCPU*100) / 100,
				CPURange:           fmt.Sprintf("%.2f%% – %.2f%%", minCPU, maxCPU),
				RAMUsageBytes:      ramBytes,
				RAMUsageMiB:        math.Round(ramMiB*10) / 10,
				RAMPeakMiB:         math.Round(peakMiB*10) / 10,
				RAMPercent:         math.Round(ramPercent*100) / 100,
				TotalCPUTimeMillis: cpuTime.Milliseconds(),
				Goroutines:         runtime.NumGoroutine(),
				UptimeSeconds:      math.Round(now.Sub(s.startedAt).Seconds()*10) / 10,
			}
			return s.stats
		}
	}

	// If interval is too short, return latest stats with refreshed RAM and goroutines
	s.stats.RAMUsageBytes = ramBytes
	s.stats.RAMUsageMiB = math.Round(ramMiB*10) / 10
	s.stats.RAMPeakMiB = math.Round(peakMiB*10) / 10
	s.stats.RAMPercent = math.Round(ramPercent*100) / 100
	s.stats.TotalCPUTimeMillis = cpuTime.Milliseconds()
	s.stats.Goroutines = runtime.NumGoroutine()
	s.stats.UptimeSeconds = math.Round(now.Sub(s.startedAt).Seconds()*10) / 10

	return s.stats
}
