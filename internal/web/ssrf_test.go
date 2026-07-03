package web

import (
	"context"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBlockedIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "0.0.0.0", "fd00::1", "fe80::1"}
	for _, s := range blocked {
		if !blockedIP(net.ParseIP(s)) {
			t.Errorf("blockedIP(%s) = false, want true", s)
		}
	}
	allowed := []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "8.8.8.8"}
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
	srv := httptest.NewServer(nil)
	defer srv.Close()
	c := NewClient(2*time.Second, 64)
	_, err := c.Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected private-address rejection, got %v", err)
	}
}
