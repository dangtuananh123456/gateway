// Package discovery resolves PDU Session instances through DNS.
package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

var (
	// ErrNoAddresses indicates that a successful DNS response contains no IPs.
	ErrNoAddresses = errors.New("DNS lookup returned no addresses")
	// ErrInvalidAddress indicates that the lookup returned a malformed IP value.
	ErrInvalidAddress = errors.New("DNS lookup returned an invalid address")
)

// IPAddressLookup is implemented by net.Resolver and can be replaced in tests.
type IPAddressLookup interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

// Resolver supplies the current, normalized PDU candidate addresses.
type Resolver interface {
	Resolve(context.Context) ([]netip.AddrPort, error)
}

// DNSResolver resolves one service hostname and attaches the configured PDU port.
type DNSResolver struct {
	lookup   IPAddressLookup
	hostname string
	port     uint16
	timeout  time.Duration
}

// NewDNSResolver creates a context-aware resolver. Pass net.DefaultResolver in
// production and a fake IPAddressLookup in unit tests.
func NewDNSResolver(
	lookup IPAddressLookup,
	hostname string,
	port int,
	timeout time.Duration,
) (*DNSResolver, error) {
	if lookup == nil {
		return nil, errors.New("create DNS resolver: IP address lookup must not be nil")
	}
	if strings.TrimSpace(hostname) == "" || hostname != strings.TrimSpace(hostname) {
		return nil, errors.New("create DNS resolver: hostname must be non-empty without surrounding whitespace")
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("create DNS resolver: port must be between 1 and 65535")
	}
	if timeout <= 0 {
		return nil, errors.New("create DNS resolver: timeout must be greater than zero")
	}

	return &DNSResolver{
		lookup:   lookup,
		hostname: hostname,
		port:     uint16(port),
		timeout:  timeout,
	}, nil
}

// Resolve performs one bounded DNS lookup and returns a deterministic snapshot.
func (resolver *DNSResolver) Resolve(ctx context.Context) ([]netip.AddrPort, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, resolver.timeout)
	defer cancel()

	addresses, err := resolver.lookup.LookupIPAddr(lookupCtx, resolver.hostname)
	if err != nil {
		return nil, fmt.Errorf("resolve PDU hostname %q: %w", resolver.hostname, err)
	}
	if err := lookupCtx.Err(); err != nil {
		return nil, fmt.Errorf("resolve PDU hostname %q: %w", resolver.hostname, err)
	}

	normalized := make(map[netip.AddrPort]struct{}, len(addresses))
	for index, address := range addresses {
		IP, valid := netip.AddrFromSlice(address.IP)
		if !valid {
			return nil, fmt.Errorf("%w at result index %d", ErrInvalidAddress, index)
		}
		IP = IP.Unmap()
		if address.Zone != "" {
			if !IP.Is6() {
				return nil, fmt.Errorf("%w at result index %d: zone on non-IPv6 address", ErrInvalidAddress, index)
			}
			IP = IP.WithZone(address.Zone)
		}
		normalized[netip.AddrPortFrom(IP, resolver.port)] = struct{}{}
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("resolve PDU hostname %q: %w", resolver.hostname, ErrNoAddresses)
	}

	result := make([]netip.AddrPort, 0, len(normalized))
	for address := range normalized {
		result = append(result, address)
	}
	slices.SortFunc(result, func(first, second netip.AddrPort) int {
		return first.Compare(second)
	})
	return result, nil
}
