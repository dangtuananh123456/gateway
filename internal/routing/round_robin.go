package routing

import (
	"sync/atomic"

	"github.com/dangtuananh123456/gateway/internal/registry"
)

// RoundRobin selects each backend in snapshot order using one atomic sequence.
type RoundRobin struct {
	counter atomic.Uint64
}

var _ Selector = (*RoundRobin)(nil)

// NewRoundRobin creates a concurrency-safe Round Robin selector.
func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

// Select returns the next backend from the request's immutable snapshot.
func (selector *RoundRobin) Select(snapshot registry.Snapshot) (registry.Instance, error) {
	backendCount := snapshot.Len()
	if backendCount == 0 {
		return registry.Instance{}, ErrNoBackend
	}

	sequence := selector.counter.Add(1) - 1
	instance, _ := snapshot.At(int(sequence % uint64(backendCount)))
	return instance, nil
}
