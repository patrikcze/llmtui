package web

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
)

var errTooManyRedirects = errors.New("stopped after 5 redirects")

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

// blockedIP reports whether an address must never be dialed: loopback,
// private, link-local, unique-local, and unspecified ranges. Fetching those
// would let a hostile page probe the user's machine or LAN.
func blockedIP(ip net.IP) bool {
	return ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
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
		return nil, fmt.Errorf("host %s resolves to a private or local address — not allowed", host)
	}
}
