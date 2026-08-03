package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestDNSResolverNormalizesDeduplicatesAndSorts(t *testing.T) {
	firstOrder := []net.IPAddr{
		{IP: net.ParseIP("2001:db8::1")},
		{IP: net.ParseIP("10.0.0.10")},
		{IP: net.IPv4(10, 0, 0, 2)},
		{IP: net.ParseIP("10.0.0.2")},
		{IP: net.ParseIP("2001:db8::1")},
	}
	secondOrder := slices.Clone(firstOrder)
	slices.Reverse(secondOrder)
	var calls atomic.Int64
	lookup := lookupFunc(func(_ context.Context, hostname string) ([]net.IPAddr, error) {
		if hostname != "pdu-session" {
			t.Errorf("hostname = %q, want pdu-session", hostname)
		}
		if calls.Add(1) == 1 {
			return firstOrder, nil
		}
		return secondOrder, nil
	})
	resolver := newTestDNSResolver(t, lookup, time.Second)
	want := []netip.AddrPort{
		netip.MustParseAddrPort("10.0.0.2:8081"),
		netip.MustParseAddrPort("10.0.0.10:8081"),
		netip.MustParseAddrPort("[2001:db8::1]:8081"),
	}

	first, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("first Resolve() error = %v", err)
	}
	second, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if !slices.Equal(first, want) {
		t.Errorf("first Resolve() = %v, want %v", first, want)
	}
	if !slices.Equal(second, want) {
		t.Errorf("second Resolve() = %v, want deterministic %v", second, want)
	}
}

func TestDNSResolverPreservesIPv6Zone(t *testing.T) {
	resolver := newTestDNSResolver(t, lookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("fe80::1"), Zone: "eth0"}}, nil
	}), time.Second)

	addresses, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := netip.MustParseAddrPort("[fe80::1%eth0]:8081")
	if len(addresses) != 1 || addresses[0] != want {
		t.Errorf("Resolve() = %v, want [%s]", addresses, want)
	}
}

func TestDNSResolverReturnsLookupError(t *testing.T) {
	wantErr := errors.New("DNS server unavailable")
	resolver := newTestDNSResolver(t, lookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return nil, wantErr
	}), time.Second)

	_, err := resolver.Resolve(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Resolve() error = %v, want wrapped lookup error", err)
	}
}

func TestDNSResolverRejectsEmptyResult(t *testing.T) {
	resolver := newTestDNSResolver(t, lookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
		return nil, nil
	}), time.Second)

	_, err := resolver.Resolve(context.Background())
	if !errors.Is(err, ErrNoAddresses) {
		t.Fatalf("Resolve() error = %v, want ErrNoAddresses", err)
	}
}

func TestDNSResolverRejectsInvalidAddress(t *testing.T) {
	tests := []struct {
		name      string
		addresses []net.IPAddr
	}{
		{name: "empty IP", addresses: []net.IPAddr{{}}},
		{name: "IPv4 with zone", addresses: []net.IPAddr{{IP: net.IPv4(10, 0, 0, 1), Zone: "eth0"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := newTestDNSResolver(t, lookupFunc(func(context.Context, string) ([]net.IPAddr, error) {
				return test.addresses, nil
			}), time.Second)
			_, err := resolver.Resolve(context.Background())
			if !errors.Is(err, ErrInvalidAddress) {
				t.Fatalf("Resolve() error = %v, want ErrInvalidAddress", err)
			}
		})
	}
}

func TestDNSResolverAppliesLookupTimeout(t *testing.T) {
	deadlineSeen := make(chan bool, 1)
	lookup := lookupFunc(func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		_, present := ctx.Deadline()
		deadlineSeen <- present
		<-ctx.Done()
		return nil, ctx.Err()
	})
	resolver := newTestDNSResolver(t, lookup, 20*time.Millisecond)

	_, err := resolver.Resolve(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Resolve() error = %v, want context deadline exceeded", err)
	}
	if !<-deadlineSeen {
		t.Error("lookup context did not have a deadline")
	}
}

func TestDNSResolverHonorsParentCancellation(t *testing.T) {
	lookup := lookupFunc(func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	resolver := newTestDNSResolver(t, lookup, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := resolver.Resolve(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Resolve() error = %v, want context canceled", err)
	}
}

func TestNewDNSResolverValidation(t *testing.T) {
	validLookup := lookupFunc(func(context.Context, string) ([]net.IPAddr, error) { return nil, nil })
	tests := []struct {
		name     string
		lookup   IPAddressLookup
		hostname string
		port     int
		timeout  time.Duration
	}{
		{name: "nil lookup", hostname: "pdu-session", port: 8081, timeout: time.Second},
		{name: "empty hostname", lookup: validLookup, port: 8081, timeout: time.Second},
		{name: "hostname whitespace", lookup: validLookup, hostname: " pdu-session ", port: 8081, timeout: time.Second},
		{name: "zero port", lookup: validLookup, hostname: "pdu-session", timeout: time.Second},
		{name: "large port", lookup: validLookup, hostname: "pdu-session", port: 65536, timeout: time.Second},
		{name: "zero timeout", lookup: validLookup, hostname: "pdu-session", port: 8081},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewDNSResolver(test.lookup, test.hostname, test.port, test.timeout); err == nil {
				t.Fatal("NewDNSResolver() error = nil, want validation error")
			}
		})
	}
}

type lookupFunc func(context.Context, string) ([]net.IPAddr, error)

func (lookup lookupFunc) LookupIPAddr(ctx context.Context, hostname string) ([]net.IPAddr, error) {
	return lookup(ctx, hostname)
}

func newTestDNSResolver(t *testing.T, lookup IPAddressLookup, timeout time.Duration) *DNSResolver {
	t.Helper()
	resolver, err := NewDNSResolver(lookup, "pdu-session", 8081, timeout)
	if err != nil {
		t.Fatalf("NewDNSResolver() error = %v", err)
	}
	return resolver
}
