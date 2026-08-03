package routing

import (
	"errors"
	"fmt"

	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// Factory contains the fixed selector set configured during Gateway startup.
// It is immutable after construction and safe for concurrent Create calls.
type Factory struct {
	roundRobin Selector
	weighted   Selector
	load       Selector
}

// NewFactory creates a selector factory from injected algorithm implementations.
func NewFactory(roundRobin, weighted, load Selector) (*Factory, error) {
	if roundRobin == nil {
		return nil, errors.New("create selector factory: round-robin selector must not be nil")
	}
	if weighted == nil {
		return nil, errors.New("create selector factory: weighted selector must not be nil")
	}
	if load == nil {
		return nil, errors.New("create selector factory: load selector must not be nil")
	}
	return &Factory{roundRobin: roundRobin, weighted: weighted, load: load}, nil
}

// Create returns the implementation selected by the startup routing mode.
func (factory *Factory) Create(mode constants.RoutingMode) (Selector, error) {
	switch mode {
	case constants.RoutingRoundRobin:
		return factory.roundRobin, nil
	case constants.RoutingWeighted:
		return factory.weighted, nil
	case constants.RoutingLoad:
		return factory.load, nil
	default:
		return nil, fmt.Errorf("create selector: unsupported routing mode %q", mode)
	}
}
