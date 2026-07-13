package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// feedDelta drives a delta event through the same handleStreamEvent path a
// live stream uses, so the ThinkFilter wiring is exercised end to end.
func feedDelta(t *testing.T, m *Model, delta string) {
	t.Helper()
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{Type: provider.EventDelta, Delta: delta}, ok: true})
}

// Leaked think block: content deltas containing <think>…</think> must not
// reach the visible answer or the stored session message.
func TestLeakedThinkBlockIsStrippedFromReplyAndHistory(t *testing.T) {
	m := newTestModel(t)
	m.resetThinkFilter()
	for _, d := range []string{"<think>because", " reasons</think>", "42"} {
		feedDelta(t, m, d)
	}
	m.finishStream(&provider.Usage{})
	msgs := m.session.Messages
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant || last.Content != "42" {
		t.Fatalf("stored assistant content = %q", last.Content)
	}
	if m.reasoningLen == 0 {
		t.Fatal("reasoning activity was not counted")
	}
}

// Unclosed think block: the reply must be salvaged, not dropped.
func TestUnclosedThinkBlockIsSalvaged(t *testing.T) {
	m := newTestModel(t)
	m.resetThinkFilter()
	feedDelta(t, m, "<think>ran out of tokens mid-thought")
	m.finishStream(&provider.Usage{})
	msgs := m.session.Messages
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "ran out of tokens mid-thought") {
		t.Fatalf("unclosed reasoning lost: %q", last.Content)
	}
}

// Filter disabled by config: content passes through verbatim.
func TestStripLeakedThinkingCanBeDisabled(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Chat.StripLeakedThinking = false
	m.resetThinkFilter()
	feedDelta(t, m, "<think>x</think>y")
	m.finishStream(&provider.Usage{})
	msgs := m.session.Messages
	if last := msgs[len(msgs)-1]; last.Content != "<think>x</think>y" {
		t.Fatalf("content = %q", last.Content)
	}
}
