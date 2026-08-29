package provider

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
)

// CapabilitySupport preserves the distinction between an explicitly
// unsupported feature and one the backend/model has not advertised. Unknown
// native-tool support intentionally keeps the existing optimistic one-shot
// attempt; treating unknown as false would silently disable tools on generic
// OpenAI-compatible servers that do support them.
type CapabilitySupport uint8

const (
	CapabilityUnknown CapabilitySupport = iota
	CapabilityUnsupported
	CapabilitySupported
)

func (s CapabilitySupport) String() string {
	switch s {
	case CapabilityUnsupported:
		return "unsupported"
	case CapabilitySupported:
		return "supported"
	default:
		return "unknown"
	}
}

// Capabilities describes what a backend supports, for /doctor and prompt
// composition decisions.
type Capabilities struct {
	SupportsStreaming     bool
	SupportsModelList     bool
	SupportsTokenUsage    bool
	SupportsJSONMode      bool
	SupportsSystemPrompt  bool
	ModelFamily           ModelFamily
	TemplateOwnership     TemplateOwnership
	HarmonyProtocol       CapabilitySupport
	ReasoningContinuation CapabilitySupport
	StructuredReasoning   CapabilitySupport
	StreamingReasoning    CapabilitySupport
	NativeTools           CapabilitySupport
	ParallelToolCalls     CapabilitySupport
	ReasoningEvents       CapabilitySupport
	StructuredOutput      CapabilitySupport
	ContextWindowTokens   int // 0 = unknown; profiles/config provide fallback
}

// CapabilityReporter is implemented by providers that can describe
// themselves. Callers should fall back to DefaultCapabilities otherwise.
type CapabilityReporter interface {
	Capabilities() Capabilities
}

// ModelCapabilityReporter reports selected-model capabilities when support
// depends on model metadata or configuration rather than only the transport.
type ModelCapabilityReporter interface {
	CapabilitiesFor(model string) Capabilities
}

// CapabilityOverrides contains user-authoritative provider capability
// settings. Nil leaves the provider/model report unchanged; pointer booleans
// preserve an explicit false from YAML.
type CapabilityOverrides struct {
	NativeTools         *bool
	ParallelToolCalls   *bool
	ReasoningEvents     *bool
	StructuredOutput    *bool
	ContextWindowTokens *int
}

// DefaultCapabilities is the conservative assumption for unknown backends.
func DefaultCapabilities() Capabilities {
	return Capabilities{
		SupportsStreaming:    true,
		SupportsSystemPrompt: true,
	}
}

// CapabilitiesFor returns the selected-model report when available, then the
// provider-wide report, then conservative defaults.
func CapabilitiesFor(p Provider, model string) Capabilities {
	if r, ok := p.(ModelCapabilityReporter); ok {
		return r.CapabilitiesFor(model)
	}
	if r, ok := p.(CapabilityReporter); ok {
		return r.Capabilities()
	}
	return DefaultCapabilities()
}

// WithCapabilityOverrides applies explicit configuration without discarding
// optional provider behavior such as Close or runtime fingerprinting.
func WithCapabilityOverrides(p Provider, overrides CapabilityOverrides) Provider {
	if p == nil || overrides.empty() {
		return p
	}
	return &capabilityOverrideProvider{Provider: p, overrides: overrides}
}

func (o CapabilityOverrides) empty() bool {
	return o.NativeTools == nil && o.ParallelToolCalls == nil &&
		o.ReasoningEvents == nil && o.StructuredOutput == nil &&
		o.ContextWindowTokens == nil
}

type capabilityOverrideProvider struct {
	Provider
	overrides CapabilityOverrides
}

func (p *capabilityOverrideProvider) Capabilities() Capabilities {
	return p.CapabilitiesFor("")
}

func (p *capabilityOverrideProvider) CapabilitiesFor(model string) Capabilities {
	caps := CapabilitiesFor(p.Provider, model)
	applySupportOverride(&caps.NativeTools, p.overrides.NativeTools)
	applySupportOverride(&caps.ParallelToolCalls, p.overrides.ParallelToolCalls)
	applySupportOverride(&caps.ReasoningEvents, p.overrides.ReasoningEvents)
	applySupportOverride(&caps.StructuredOutput, p.overrides.StructuredOutput)
	if p.overrides.ContextWindowTokens != nil {
		caps.ContextWindowTokens = *p.overrides.ContextWindowTokens
	}
	return caps
}

func (p *capabilityOverrideProvider) RuntimeFingerprint() string {
	return RuntimeFingerprintOf(p.Provider)
}

func (p *capabilityOverrideProvider) Close() error {
	return CloseProvider(p.Provider)
}

func applySupportOverride(dst *CapabilitySupport, value *bool) {
	if value == nil {
		return
	}
	*dst = CapabilityUnsupported
	if *value {
		*dst = CapabilitySupported
	}
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
