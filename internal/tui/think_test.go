package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/modelprofile"
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

func TestPromptRailAndReasoningVisibility(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.session.AddUser("How does this work?")
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "It works carefully.", Reasoning: "private scratch work",
	})
	m.refreshViewport()

	// lipgloss v2 no longer auto-detects a non-tty test process and drops
	// color codes the way v1 did; strip them so a literal substring check
	// isn't broken by a style boundary landing mid-string (e.g. between the
	// "┃ " rail and the message body, each its own Render() call).
	view := ansi.Strip(m.viewport.View())
	for _, want := range []string{"┃ How does this work?", "Thought", "private scratch work", "It works carefully."} {
		if !strings.Contains(view, want) {
			t.Errorf("conversation view missing %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"╭", "╰", "you", "assistant"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("conversation view contains removed card/role marker %q:\n%s", unwanted, view)
		}
	}
	border, top, right, bottom, left := m.theme.PromptRail.GetBorder()
	if border.Left != "┃" || top || right || bottom || !left {
		t.Fatalf("prompt border = %+v sides=%v/%v/%v/%v, want heavy left rail only", border, top, right, bottom, left)
	}
	if m.theme.ReasoningText.GetForeground() != m.theme.Subtle {
		t.Fatal("reasoning text should use the muted gray theme color")
	}
	if m.theme.AnswerText.GetForeground() != m.theme.Text {
		t.Fatal("answer text should use the normal foreground color")
	}

	m.showReasoning = false
	m.refreshViewport()
	view = m.viewport.View()
	if strings.Contains(view, "private scratch work") {
		t.Fatalf("hidden reasoning remained visible:\n%s", view)
	}
	if !strings.Contains(view, "+ Thought · /thoughts show") {
		t.Fatalf("hidden reasoning lacks recovery hint:\n%s", view)
	}
}

func TestReasoningHeaderShowsDurationWhenKnown(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "answer text",
		Reasoning: "because X", ReasoningDuration: 4400 * time.Millisecond,
	})

	m.showReasoning = false
	m.refreshViewport()
	view := m.viewport.View()
	if !strings.Contains(view, "+ Thought: 4.4s") {
		t.Fatalf("collapsed header missing duration:\n%s", view)
	}
	if strings.Contains(view, "because X") {
		t.Fatalf("collapsed view must not leak the reasoning body:\n%s", view)
	}

	m.showReasoning = true
	m.refreshViewport()
	view = m.viewport.View()
	if !strings.Contains(view, "- Thought: 4.4s") {
		t.Fatalf("expanded header missing duration:\n%s", view)
	}
	if !strings.Contains(view, "because X") {
		t.Fatalf("expanded view missing reasoning body:\n%s", view)
	}
}

func TestReasoningHeaderTicksWhileStreaming(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.showReasoning = true
	m.thinking = true
	m.reasoningStart = time.Now().Add(-3 * time.Second)
	m.reasoningBuf.WriteString("still working on it")
	m.refreshViewport()

	view := m.viewport.View()
	if !strings.Contains(view, "- Thought: 3.") || !strings.Contains(view, "s…") {
		t.Fatalf("streaming header missing live ticking duration with ellipsis:\n%s", view)
	}
}

func TestAnswerForegroundPreservesMarkdownRendering(t *testing.T) {
	m := newTestModel(t)
	m.cfg.UI.Markdown = true
	// Changing width rebuilds the Glamour renderer after Markdown is enabled.
	m.resize(100, 40)
	m.session.AddAssistant("## Weather Summary\n\n| City | Forecast |\n| --- | --- |\n| **Brno** | Rain |\n\n```go\nfmt.Println(\"ok\")\n```")
	m.refreshViewport()

	view := ansi.Strip(m.viewport.View())
	for _, want := range []string{"Weather Summary", "City", "Forecast", "Brno", "fmt.Println(\"ok\")"} {
		if !strings.Contains(view, want) {
			t.Fatalf("rendered Markdown is missing %q:\n%s", want, view)
		}
	}
	for _, sourceMarker := range []string{"**Brno**", "```go"} {
		if strings.Contains(view, sourceMarker) {
			t.Fatalf("Markdown source marker %q was not rendered:\n%s", sourceMarker, view)
		}
	}
	if !strings.Contains(view, "│") {
		t.Fatalf("Markdown table was not laid out as a table:\n%s", view)
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

// TestAutoReasoningUsesProfileHintForEmbeddedProvider verifies that "auto"
// resolves enable_thinking to on for the embedded provider when the active
// model profile's ReasoningHint is set. Gemma 4's real GGUF chat template
// only injects its thinking token when "enable_thinking is defined and
// enable_thinking" — an omitted variable (the previous "auto" behavior) is
// indistinguishable from an explicit off for that template, which is what
// made "auto" silently behave as "always off" for this model.
func TestAutoReasoningUsesProfileHintForEmbeddedProvider(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Providers = map[string]config.ProviderConfig{
		"embedded_gemma4": {Type: "embedded"},
	}
	m.cfg.Provider = "embedded_gemma4"
	m.profiles = []modelprofile.Profile{{
		Name:          "gemma4-e4b",
		Match:         []string{"gemma-4-e4b"},
		ReasoningHint: true,
	}}
	m.model = "google/gemma-4-e4b-it"
	m.reasoningMode = "auto"

	if req := m.buildRequest(nil); req.Reasoning != "on" {
		t.Fatalf("embedded provider auto reasoning = %q, want on", req.Reasoning)
	}
}

// TestAutoReasoningLeavesRemoteProvidersAlone confirms the profile-hint
// promotion is scoped to the embedded provider only. A remote host (LM
// Studio, Ollama, any OpenAI-compatible server) applies its own chat
// template and may already have its own independent default (e.g. LM
// Studio's own "Enable Thinking" toggle) — llmtui must keep omitting
// enable_thinking for "auto" there rather than overriding a host default it
// does not control.
func TestAutoReasoningLeavesRemoteProvidersAlone(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Providers = map[string]config.ProviderConfig{
		"lmstudio": {Type: "openai_compatible"},
	}
	m.cfg.Provider = "lmstudio"
	m.profiles = []modelprofile.Profile{{
		Name:          "gemma4-e4b",
		Match:         []string{"gemma-4-e4b"},
		ReasoningHint: true,
	}}
	m.model = "google/gemma-4-e4b-it"
	m.reasoningMode = "auto"

	if req := m.buildRequest(nil); req.Reasoning != "" {
		t.Fatalf("remote provider auto reasoning = %q, want empty (omitted)", req.Reasoning)
	}
}

func TestReasoningDurationCapturedOnFinish(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventReasoning, Delta: "thinking...",
	}, ok: true, gen: m.streamGen})
	time.Sleep(5 * time.Millisecond)
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventDelta, Delta: "final answer",
	}, ok: true, gen: m.streamGen})
	m.finishStream(&provider.Usage{}, false)

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.ReasoningDuration < 5*time.Millisecond {
		t.Fatalf("ReasoningDuration = %v, want >= 5ms", last.ReasoningDuration)
	}
}

func TestReasoningDurationResetsBetweenTurns(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventReasoning, Delta: "first turn thinking",
	}, ok: true, gen: m.streamGen})
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventDelta, Delta: "first answer",
	}, ok: true, gen: m.streamGen})
	m.finishStream(&provider.Usage{}, false)

	m.thinking = true
	m.turnRuntime.state = turnModelStreaming
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventDelta, Delta: "second answer, no reasoning this time",
	}, ok: true, gen: m.streamGen})
	m.finishStream(&provider.Usage{}, false)

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.ReasoningDuration != 0 {
		t.Fatalf("ReasoningDuration = %v, want 0 (this turn had no reasoning, and finishStream must reset the prior turn's window)", last.ReasoningDuration)
	}
}

func TestReasoningDurationCapturedViaThinkFilter(t *testing.T) {
	m := newTestModel(t)
	m.resetThinkFilter()
	feedDelta(t, m, "<think>because")
	time.Sleep(5 * time.Millisecond)
	feedDelta(t, m, " reasons</think>42")
	m.finishStream(&provider.Usage{}, false)

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.ReasoningDuration < 5*time.Millisecond {
		t.Fatalf("ReasoningDuration = %v, want >= 5ms (leaked <think> block path)", last.ReasoningDuration)
	}
}
