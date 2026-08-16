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
	m.finishStream(&provider.Usage{}, false)
	msgs := m.session.Messages
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant || last.Content != "42" {
		t.Fatalf("stored assistant content = %q", last.Content)
	}
	if m.reasoningLen == 0 {
		t.Fatal("reasoning activity was not counted")
	}
	if last.Reasoning != "because reasons" {
		t.Fatalf("captured reasoning = %q", last.Reasoning)
	}
}

// Unclosed think block: the reply must be salvaged, not dropped.
func TestUnclosedThinkBlockIsSalvaged(t *testing.T) {
	m := newTestModel(t)
	m.resetThinkFilter()
	feedDelta(t, m, "<think>ran out of tokens mid-thought")
	m.finishStream(&provider.Usage{}, false)
	msgs := m.session.Messages
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "ran out of tokens mid-thought") {
		t.Fatalf("unclosed reasoning lost: %q", last.Content)
	}
	if last.Reasoning != "" {
		t.Fatalf("salvaged reasoning duplicated into hidden UI field: %q", last.Reasoning)
	}
}

func TestDedicatedReasoningEventIsCapturedSeparately(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:  provider.EventReasoning,
		Delta: "inspect inputs\x1b[2J",
	}, ok: true, gen: m.streamGen})
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:  provider.EventDelta,
		Delta: "final answer",
	}, ok: true, gen: m.streamGen})
	m.finishStream(&provider.Usage{}, false)

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Reasoning != "inspect inputs" {
		t.Fatalf("reasoning = %q, want sanitized separate reasoning", last.Reasoning)
	}
	if last.Content != "final answer" {
		t.Fatalf("answer = %q", last.Content)
	}
}

func TestProviderProgressIsNotCapturedAsReasoning(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:  provider.EventProgress,
		Delta: "loading local model",
	}, ok: true, gen: m.streamGen})
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:  provider.EventDelta,
		Delta: "ready",
	}, ok: true, gen: m.streamGen})
	m.finishStream(&provider.Usage{}, false)

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Reasoning != "" {
		t.Fatalf("provider progress captured as reasoning: %q", last.Reasoning)
	}
	if last.Content != "ready" {
		t.Fatalf("answer = %q", last.Content)
	}
}

func TestConversationCardsAndReasoningVisibility(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.session.AddUser("How does this work?")
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "It works carefully.", Reasoning: "private scratch work",
	})
	m.refreshViewport()

	view := m.viewport.View()
	for _, want := range []string{"╭", "you", "thought", "assistant", "private scratch work", "It works carefully."} {
		if !strings.Contains(view, want) {
			t.Errorf("conversation view missing %q:\n%s", want, view)
		}
	}

	m.showReasoning = false
	m.refreshViewport()
	view = m.viewport.View()
	if strings.Contains(view, "private scratch work") {
		t.Fatalf("hidden reasoning remained visible:\n%s", view)
	}
	if !strings.Contains(view, "reasoning hidden") || !strings.Contains(view, "/thoughts show") {
		t.Fatalf("hidden reasoning card lacks recovery hint:\n%s", view)
	}
}

// Filter disabled by config: content passes through verbatim.
func TestStripLeakedThinkingCanBeDisabled(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Chat.StripLeakedThinking = false
	m.resetThinkFilter()
	feedDelta(t, m, "<think>x</think>y")
	m.finishStream(&provider.Usage{}, false)
	msgs := m.session.Messages
	if last := msgs[len(msgs)-1]; last.Content != "<think>x</think>y" {
		t.Fatalf("content = %q", last.Content)
	}
}

func TestEffectiveReasoningPrecedence(t *testing.T) {
	m := newTestModel(t)
	if got := m.effectiveReasoning(); got != "auto" {
		t.Fatalf("default = %q, want auto", got)
	}
	m.cfg.Chat.Reasoning = "off"
	if got := m.effectiveReasoning(); got != "off" {
		t.Fatalf("config = %q, want off", got)
	}
	m.reasoningMode = "on" // session override wins
	if got := m.effectiveReasoning(); got != "on" {
		t.Fatalf("override = %q, want on", got)
	}
	m.cfg.Chat.Reasoning = "bogus"
	m.reasoningMode = ""
	if got := m.effectiveReasoning(); got != "auto" {
		t.Fatalf("invalid config value = %q, want auto", got)
	}
}

func TestBuildRequestCarriesReasoning(t *testing.T) {
	m := newTestModel(t)
	m.reasoningMode = "off"
	req := m.buildRequest(nil)
	if req.Reasoning != "off" {
		t.Fatalf("req.Reasoning = %q", req.Reasoning)
	}
	m.reasoningMode = "auto"
	if req := m.buildRequest(nil); req.Reasoning != "" {
		t.Fatalf("auto must map to empty, got %q", req.Reasoning)
	}
}
