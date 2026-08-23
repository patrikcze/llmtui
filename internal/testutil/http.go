// Package testutil provides hermetic test-only infrastructure shared by
// internal packages.
package testutil

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// NewHTTPServer starts an IPv4 loopback server. httptest.NewServer tries IPv6
// first and panics when IPv6 is unavailable; this helper has an explicit
// listener seam and cleanly skips in sandboxes that forbid all listeners.
func NewHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listeners unavailable in this test environment: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

// SkipIfListenerUnavailable skips only when an in-process listener could not
// be created. Other startup failures remain real test failures.
func SkipIfListenerUnavailable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		t.Skipf("loopback listeners unavailable in this test environment: %v", err)
	}
}
