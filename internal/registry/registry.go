// Package registry maintains discovered PDU candidates and immutable healthy snapshots.
package registry

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	// ErrCandidateNotFound indicates that discovery has not registered an address.
	ErrCandidateNotFound = errors.New("PDU candidate not found")
	// ErrInvalidCandidate indicates invalid address, metadata, load, or timestamp input.
	ErrInvalidCandidate = errors.New("invalid PDU candidate")
)

// Candidate contains all mutable discovery and observation state for one address.
type Candidate struct {
	Address              netip.AddrPort
	Healthy              bool
	InstanceID           string
	Weight               int
	ActiveRequests       int64
	DiscoveredAt         time.Time
	LastSeenAt           time.Time
	LastSuccessAt        time.Time
	LastHealthSuccessAt  time.Time
	LastMetricsSuccessAt time.Time
	LastIdentityAt       time.Time
	LastFailureAt        time.Time
}

// Metadata is the health and load state collected from one PDU instance.
type Metadata struct {
	InstanceID     string
	Weight         int
	ActiveRequests int64
}

// Registry protects the candidate map and creates coherent healthy snapshots.
type Registry struct {
	mu         sync.RWMutex
	candidates map[netip.AddrPort]Candidate
}

// New creates an empty instance registry.
func New() *Registry {
	return &Registry{candidates: make(map[netip.AddrPort]Candidate)}
}

// Upsert records an address observed through DNS. New candidates start unhealthy.
func (registry *Registry) Upsert(address netip.AddrPort, seenAt time.Time) (bool, error) {
	if err := validateAddressAndTime(address, seenAt); err != nil {
		return false, err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	candidate, found := registry.candidates[address]
	if !found {
		registry.candidates[address] = Candidate{
			Address:      address,
			DiscoveredAt: seenAt,
			LastSeenAt:   seenAt,
		}
		return true, nil
	}
	if seenAt.After(candidate.LastSeenAt) {
		candidate.LastSeenAt = seenAt
		registry.candidates[address] = candidate
	}
	return false, nil
}

// Remove deletes a candidate and prevents it from appearing in future snapshots.
func (registry *Registry) Remove(address netip.AddrPort) bool {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, found := registry.candidates[address]; !found {
		return false
	}
	delete(registry.candidates, address)
	return true
}

// MarkHealthy atomically publishes identity, weight, and cached load for a candidate.
func (registry *Registry) MarkHealthy(
	address netip.AddrPort,
	metadata Metadata,
	observedAt time.Time,
) error {
	if err := validateMetadata(metadata, observedAt); err != nil {
		return err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	candidate, found := registry.candidates[address]
	if !found {
		return fmt.Errorf("%w: %s", ErrCandidateNotFound, address)
	}
	if observationIsOlder(candidate, observedAt) {
		return nil
	}
	candidate.Healthy = true
	candidate.InstanceID = metadata.InstanceID
	candidate.Weight = metadata.Weight
	candidate.ActiveRequests = metadata.ActiveRequests
	candidate.LastSuccessAt = observedAt
	candidate.LastHealthSuccessAt = observedAt
	candidate.LastMetricsSuccessAt = observedAt
	candidate.LastIdentityAt = observedAt
	registry.candidates[address] = candidate
	return nil
}

// MarkHealthSuccess records a successful health probe. A new or changed
// identity must also publish matching metrics before it becomes routable.
func (registry *Registry) MarkHealthSuccess(
	address netip.AddrPort,
	instanceID string,
	observedAt time.Time,
) error {
	if err := validateIdentity(instanceID, observedAt); err != nil {
		return err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	candidate, found := registry.candidates[address]
	if !found {
		return fmt.Errorf("%w: %s", ErrCandidateNotFound, address)
	}
	if observedAt.Before(candidate.LastHealthSuccessAt) ||
		(instanceID != candidate.InstanceID && observedAt.Before(candidate.LastIdentityAt)) {
		return nil
	}
	if candidate.InstanceID != instanceID {
		candidate.InstanceID = instanceID
		candidate.Weight = 0
		candidate.ActiveRequests = 0
		candidate.LastMetricsSuccessAt = time.Time{}
		candidate.LastIdentityAt = observedAt
	}
	candidate.Healthy = true
	candidate.LastHealthSuccessAt = observedAt
	if observedAt.After(candidate.LastSuccessAt) {
		candidate.LastSuccessAt = observedAt
	}
	registry.candidates[address] = candidate
	return nil
}

// MarkMetricsSuccess refreshes the cached weight and load. Metrics alone do
// not make a candidate healthy; a matching health probe is also required.
func (registry *Registry) MarkMetricsSuccess(
	address netip.AddrPort,
	metadata Metadata,
	observedAt time.Time,
) error {
	if err := validateMetadata(metadata, observedAt); err != nil {
		return err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	candidate, found := registry.candidates[address]
	if !found {
		return fmt.Errorf("%w: %s", ErrCandidateNotFound, address)
	}
	if observedAt.Before(candidate.LastMetricsSuccessAt) ||
		(metadata.InstanceID != candidate.InstanceID && observedAt.Before(candidate.LastIdentityAt)) {
		return nil
	}
	if candidate.InstanceID != metadata.InstanceID {
		candidate.Healthy = false
		candidate.InstanceID = metadata.InstanceID
		candidate.LastHealthSuccessAt = time.Time{}
		candidate.LastIdentityAt = observedAt
	}
	candidate.Weight = metadata.Weight
	candidate.ActiveRequests = metadata.ActiveRequests
	candidate.LastMetricsSuccessAt = observedAt
	if observedAt.After(candidate.LastSuccessAt) {
		candidate.LastSuccessAt = observedAt
	}
	registry.candidates[address] = candidate
	return nil
}

// MarkUnhealthy removes a candidate from routing while retaining discovery state.
func (registry *Registry) MarkUnhealthy(address netip.AddrPort, observedAt time.Time) error {
	if err := validateAddressAndTime(address, observedAt); err != nil {
		return err
	}

	registry.mu.Lock()
	defer registry.mu.Unlock()
	candidate, found := registry.candidates[address]
	if !found {
		return fmt.Errorf("%w: %s", ErrCandidateNotFound, address)
	}
	if observationIsOlder(candidate, observedAt) {
		return nil
	}
	candidate.Healthy = false
	candidate.LastFailureAt = observedAt
	registry.candidates[address] = candidate
	return nil
}

// Get returns a value copy that cannot mutate registry state.
func (registry *Registry) Get(address netip.AddrPort) (Candidate, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	candidate, found := registry.candidates[address]
	return candidate, found
}

// Candidates returns a deterministic copy sorted by address.
func (registry *Registry) Candidates() []Candidate {
	registry.mu.RLock()
	result := make([]Candidate, 0, len(registry.candidates))
	for _, candidate := range registry.candidates {
		result = append(result, candidate)
	}
	registry.mu.RUnlock()

	slices.SortFunc(result, func(first, second Candidate) int {
		return first.Address.Compare(second.Address)
	})
	return result
}

// HealthySnapshot returns a coherent immutable view containing only routable instances.
func (registry *Registry) HealthySnapshot() Snapshot {
	registry.mu.RLock()
	instances := make([]Instance, 0, len(registry.candidates))
	for _, candidate := range registry.candidates {
		if !candidate.Healthy || candidate.InstanceID == "" || candidate.Weight <= 0 ||
			candidate.LastHealthSuccessAt.IsZero() || candidate.LastMetricsSuccessAt.IsZero() {
			continue
		}
		instances = append(instances, Instance{
			Address:        candidate.Address,
			InstanceID:     candidate.InstanceID,
			Weight:         candidate.Weight,
			ActiveRequests: candidate.ActiveRequests,
		})
	}
	registry.mu.RUnlock()

	slices.SortFunc(instances, func(first, second Instance) int {
		if comparison := strings.Compare(first.InstanceID, second.InstanceID); comparison != 0 {
			return comparison
		}
		return first.Address.Compare(second.Address)
	})
	return newSnapshot(instances)
}

func validateAddressAndTime(address netip.AddrPort, observedAt time.Time) error {
	if !address.IsValid() || address.Port() == 0 {
		return fmt.Errorf("%w: address must contain a valid IP and non-zero port", ErrInvalidCandidate)
	}
	if observedAt.IsZero() {
		return fmt.Errorf("%w: observation timestamp must not be zero", ErrInvalidCandidate)
	}
	return nil
}

func validateMetadata(metadata Metadata, observedAt time.Time) error {
	if err := validateIdentity(metadata.InstanceID, observedAt); err != nil {
		return err
	}
	if metadata.Weight <= 0 {
		return fmt.Errorf("%w: weight must be greater than zero", ErrInvalidCandidate)
	}
	if metadata.ActiveRequests < 0 {
		return fmt.Errorf("%w: active requests must not be negative", ErrInvalidCandidate)
	}
	return nil
}

func validateIdentity(instanceID string, observedAt time.Time) error {
	if strings.TrimSpace(instanceID) == "" || instanceID != strings.TrimSpace(instanceID) {
		return fmt.Errorf("%w: instance ID must be non-empty without surrounding whitespace", ErrInvalidCandidate)
	}
	if observedAt.IsZero() {
		return fmt.Errorf("%w: observation timestamp must not be zero", ErrInvalidCandidate)
	}
	return nil
}

func observationIsOlder(candidate Candidate, observedAt time.Time) bool {
	lastStateAt := candidate.LastSuccessAt
	if candidate.LastFailureAt.After(lastStateAt) {
		lastStateAt = candidate.LastFailureAt
	}
	return observedAt.Before(lastStateAt)
}
