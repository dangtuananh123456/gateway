package routing

import (
	"strings"
	"sync"

	"github.com/dangtuananh123456/gateway/internal/registry"
)

// SmoothWeighted implements Smooth Weighted Round Robin across changing snapshots.
type SmoothWeighted struct {
	mu         sync.Mutex
	states     map[string]weightedState
	generation uint64
}

type weightedState struct {
	currentWeight int64
	weight        int
	generation    uint64
}

var _ Selector = (*SmoothWeighted)(nil)

// NewSmoothWeighted creates a concurrency-safe weighted selector.
func NewSmoothWeighted() *SmoothWeighted {
	return &SmoothWeighted{states: make(map[string]weightedState)}
}

// Select performs one complete Smooth Weighted Round Robin update atomically.
func (selector *SmoothWeighted) Select(snapshot registry.Snapshot) (registry.Instance, error) {
	selector.mu.Lock()
	defer selector.mu.Unlock()

	if snapshot.Len() == 0 {
		clear(selector.states)
		return registry.Instance{}, ErrNoBackend
	}
	generation := selector.nextGeneration()
	var winner registry.Instance
	var winnerCurrent int64
	winnerFound := false
	var totalWeight int64

	for index := range snapshot.Len() {
		instance, _ := snapshot.At(index)
		state := selector.states[instance.InstanceID]
		state.weight = instance.Weight
		state.currentWeight += int64(instance.Weight)
		state.generation = generation
		selector.states[instance.InstanceID] = state
		totalWeight += int64(instance.Weight)

		if !winnerFound || state.currentWeight > winnerCurrent ||
			(state.currentWeight == winnerCurrent && strings.Compare(instance.InstanceID, winner.InstanceID) < 0) {
			winner = instance
			winnerCurrent = state.currentWeight
			winnerFound = true
		}
	}
	for instanceID, state := range selector.states {
		if state.generation != generation {
			delete(selector.states, instanceID)
		}
	}

	winnerState := selector.states[winner.InstanceID]
	winnerState.currentWeight -= totalWeight
	selector.states[winner.InstanceID] = winnerState
	return winner, nil
}

func (selector *SmoothWeighted) nextGeneration() uint64 {
	selector.generation++
	if selector.generation != 0 {
		return selector.generation
	}
	for instanceID, state := range selector.states {
		state.generation = 0
		selector.states[instanceID] = state
	}
	selector.generation = 1
	return selector.generation
}
