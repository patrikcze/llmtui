package provider

import (
	"context"
	"testing"
)

type probeProvider struct {
	requests []ChatRequest
}

func (p *probeProvider) Name() string                                    { return "probe" }
func (p *probeProvider) ListModels(context.Context) ([]ModelInfo, error) { return nil, nil }
func (p *probeProvider) HealthCheck(context.Context) error               { return nil }
func (p *probeProvider) Capabilities() Capabilities {
	return Capabilities{NativeTools: CapabilitySupported}
}
func (p *probeProvider) Chat(_ context.Context, request ChatRequest) (<-chan ChatEvent, error) {
	p.requests = append(p.requests, request)
	events := make(chan ChatEvent, 1)
	if len(p.requests) == 1 {
		events <- ChatEvent{Type: EventDone, ToolCalls: []ToolCall{{ID: "probe-1", Name: "conformance_echo", Arguments: `{"probe_token":"llmtui-tool-conformance-v1"}`}}}
	} else {
		events <- ChatEvent{Type: EventDone}
	}
	close(events)
	return events, nil
}

func TestProbeNativeToolCallsNeverExecutesATool(t *testing.T) {
	p := &probeProvider{}
	report, err := ProbeNativeToolCalls(context.Background(), p, "test-model", true)
	if err != nil {
		t.Fatal(err)
	}
	if report.NativeCall != ConformancePass || report.Arguments != ConformancePass || report.ResultCorrelation != ConformancePass {
		t.Fatalf("report = %+v", report)
	}
	if report.RequiredSelection != ConformanceNotSupported {
		t.Fatalf("required selection = %q", report.RequiredSelection)
	}
	if len(p.requests) != 2 || len(p.requests[0].Tools) != 1 {
		t.Fatalf("requests = %+v", p.requests)
	}
	if got := p.requests[1].Messages[len(p.requests[1].Messages)-1]; got.Role != RoleTool || got.ToolCallID != "probe-1" {
		t.Fatalf("follow-up = %+v", got)
	}
}
