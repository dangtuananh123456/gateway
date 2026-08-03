package routing

import (
	"strings"

	"github.com/dangtuananh123456/gateway/internal/registry"
)

// LoadBased selects the backend with the lowest cached active request count.
type LoadBased struct{}

var _ Selector = (*LoadBased)(nil)

// NewLoadBased creates a stateless load-based selector.
func NewLoadBased() *LoadBased {
	return &LoadBased{}
}

// Select returns the least-loaded backend, breaking ties by instance ID.
func (*LoadBased) Select(snapshot registry.Snapshot) (registry.Instance, error) {
	selected, found := snapshot.At(0)
	if !found {
		return registry.Instance{}, ErrNoBackend
	}
	for index := 1; index < snapshot.Len(); index++ {
		candidate, _ := snapshot.At(index)
		if candidate.ActiveRequests < selected.ActiveRequests ||
			(candidate.ActiveRequests == selected.ActiveRequests &&
				strings.Compare(candidate.InstanceID, selected.InstanceID) < 0) {
			selected = candidate
		}
	}
	return selected, nil
}
