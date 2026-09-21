package mock

import (
	"context"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestChatStreamsAndReportsUsage(t *testing.T) {
	p := New()
	p.Delay = 0 // no pacing in tests

	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model:    "demo-model",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var (
		text  strings.Builder
		usage *provider.Usage
	)
	deltas := 0
	for ev := range events {
		switch ev.Type {
		case provider.EventDelta:
			deltas++
			text.WriteString(ev.Delta)
		case provider.EventDone:
			usage = ev.Usage
		case provider.EventError:
			t.Fatalf("unexpected error: %v", ev.Err)
		}
	}

	if deltas < 2 {
		t.Errorf("deltas = %d, want streaming in multiple chunks", deltas)
	}
	if !strings.Contains(text.String(), "demo") {
		t.Errorf("reply should mention demo mode, got %q", text.String())
	}
	if usage == nil || !usage.Estimated || usage.TotalTokens == 0 {
		t.Errorf("usage = %+v, want estimated non-zero usage", usage)
	}
}

func TestChatRespectsCancellation(t *testing.T) {
	p := New() // real delay so cancellation lands mid-stream

	ctx, cancel := context.WithCancel(context.Background())
	events, err := p.Chat(ctx, provider.ChatRequest{Model: "demo-model"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	cancel()

	sawError := false
	for ev := range events {
		if ev.Type == provider.EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Error("expected an error event after cancellation")
	}
}

func TestListModels(t *testing.T) {
	models, err := New().ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) == 0 {
		t.Error("mock should list at least one model")
	}
}

func TestReplyListsOfferedTools(t *testing.T) {
	p := New()
	p.Delay = 0
	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model:    "demo-model",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		Tools:    []provider.ToolSpec{{Name: "list_dir", Description: "List a directory.\nSecond line."}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	var text strings.Builder
	for ev := range events {
		text.WriteString(ev.Delta)
	}
	got := text.String()
	if !strings.Contains(got, "`list_dir` — List a directory.") || strings.Contains(got, "Second line") {
		t.Errorf("reply should list tool name and first description line, got:\n%s", got)
	}
}

func TestContextWindowsAreConsistent(t *testing.T) {
	p := New()
	models, _ := p.ListModels(context.Background())
	for _, m := range models {
		if m.ContextLen < 16384 {
			t.Errorf("%s ContextLen = %d; too small for tool schemas plus the default response reserve", m.ID, m.ContextLen)
		}
	}
	if got := p.Capabilities().ContextWindowTokens; got != models[0].ContextLen {
		t.Errorf("capabilities window %d != demo-model ContextLen %d", got, models[0].ContextLen)
	}
	if p.Capabilities().NativeTools == provider.CapabilityUnsupported {
		t.Error("demo should keep native tools so it exercises the real request path")
	}
}
