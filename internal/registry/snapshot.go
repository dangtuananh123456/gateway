package registry

import (
	"net/netip"
	"slices"
)

// Instance is the routing-safe state of one healthy PDU instance.
type Instance struct {
	Address        netip.AddrPort
	InstanceID     string
	Weight         int
	ActiveRequests int64
}

// Snapshot is immutable after construction. It exposes no mutable backing slice.
type Snapshot struct {
	instances []Instance
}

func newSnapshot(instances []Instance) Snapshot {
	return Snapshot{instances: instances}
}

// Len returns the number of routable instances.
func (snapshot Snapshot) Len() int {
	return len(snapshot.instances)
}

// At returns an instance value at index without exposing the backing slice.
func (snapshot Snapshot) At(index int) (Instance, bool) {
	if index < 0 || index >= len(snapshot.instances) {
		return Instance{}, false
	}
	return snapshot.instances[index], true
}

// All returns a defensive copy for diagnostics and non-hot-path consumers.
func (snapshot Snapshot) All() []Instance {
	return slices.Clone(snapshot.instances)
}
