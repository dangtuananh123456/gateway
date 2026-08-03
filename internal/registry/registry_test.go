package registry

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func TestRegistryCandidateLifecycle(t *testing.T) {
	registry := New()
	address := netip.MustParseAddrPort("10.0.0.1:8081")
	discoveredAt := testTime()

	added, err := registry.Upsert(address, discoveredAt)
	if err != nil || !added {
		t.Fatalf("Upsert() = %t, %v; want true, nil", added, err)
	}
	candidate, found := registry.Get(address)
	if !found {
		t.Fatal("Get() did not find new candidate")
	}
	if candidate.Healthy || candidate.Address != address ||
		!candidate.DiscoveredAt.Equal(discoveredAt) || !candidate.LastSeenAt.Equal(discoveredAt) {
		t.Errorf("new candidate = %+v, want unhealthy candidate with discovery timestamps", candidate)
	}
	if snapshot := registry.HealthySnapshot(); snapshot.Len() != 0 {
		t.Errorf("healthy snapshot length = %d, want 0 before health success", snapshot.Len())
	}

	newSeenAt := discoveredAt.Add(time.Second)
	added, err = registry.Upsert(address, newSeenAt)
	if err != nil || added {
		t.Fatalf("second Upsert() = %t, %v; want false, nil", added, err)
	}
	candidate, _ = registry.Get(address)
	if !candidate.LastSeenAt.Equal(newSeenAt) || !candidate.DiscoveredAt.Equal(discoveredAt) {
		t.Errorf("candidate timestamps = %+v, want original discovery and updated last seen", candidate)
	}

	metadata := Metadata{InstanceID: "pdu-session-1", Weight: 3, ActiveRequests: 7}
	observedAt := discoveredAt.Add(2 * time.Second)
	if err := registry.MarkHealthy(address, metadata, observedAt); err != nil {
		t.Fatalf("MarkHealthy() error = %v", err)
	}
	candidate, _ = registry.Get(address)
	if !candidate.Healthy || candidate.InstanceID != metadata.InstanceID ||
		candidate.Weight != metadata.Weight || candidate.ActiveRequests != metadata.ActiveRequests ||
		!candidate.LastSuccessAt.Equal(observedAt) {
		t.Errorf("healthy candidate = %+v, want metadata %+v", candidate, metadata)
	}

	failedAt := observedAt.Add(time.Second)
	if err := registry.MarkUnhealthy(address, failedAt); err != nil {
		t.Fatalf("MarkUnhealthy() error = %v", err)
	}
	candidate, _ = registry.Get(address)
	if candidate.Healthy || !candidate.LastFailureAt.Equal(failedAt) {
		t.Errorf("candidate after failure = %+v, want unhealthy with failure timestamp", candidate)
	}
	if snapshot := registry.HealthySnapshot(); snapshot.Len() != 0 {
		t.Errorf("snapshot length after failure = %d, want 0", snapshot.Len())
	}

	if !registry.Remove(address) {
		t.Error("Remove() = false, want true")
	}
	if _, found := registry.Get(address); found {
		t.Error("Get() found removed candidate")
	}
	if registry.Remove(address) {
		t.Error("second Remove() = true, want false")
	}
}

func TestRegistryIgnoresStaleObservation(t *testing.T) {
	registry := New()
	address := netip.MustParseAddrPort("10.0.0.1:8081")
	base := testTime()
	if _, err := registry.Upsert(address, base); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	latest := base.Add(2 * time.Second)
	if err := registry.MarkHealthy(address, Metadata{
		InstanceID: "pdu-new", Weight: 2, ActiveRequests: 4,
	}, latest); err != nil {
		t.Fatalf("MarkHealthy() error = %v", err)
	}
	if err := registry.MarkUnhealthy(address, base.Add(time.Second)); err != nil {
		t.Fatalf("stale MarkUnhealthy() error = %v", err)
	}

	candidate, _ := registry.Get(address)
	if !candidate.Healthy || candidate.InstanceID != "pdu-new" {
		t.Errorf("candidate = %+v, want latest healthy observation retained", candidate)
	}
}

func TestRegistryRequiresMatchingHealthAndMetrics(t *testing.T) {
	registry := New()
	address := netip.MustParseAddrPort("10.0.0.1:8081")
	base := testTime()
	if _, err := registry.Upsert(address, base); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if err := registry.MarkHealthSuccess(address, "pdu-1", base.Add(time.Second)); err != nil {
		t.Fatalf("MarkHealthSuccess() error = %v", err)
	}
	if got := registry.HealthySnapshot().Len(); got != 0 {
		t.Fatalf("snapshot after health only = %d, want 0", got)
	}
	if err := registry.MarkMetricsSuccess(address, Metadata{
		InstanceID: "pdu-1", Weight: 2, ActiveRequests: 4,
	}, base.Add(2*time.Second)); err != nil {
		t.Fatalf("MarkMetricsSuccess() error = %v", err)
	}
	if got := registry.HealthySnapshot().Len(); got != 1 {
		t.Fatalf("snapshot after matching metrics = %d, want 1", got)
	}

	if err := registry.MarkMetricsSuccess(address, Metadata{
		InstanceID: "pdu-2", Weight: 3, ActiveRequests: 1,
	}, base.Add(3*time.Second)); err != nil {
		t.Fatalf("changed MarkMetricsSuccess() error = %v", err)
	}
	if got := registry.HealthySnapshot().Len(); got != 0 {
		t.Fatalf("snapshot after identity change = %d, want 0", got)
	}
	if err := registry.MarkHealthSuccess(address, "pdu-2", base.Add(4*time.Second)); err != nil {
		t.Fatalf("changed MarkHealthSuccess() error = %v", err)
	}
	if got := registry.HealthySnapshot().Len(); got != 1 {
		t.Errorf("snapshot after matching health recovery = %d, want 1", got)
	}
}

func TestHealthySnapshotIsSortedAndImmutable(t *testing.T) {
	registry := New()
	base := testTime()
	entries := []struct {
		address  string
		metadata Metadata
	}{
		{address: "10.0.0.3:8081", metadata: Metadata{InstanceID: "pdu-2", Weight: 2, ActiveRequests: 3}},
		{address: "10.0.0.2:8081", metadata: Metadata{InstanceID: "pdu-1", Weight: 1, ActiveRequests: 2}},
		{address: "10.0.0.1:8081", metadata: Metadata{InstanceID: "pdu-1", Weight: 3, ActiveRequests: 1}},
	}
	for index, entry := range entries {
		address := netip.MustParseAddrPort(entry.address)
		if _, err := registry.Upsert(address, base); err != nil {
			t.Fatalf("Upsert(%s) error = %v", address, err)
		}
		if err := registry.MarkHealthy(address, entry.metadata, base.Add(time.Duration(index+1)*time.Second)); err != nil {
			t.Fatalf("MarkHealthy(%s) error = %v", address, err)
		}
	}
	// A discovered-only candidate must never become routable.
	if _, err := registry.Upsert(netip.MustParseAddrPort("10.0.0.4:8081"), base); err != nil {
		t.Fatalf("Upsert(unhealthy) error = %v", err)
	}

	snapshot := registry.HealthySnapshot()
	wantAddresses := []netip.AddrPort{
		netip.MustParseAddrPort("10.0.0.1:8081"),
		netip.MustParseAddrPort("10.0.0.2:8081"),
		netip.MustParseAddrPort("10.0.0.3:8081"),
	}
	got := snapshot.All()
	if snapshot.Len() != len(wantAddresses) {
		t.Fatalf("snapshot length = %d, want %d", snapshot.Len(), len(wantAddresses))
	}
	for index, wantAddress := range wantAddresses {
		if got[index].Address != wantAddress {
			t.Errorf("snapshot[%d].Address = %s, want %s", index, got[index].Address, wantAddress)
		}
	}

	got[0].Weight = 999
	unchanged, found := snapshot.At(0)
	if !found || unchanged.Weight == 999 {
		t.Errorf("snapshot was mutated through All(): %+v", unchanged)
	}
	if _, found := snapshot.At(-1); found {
		t.Error("At(-1) found an instance")
	}
	if _, found := snapshot.At(snapshot.Len()); found {
		t.Error("At(len) found an instance")
	}

	candidate, _ := registry.Get(wantAddresses[0])
	candidate.Weight = 999
	storedAgain, _ := registry.Get(wantAddresses[0])
	if storedAgain.Weight == 999 {
		t.Error("registry state was mutated through Get() result")
	}
}

func TestCandidatesReturnsSortedCopy(t *testing.T) {
	registry := New()
	base := testTime()
	for _, rawAddress := range []string{"10.0.0.10:8081", "10.0.0.2:8081", "10.0.0.1:8081"} {
		if _, err := registry.Upsert(netip.MustParseAddrPort(rawAddress), base); err != nil {
			t.Fatalf("Upsert(%s) error = %v", rawAddress, err)
		}
	}
	candidates := registry.Candidates()
	want := []netip.AddrPort{
		netip.MustParseAddrPort("10.0.0.1:8081"),
		netip.MustParseAddrPort("10.0.0.2:8081"),
		netip.MustParseAddrPort("10.0.0.10:8081"),
	}
	for index := range want {
		if candidates[index].Address != want[index] {
			t.Errorf("Candidates()[%d] = %s, want %s", index, candidates[index].Address, want[index])
		}
	}
	candidates[0].Healthy = true
	stored, _ := registry.Get(want[0])
	if stored.Healthy {
		t.Error("registry state was mutated through Candidates() result")
	}
}

func TestRegistryValidationAndUnknownCandidate(t *testing.T) {
	registry := New()
	validAddress := netip.MustParseAddrPort("10.0.0.1:8081")
	base := testTime()
	if _, err := registry.Upsert(validAddress, base); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	invalidUpserts := []struct {
		address netip.AddrPort
		at      time.Time
	}{
		{at: base},
		{address: netip.AddrPortFrom(netip.MustParseAddr("10.0.0.2"), 0), at: base},
		{address: validAddress},
	}
	for _, test := range invalidUpserts {
		if _, err := registry.Upsert(test.address, test.at); !errors.Is(err, ErrInvalidCandidate) {
			t.Errorf("Upsert(%s) error = %v, want ErrInvalidCandidate", test.address, err)
		}
	}

	invalidMetadata := []Metadata{
		{Weight: 1},
		{InstanceID: " pdu-1 ", Weight: 1},
		{InstanceID: "pdu-1"},
		{InstanceID: "pdu-1", Weight: 1, ActiveRequests: -1},
	}
	for _, metadata := range invalidMetadata {
		if err := registry.MarkHealthy(validAddress, metadata, base); !errors.Is(err, ErrInvalidCandidate) {
			t.Errorf("MarkHealthy(%+v) error = %v, want ErrInvalidCandidate", metadata, err)
		}
	}
	if err := registry.MarkHealthy(validAddress, Metadata{InstanceID: "pdu-1", Weight: 1}, time.Time{}); !errors.Is(err, ErrInvalidCandidate) {
		t.Errorf("MarkHealthy(zero time) error = %v, want ErrInvalidCandidate", err)
	}

	unknown := netip.MustParseAddrPort("10.0.0.99:8081")
	if err := registry.MarkHealthy(unknown, Metadata{InstanceID: "pdu-99", Weight: 1}, base); !errors.Is(err, ErrCandidateNotFound) {
		t.Errorf("MarkHealthy(unknown) error = %v, want ErrCandidateNotFound", err)
	}
	if err := registry.MarkUnhealthy(unknown, base); !errors.Is(err, ErrCandidateNotFound) {
		t.Errorf("MarkUnhealthy(unknown) error = %v, want ErrCandidateNotFound", err)
	}
}

func TestRegistryConcurrentReadWrite(t *testing.T) {
	registry := New()
	base := testTime()
	const candidateCount = 256
	errorsFound := make(chan error, candidateCount*2)

	var writers sync.WaitGroup
	writers.Add(candidateCount)
	for index := range candidateCount {
		go func() {
			defer writers.Done()
			address := testAddress(index)
			observedAt := base.Add(time.Duration(index+1) * time.Nanosecond)
			if _, err := registry.Upsert(address, observedAt); err != nil {
				errorsFound <- fmt.Errorf("upsert %s: %w", address, err)
				return
			}
			if err := registry.MarkHealthy(address, Metadata{
				InstanceID:     fmt.Sprintf("pdu-%03d", index),
				Weight:         index%3 + 1,
				ActiveRequests: int64(index % 11),
			}, observedAt.Add(time.Second)); err != nil {
				errorsFound <- fmt.Errorf("mark healthy %s: %w", address, err)
				return
			}
			if index%3 == 0 {
				registry.Remove(address)
			}
		}()
	}

	var readers sync.WaitGroup
	readers.Add(8)
	for range 8 {
		go func() {
			defer readers.Done()
			for range 500 {
				candidates := registry.Candidates()
				snapshot := registry.HealthySnapshot()
				if snapshot.Len() > 0 {
					_, _ = snapshot.At(snapshot.Len() - 1)
				}
				if len(candidates) > 0 {
					registry.Get(candidates[0].Address)
				}
			}
		}()
	}
	writers.Wait()
	readers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}

	wantRemaining := candidateCount - (candidateCount+2)/3
	if got := len(registry.Candidates()); got != wantRemaining {
		t.Errorf("remaining candidates = %d, want %d", got, wantRemaining)
	}
	if got := registry.HealthySnapshot().Len(); got != wantRemaining {
		t.Errorf("healthy snapshot length = %d, want %d", got, wantRemaining)
	}
}

func testAddress(index int) netip.AddrPort {
	address := netip.AddrFrom4([4]byte{10, byte(index >> 8), byte(index), 1})
	return netip.AddrPortFrom(address, 8081)
}

func testTime() time.Time {
	return time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
}
