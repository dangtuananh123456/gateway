package routing

import (
	"errors"

	"github.com/dangtuananh123456/gateway/internal/registry"
)

var (
	// ErrNoBackend is returned when the immutable snapshot has no routable PDU.
	ErrNoBackend = errors.New("no backend available")
)

// Selector chooses one backend from an immutable routing snapshot.
// Implementations must not retain or mutate data returned by the snapshot.
type Selector interface {
	Select(registry.Snapshot) (registry.Instance, error)
}
