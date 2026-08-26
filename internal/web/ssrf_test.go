package web

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/testutil"
)

func TestBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "fd00::1", "fe80::1",
		// Reserved ranges the stdlib predicates do not cover. Each one is a
		// real place an SSRF target can live: CGNAT/tailnet overlays, the
		// IETF protocol block, benchmarking space, class-E, and broadcast.
		"100.64.1.1", "100.127.255.254", "192.0.0.1", "198.18.0.1",
		"198.19.255.254", "240.0.0.1", "255.255.255.255",
		// Multicast, v4 and v6.
		"224.0.0.1", "239.255.255.250", "ff02::1",
		// IPv6 discard-only and benchmarking prefixes.
		"100::1", "2001:2::1",
	}
	for _, s := range blocked {
		if !blockedIP(net.ParseIP(s)) {
			t.Errorf("blockedIP(%s) = false, want true", s)
		}
	}
	allowed := []string{
		"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "8.8.8.8",
		// Neighbours of the new ranges that must stay reachable.
		"100.63.255.255", "100.128.0.1", "192.0.1.1", "198.17.255.255",
		"198.20.0.1", "223.255.255.255",
	}
	for _, s := range allowed {
		if blockedIP(net.ParseIP(s)) {
			t.Errorf("blockedIP(%s) = true, want false", s)
		}
	}
}

func TestFetchRejectsSchemesAndPrivateHosts(t *testing.T) {
	c := NewClient(2*time.Second, 64)
	for _, u := range []string{"ftp://example.com/x", "file:///etc/passwd", "http://localhost/x", "http://127.0.0.1:8080/x"} {
		if _, err := c.Fetch(context.Background(), u); err == nil {
			t.Errorf("Fetch(%s) succeeded, want error", u)
		}
	}
}

func TestFetchBlocksLoopbackServer(t *testing.T) {
	srv := testutil.NewHTTPServer(t, nil)
	defer srv.Close()
	c := NewClient(2*time.Second, 64)
	_, err := c.Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected private-address rejection, got %v", err)
	}
}
