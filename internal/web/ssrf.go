package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
)

var errTooManyRedirects = errors.New("stopped after 5 redirects")

// errBlockedAddress is a deterministic guardrail rejection: the host resolved
// only to addresses that must never be dialed. Retrying it over HTTP/1.1 would
// fail identically, so the fetch fallback skips it.
var errBlockedAddress = errors.New("host resolves to a private or local address — not allowed")

// checkURL admits only plain web URLs.
func checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q is not allowed (http/https only)", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("URL has no host")
	}
	return nil
}

// reservedNets are ranges the stdlib predicates below do not cover but that
// must never be dialed either: carrier-grade NAT (in practice the range where
// tailnet/VPN-internal services live), IETF protocol assignments, benchmarking,
// the reserved class-E space (which includes the broadcast address), and the
// IPv6 discard-only prefix.
var reservedNets = mustParseCIDRs(
	"100.64.0.0/10", // RFC 6598 carrier-grade NAT / tailnet-style overlays
	"192.0.0.0/24",  // RFC 6890 IETF protocol assignments
	"198.18.0.0/15", // RFC 2544 benchmarking
	"240.0.0.0/4",   // RFC 1112 reserved, includes 255.255.255.255
	"100::/64",      // RFC 6666 IPv6 discard-only
	"2001:2::/48",   // RFC 5180 IPv6 benchmarking
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("web: invalid reserved CIDR " + cidr + ": " + err.Error())
		}
		out = append(out, network)
	}
	return out
}

// blockedIP reports whether an address must never be dialed: loopback,
// private, link-local, unique-local, multicast, unspecified, and the reserved
// ranges above. Fetching those would let a hostile page probe the user's
// machine, LAN, or overlay network.
func blockedIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, network := range reservedNets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// guardedDial resolves the host itself and dials a vetted IP directly, so a
// DNS answer cannot change between check and connect (rebinding).
func (c *Client) guardedDial(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		for _, a := range addrs {
			if c.allowPrivate || !blockedIP(a.IP) {
				return d.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
			}
		}
		return nil, fmt.Errorf("resolve %s: %w", host, errBlockedAddress)
	}
}
