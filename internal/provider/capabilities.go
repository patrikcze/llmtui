package provider

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
)

// Capabilities describes what a backend supports, for /doctor and prompt
// composition decisions.
type Capabilities struct {
	SupportsStreaming    bool
	SupportsModelList    bool
	SupportsTokenUsage   bool
	SupportsJSONMode     bool
	SupportsSystemPrompt bool
	ContextWindowTokens  int // 0 = unknown; profiles/config provide fallback
}

// CapabilityReporter is implemented by providers that can describe
// themselves. Callers should fall back to DefaultCapabilities otherwise.
type CapabilityReporter interface {
	Capabilities() Capabilities
}

// DefaultCapabilities is the conservative assumption for unknown backends.
func DefaultCapabilities() Capabilities {
	return Capabilities{
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
	}
}

// CapabilitiesOf returns the provider's self-description or defaults.
func CapabilitiesOf(p Provider) Capabilities {
	if r, ok := p.(CapabilityReporter); ok {
		return r.Capabilities()
	}
	return DefaultCapabilities()
}

// RetryableError reports whether a request error is worth retrying:
// transient network problems, yes; user cancellation or HTTP-level
// failures (wrong model, bad request), no.
func RetryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := err.Error()
	for _, s := range []string{"connection refused", "connection reset", "EOF", "broken pipe", "no such host"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
