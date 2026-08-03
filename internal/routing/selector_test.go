package routing

import (
	"errors"
	"testing"

	"github.com/dangtuananh123456/gateway/internal/registry"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestFactoryCreatesSelectorForConfiguredMode(t *testing.T) {
	roundRobin := &fakeSelector{name: "round-robin"}
	weighted := &fakeSelector{name: "weighted"}
	load := &fakeSelector{name: "load"}
	factory, err := NewFactory(roundRobin, weighted, load)
	if err != nil {
		t.Fatalf("NewFactory() error = %v", err)
	}

	tests := []struct {
		mode constants.RoutingMode
		want Selector
	}{
		{mode: constants.RoutingRoundRobin, want: roundRobin},
		{mode: constants.RoutingWeighted, want: weighted},
		{mode: constants.RoutingLoad, want: load},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			got, err := factory.Create(test.mode)
			if err != nil {
				t.Fatalf("Create(%q) error = %v", test.mode, err)
			}
			if got != test.want {
				t.Errorf("Create(%q) = %T, want injected %T", test.mode, got, test.want)
			}
		})
	}
}

func TestFactoryFailsFastForUnsupportedMode(t *testing.T) {
	selector := &fakeSelector{}
	factory, err := NewFactory(selector, selector, selector)
	if err != nil {
		t.Fatalf("NewFactory() error = %v", err)
	}
	if got, err := factory.Create(constants.RoutingMode("random")); err == nil || got != nil {
		t.Fatalf("Create(random) = %v, %v; want nil, error", got, err)
	}
}

func TestNewFactoryRejectsMissingSelector(t *testing.T) {
	selector := &fakeSelector{}
	tests := []struct {
		name                       string
		roundRobin, weighted, load Selector
	}{
		{name: "round robin", weighted: selector, load: selector},
		{name: "weighted", roundRobin: selector, load: selector},
		{name: "load", roundRobin: selector, weighted: selector},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewFactory(test.roundRobin, test.weighted, test.load); err == nil {
				t.Fatal("NewFactory() error = nil, want missing dependency error")
			}
		})
	}
}

func TestSelectorCanBeInjectedAndReturnsStableNoBackendError(t *testing.T) {
	selector := &fakeSelector{err: ErrNoBackend}
	factory, err := NewFactory(selector, selector, selector)
	if err != nil {
		t.Fatalf("NewFactory() error = %v", err)
	}
	selected, err := factory.Create(constants.RoutingRoundRobin)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := selected.Select(registry.Snapshot{}); !errors.Is(err, ErrNoBackend) {
		t.Errorf("Select(empty) error = %v, want ErrNoBackend", err)
	}
	if selector.calls != 1 {
		t.Errorf("fake selector calls = %d, want 1", selector.calls)
	}
}

type fakeSelector struct {
	name  string
	err   error
	calls int
}

func (selector *fakeSelector) Select(registry.Snapshot) (registry.Instance, error) {
	selector.calls++
	return registry.Instance{}, selector.err
}
