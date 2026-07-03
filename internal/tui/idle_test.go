package tui

// Regression tests for the streaming inactivity timeout: a slow-but-steady
// stream must run to completion (no whole-request cutoff), while a genuinely
// stalled stream must be reported as an idle stall, keeping partial output.

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/provider"
)

// runStream executes the tea.Cmd chain that dispatch/handleStreamEvent
// produce, driving the real event loop until the stream finalizes.
func runStream(t *testing.T, m *Model, first tea.Cmd) {
	t.Helper()
	cmd := first
	deadline := time.Now().Add(10 * time.Second)
	for cmd != nil {
		if time.Now().After(deadline) {
			t.Fatal("stream did not finalize in time")
		}
		msg := cmd()
		_, cmd = m.Update(msg)
		if !m.thinking {
			return
		}
	}
}

// A model that keeps emitting tokens faster than the idle window must finish,
// even though the whole reply takes longer than that window — proving the
// timeout is per-token inactivity, not a whole-request deadline.
func TestSlowSteadyStreamCompletes(t *testing.T) {
	m := newTestModel(t)
	// Idle window 200ms; tokens every ~5ms. The full demo reply takes far
	// longer than 200ms in aggregate, so a whole-request timeout would kill it.
	m.cfg.Network.Timeout = "200ms"
	m.prov = &pacedProvider{gap: 5 * time.Millisecond, chunks: 60}

	runStream(t, m, m.dispatch("hello", nil))

	if m.errText != "" {
		t.Fatalf("healthy slow stream reported an error: %q", m.errText)
	}
	if last := lastAssistant(m); !strings.Contains(last, "chunk-59") {
		t.Fatalf("stream truncated; last assistant message = %q", last)
	}
}

// A stream that stops sending tokens must trip the idle watchdog and be
// reported as a stall, with any partial output preserved.
func TestStalledStreamReportsIdle(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Network.Timeout = "60ms"
	// Emits two chunks, then blocks until the context is canceled.
	m.prov = &pacedProvider{gap: 5 * time.Millisecond, chunks: 2, stallAfter: true}

	runStream(t, m, m.dispatch("hello", nil))

	if !strings.Contains(m.errText, "stuck") && !strings.Contains(m.errText, "no response") {
		t.Fatalf("stalled stream not reported as idle: %q", m.errText)
	}
	if !strings.Contains(m.errText, "partial reply kept") {
		t.Errorf("partial output should be preserved on idle stall: %q", m.errText)
	}
	if last := lastAssistant(m); !strings.Contains(last, "chunk-0") {
		t.Errorf("partial chunks lost: %q", last)
	}
}

// The idle classification helper distinguishes a watchdog cancel from a user
// Esc (both cancel the same context).
func TestIdleClassification(t *testing.T) {
	m := newTestModel(t)

	userCtx, userCancel := context.WithCancelCause(context.Background())
	m.streamCtx = userCtx
	userCancel(context.Canceled)
	if m.streamCanceledByIdle() {
		t.Error("user cancel misclassified as idle timeout")
	}

	idleCtx, idleCancel := context.WithCancelCause(context.Background())
	m.streamCtx = idleCtx
	idleCancel(errStreamIdle)
	if !m.streamCanceledByIdle() {
		t.Error("idle cancel not recognized")
	}
}

func lastAssistant(m *Model) string {
	for i := len(m.session.Messages) - 1; i >= 0; i-- {
		if m.session.Messages[i].Role == provider.RoleAssistant {
			return m.session.Messages[i].Content
		}
	}
	return ""
}

// A reasoning model that "thinks" for longer than the idle window before
// producing any visible content must not be timed out — reasoning activity
// keeps the stream alive. This reproduces the LM Studio reasoning-model hang.
func TestReasoningKeepsStreamAlive(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Network.Timeout = "150ms"
	// 40 reasoning chunks at 5ms (~200ms of pure thinking, well past the
	// 150ms window) followed by visible content.
	m.prov = &pacedProvider{gap: 5 * time.Millisecond, chunks: 5, reasoningChunks: 40}

	runStream(t, m, m.dispatch("explain", nil))

	if m.errText != "" {
		t.Fatalf("reasoning model timed out during thinking: %q", m.errText)
	}
	if last := lastAssistant(m); !strings.Contains(last, "chunk-4") {
		t.Fatalf("visible answer missing after reasoning: %q", last)
	}
}

// pacedProvider streams reasoningChunks reasoning events, then chunks visible
// "chunk-N " tokens with a gap between each, optionally stalling after.
type pacedProvider struct {
	gap             time.Duration
	chunks          int
	reasoningChunks int
	stallAfter      bool
}

func (p *pacedProvider) Name() string { return "paced" }

func (p *pacedProvider) HealthCheck(context.Context) error { return nil }

func (p *pacedProvider) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}

func (p *pacedProvider) Chat(ctx context.Context, _ provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	events := make(chan provider.ChatEvent)
	go func() {
		defer close(events)
		for i := 0; i < p.reasoningChunks; i++ {
			select {
			case <-ctx.Done():
				provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
				return
			case <-time.After(p.gap):
			}
			if !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventReasoning, Delta: "think "}) {
				return
			}
		}
		for i := 0; i < p.chunks; i++ {
			select {
			case <-ctx.Done():
				provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
				return
			case <-time.After(p.gap):
			}
			if !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: "chunk-" + itoa(i) + " "}) {
				return
			}
		}
		if p.stallAfter {
			<-ctx.Done() // never sends another token
			provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
			return
		}
		provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{
			PromptTokens: 1, CompletionTokens: p.chunks, TotalTokens: p.chunks + 1, Estimated: true,
		}})
	}()
	return events, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
