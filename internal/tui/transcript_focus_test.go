package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/tools"
)

// TestF6TogglesTranscriptFocus is the keyboard-access gap the plan flagged
// as unimplemented: ordinary keyboard input (including PgUp/PgDown) went to
// the composer, with the transcript reachable only via the mouse. F6 opens
// a dedicated navigation mode instead.
func TestF6TogglesTranscriptFocus(t *testing.T) {
	m := newTestModel(t)

	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	if !m.transcriptFocused {
		t.Fatal("F6 did not enter transcript focus mode")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	if m.transcriptFocused {
		t.Error("a second F6 did not leave transcript focus mode")
	}
}

// TestTranscriptFocusRoutesPagingKeysToViewport checks the actual payoff:
// once focused, PgUp/PgDown/Home/End/arrows scroll the chat instead of
// reaching the composer.
func TestTranscriptFocusRoutesPagingKeysToViewport(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 60; i++ {
		m.session.AddAssistant("settled line")
	}
	m.refreshViewport()
	m.viewport.GotoBottom()

	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	before := m.viewport.YOffset()

	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if got := m.viewport.YOffset(); got >= before {
		t.Errorf("PgUp in transcript focus did not scroll up: offset %d -> %d", before, got)
	}
	if val := m.input.Value(); val != "" {
		t.Errorf("PgUp in transcript focus leaked into the composer: %q", val)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	if !m.viewport.AtTop() {
		t.Error("Home in transcript focus did not jump to the top")
	}
}

// TestTranscriptFocusExitsOnOtherKeys covers the "any other key" fallthrough:
// typing (or esc/enter) leaves focus mode and reaches the composer/normal
// handling instead of being silently swallowed.
func TestTranscriptFocusExitsOnOtherKeys(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	if !m.transcriptFocused {
		t.Fatal("setup: F6 should have entered transcript focus mode")
	}

	m.Update(tea.KeyPressMsg{Text: "x"})
	if m.transcriptFocused {
		t.Error("typing did not exit transcript focus mode")
	}
	if !strings.Contains(m.input.Value(), "x") {
		t.Errorf("typed key was not delivered to the composer after exiting focus mode, got %q", m.input.Value())
	}
}

// TestApprovalOwnsKeyboardOverTranscriptFocus is the priority rule the plan
// requires: "Approval ownership and busy-state cancellation retain
// priority." A pending tool approval must still consume every keypress even
// while transcript focus mode is (or was) toggled on.
func TestApprovalOwnsKeyboardOverTranscriptFocus(t *testing.T) {
	m := newTestModel(t)
	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	if !m.transcriptFocused {
		t.Fatal("setup: F6 should have entered transcript focus mode")
	}
	m.pendingCalls = []tools.Call{{ID: "call-1", Tool: tools.ToolReadFile, Path: "a.go"}}

	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	// The approval handler owns every keypress first (Update's pendingCalls
	// guard runs before F6 is ever inspected), so this F6 must not have
	// toggled focus mode off, and the approval must still be pending.
	if !m.transcriptFocused {
		t.Error("F6 toggled transcript focus while a tool approval was pending")
	}
	if len(m.pendingCalls) == 0 {
		t.Error("pendingCalls was cleared by an F6 keypress, which should have been swallowed by the approval handler")
	}
}

// TestEscStillCancelsBusyStateWhileTranscriptFocused is the other half of
// that priority rule: esc pressed in transcript focus mode must still
// cancel an in-flight generation, not just exit focus mode silently.
func TestEscStillCancelsBusyStateWhileTranscriptFocused(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.cancelStream = func() {}

	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	if !m.transcriptFocused {
		t.Fatal("setup: F6 should have entered transcript focus mode")
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.transcriptFocused {
		t.Error("esc did not exit transcript focus mode")
	}
	if m.thinking {
		t.Error("esc in transcript focus mode did not cancel the busy generation")
	}
}
