// Package netguard rejects outbound connections to addresses that must never be
// reachable from the ad-serving path: loopback, private, link-local and
// multicast ranges.
package netguard

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

// ErrBlockedAddress is returned when a dial target resolves into a range the
// sandbox forbids.
var ErrBlockedAddress = fmt.Errorf("outbound address is not routable from the sandbox")

// IsBlockedIP reports whether ip is loopback, private, link-local, multicast,
// unspecified or otherwise reserved for local traffic.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// Carrier-grade NAT and the "this network" range.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		if v4[0] == 0 {
			return true
		}
	}
	return false
}

// IsBlockedHost reports whether a dial host is refused without a DNS lookup.
func IsBlockedHost(host string) bool {
	trimmed := strings.ToLower(strings.Trim(host, "[]"))
	if trimmed == "" || trimmed == "localhost" || strings.HasSuffix(trimmed, ".localhost") ||
		strings.HasSuffix(trimmed, ".local") || strings.HasSuffix(trimmed, ".internal") {
		return true
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return IsBlockedIP(ip)
	}
	return false
}

// DialContext resolves the target and refuses any address in a blocked range
// before the connection is attempted. When allowPrivate is true the guard is
// disabled so a sandbox can reach services on a private container network.
func DialContext(timeout time.Duration, allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if allowPrivate {
			return dialer.DialContext(ctx, network, addr)
		}
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if IsBlockedHost(host) {
			return nil, fmt.Errorf("%w: %s", ErrBlockedAddress, host)
		}
		if net.ParseIP(host) == nil {
			addresses, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, host)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if len(addresses) == 0 {
				return nil, fmt.Errorf("%w: %s has no address", ErrBlockedAddress, host)
			}
			for _, address := range addresses {
				if IsBlockedIP(address.IP) {
					return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, address.IP)
				}
			}
		}
		return dialer.DialContext(ctx, network, addr)
	}
}
