package provider

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"
)

type capabilityTestProvider struct {
	caps   Capabilities
	closed bool
}

func (p *capabilityTestProvider) Name() string { return "capability-test" }

func (p *capabilityTestProvider) ListModels(context.Context) ([]ModelInfo, error) {
	return []ModelInfo{}, nil
}

func (p *capabilityTestProvider) Chat(context.Context, ChatRequest) (<-chan ChatEvent, error) {
	events := make(chan ChatEvent)
	close(events)
	return events, nil
}

func (p *capabilityTestProvider) HealthCheck(context.Context) error { return nil }
func (p *capabilityTestProvider) Capabilities() Capabilities        { return p.caps }
func (p *capabilityTestProvider) RuntimeFingerprint() string        { return "runtime-id" }
func (p *capabilityTestProvider) Close() error {
	p.closed = true
	return nil
}

func TestDefaultCapabilities(t *testing.T) {
	c := DefaultCapabilities()
	if !c.SupportsStreaming || !c.SupportsSystemPrompt {
		t.Errorf("defaults = %+v, want streaming + system prompt", c)
	}
	if c.SupportsModelList || c.SupportsTokenUsage || c.SupportsJSONMode {
		t.Errorf("defaults = %+v, should be conservative about optional features", c)
	}
	if c.NativeTools != CapabilityUnknown || c.ParallelToolCalls != CapabilityUnknown ||
		c.ReasoningEvents != CapabilityUnknown || c.StructuredOutput != CapabilityUnknown {
		t.Errorf("defaults = %+v, model-dependent capabilities should be unknown", c)
	}
}

func TestCapabilityOverridesAreTriStateAndPreserveOptionalInterfaces(t *testing.T) {
	base := &capabilityTestProvider{caps: Capabilities{
		SupportsStreaming: true,
		NativeTools:       CapabilityUnknown,
		ReasoningEvents:   CapabilitySupported,
	}}
	unsupported := false
	supported := true
	contextWindow := 32768
	wrapped := WithCapabilityOverrides(base, CapabilityOverrides{
		NativeTools:         &unsupported,
		ParallelToolCalls:   &supported,
		ContextWindowTokens: &contextWindow,
	})

	caps := CapabilitiesFor(wrapped, "model-a")
	if caps.NativeTools != CapabilityUnsupported {
		t.Errorf("NativeTools = %s, want unsupported", caps.NativeTools)
	}
	if caps.ParallelToolCalls != CapabilitySupported {
		t.Errorf("ParallelToolCalls = %s, want supported", caps.ParallelToolCalls)
	}
	if caps.ReasoningEvents != CapabilitySupported {
		t.Errorf("ReasoningEvents = %s, want provider-reported supported", caps.ReasoningEvents)
	}
	if caps.ContextWindowTokens != contextWindow {
		t.Errorf("ContextWindowTokens = %d, want %d", caps.ContextWindowTokens, contextWindow)
	}
	if got := RuntimeFingerprintOf(wrapped); got != "runtime-id" {
		t.Errorf("RuntimeFingerprintOf = %q, want runtime-id", got)
	}
	if err := CloseProvider(wrapped); err != nil {
		t.Fatalf("CloseProvider: %v", err)
	}
	if !base.closed {
		t.Fatal("capability wrapper did not preserve Close")
	}
}

func TestRetryableError(t *testing.T) {
	retryable := []error{
		syscall.ECONNREFUSED,
		syscall.ECONNRESET,
		context.DeadlineExceeded,
		fmt.Errorf("dial tcp: connection refused"),
		fmt.Errorf("unexpected EOF"),
	}
	for _, err := range retryable {
		if !RetryableError(err) {
			t.Errorf("RetryableError(%v) = false, want true", err)
		}
	}

	notRetryable := []error{
		nil,
		context.Canceled,
		errors.New("chat request: status 404: model not found"),
		errors.New("invalid request"),
	}
	for _, err := range notRetryable {
		if RetryableError(err) {
			t.Errorf("RetryableError(%v) = true, want false", err)
		}
	}
}
