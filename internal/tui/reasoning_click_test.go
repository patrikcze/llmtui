package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/provider"
)

// TestClickingReasoningHeaderTogglesVisibility is the feature itself:
// clicking a "+/- Thought" header does exactly what /thoughts show|hide
// does, without needing to type the command.
func TestClickingReasoningHeaderTogglesVisibility(t *testing.T) {
	m := newTestModel(t)
	// newTestModel seeds a non-empty SystemPrompt, so this AddMessage lands
	// at session index 1, not 0 — hence reasoningZoneID(1) below.
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "answer text",
		Reasoning: "because X", ReasoningDuration: 2 * time.Second,
	})
	m.showReasoning = false
	m.refreshViewport()

	m.View() // triggers zone.Scan(), registering the header's bounds
	z := waitForZone(t, reasoningZoneID(1))

	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	if !m.showReasoning {
		t.Error("clicking the collapsed header did not show reasoning")
	}

	m.View()
	z = waitForZone(t, reasoningZoneID(1))
	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	if m.showReasoning {
		t.Error("clicking the expanded header did not hide reasoning again")
	}
}

// TestClickingLiveReasoningHeaderToggles covers the in-progress-turn case
// (renderLiveTail), which uses the separate liveReasoningZoneID rather than
// a per-message index.
func TestClickingLiveReasoningHeaderToggles(t *testing.T) {
	m := newTestModel(t)
	m.showReasoning = true
	m.thinking = true
	m.reasoningBuf.WriteString("still working on it")
	m.refreshViewport()

	m.View()
	z := waitForZone(t, liveReasoningZoneID)

	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	if m.showReasoning {
		t.Error("clicking the live reasoning header did not hide it")
	}
}

// TestReasoningClickResetsSelectionState guards the bug this feature could
// easily introduce: the header sits inside the same viewport region
// beginSelection uses for click-drag text selection, so a plain click on it
// must not leave m.sel.selecting stuck true (which would corrupt the next
// drag).
func TestReasoningClickResetsSelectionState(t *testing.T) {
	m := newTestModel(t)
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "answer text",
		Reasoning: "because X", ReasoningDuration: 2 * time.Second,
	})
	m.showReasoning = false
	m.refreshViewport()
	m.View()
	z := waitForZone(t, reasoningZoneID(1))

	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})

	if m.sel.selecting {
		t.Error("clicking a reasoning header left m.sel.selecting stuck true")
	}
	if m.sel.hasSelection {
		t.Error("clicking a reasoning header should not leave a stale selection highlight")
	}
}

// TestDraggingThroughReasoningHeaderStillSelectsText is the other half of
// that same concern: a real drag that happens to pass over (or end on) a
// reasoning header must still finalize as a text selection, not get
// swallowed as a toggle click.
func TestDraggingThroughReasoningHeaderStillSelectsText(t *testing.T) {
	m := newTestModel(t)
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "answer text",
		Reasoning: "because X", ReasoningDuration: 2 * time.Second,
	})
	m.showReasoning = false
	m.refreshViewport()
	m.View()
	z := waitForZone(t, chatViewportZoneID)
	rz := waitForZone(t, reasoningZoneID(1))
	before := m.showReasoning

	// Drag from one cell before the header's start to its end — a real
	// selection, not a click.
	m.Update(tea.MouseClickMsg{X: z.StartX, Y: rz.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: rz.EndX, Y: rz.StartY})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: rz.EndX, Y: rz.StartY, Button: tea.MouseLeft})

	if m.showReasoning != before {
		t.Error("a drag ending on the reasoning header toggled visibility instead of finalizing a selection")
	}
	if cmd == nil {
		t.Error("a real drag over the header should still finalize as a copy command, like ordinary text selection")
	}
}
