package tui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestClickDragSelectsAndCopiesText(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 5; i++ {
		m.session.AddAssistant(fmt.Sprintf("line number %d of the reply", i))
	}
	m.refreshViewport()

	m.View() // triggers zone.Scan(), registering the chat viewport's bounds
	z := waitForZone(t, chatViewportZoneID)

	m.Update(tea.MouseClickMsg{X: z.StartX + 2, Y: z.StartY, Button: tea.MouseLeft})
	if !m.sel.selecting {
		t.Fatal("click inside the chat viewport should start a selection")
	}

	m.Update(tea.MouseMotionMsg{X: z.StartX + 10, Y: z.StartY, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: z.StartX + 10, Y: z.StartY, Button: tea.MouseLeft})

	if m.sel.selecting {
		t.Error("release should end the drag")
	}
	if !m.sel.hasSelection {
		t.Error("a multi-cell drag should leave a selection behind")
	}
	if cmd == nil {
		t.Error("a non-empty selection should return a clipboard-write command")
	}
}

func TestSingleCellClickDoesNotSelect(t *testing.T) {
	m := newTestModel(t)
	m.session.AddAssistant("some reply text")
	m.refreshViewport()

	m.View()
	z := waitForZone(t, chatViewportZoneID)

	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	_, cmd := m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})

	if m.sel.hasSelection {
		t.Error("a click with no drag should not leave a selection")
	}
	if cmd != nil {
		t.Error("a click with no drag should not trigger a clipboard write")
	}
}

func TestClickOutsideViewportDoesNotSelect(t *testing.T) {
	m := newTestModel(t)
	m.session.AddAssistant("some reply text")
	m.refreshViewport()

	m.View()
	waitForZone(t, chatViewportZoneID)

	// Far outside any real terminal size; guaranteed out of the zone's
	// bounds regardless of layout.
	m.Update(tea.MouseClickMsg{X: 9999, Y: 9999, Button: tea.MouseLeft})
	if m.sel.selecting {
		t.Error("a click outside the chat viewport should not start a selection")
	}
}

func TestScrollClearsSelection(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < 40; i++ {
		m.session.AddAssistant(fmt.Sprintf("line %d", i))
	}
	m.refreshViewport()

	m.View()
	z := waitForZone(t, chatViewportZoneID)
	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: z.StartX + 5, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: z.StartX + 5, Y: z.StartY, Button: tea.MouseLeft})
	if !m.sel.hasSelection {
		t.Fatal("setup: expected a selection before scrolling")
	}

	m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})

	if m.sel.hasSelection || m.sel.selecting {
		t.Error("scrolling should clear the selection — its coordinates no longer point at the same content")
	}
}

func TestEscClearsSelection(t *testing.T) {
	m := newTestModel(t)
	m.session.AddAssistant("some reply text")
	m.refreshViewport()

	m.View()
	z := waitForZone(t, chatViewportZoneID)
	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: z.StartX + 5, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: z.StartX + 5, Y: z.StartY, Button: tea.MouseLeft})
	if !m.sel.hasSelection {
		t.Fatal("setup: expected a selection before esc")
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	if m.sel.hasSelection {
		t.Error("esc should clear the selection")
	}
}

func TestSelectedTextExtractsPlainRange(t *testing.T) {
	m := newTestModel(t)
	m.sel.selecting = true
	m.sel.selStartX, m.sel.selStartY = 0, 0
	m.sel.selEndX, m.sel.selEndY = 5, 0

	// selectedText reads from m.viewport.View(); set simple, unstyled
	// content directly so the expected slice is unambiguous.
	m.viewport.SetContent("hello world\nsecond line")

	got := m.selectedText()
	if got != "hello" {
		t.Errorf("selectedText() = %q, want %q", got, "hello")
	}
}

func TestSelectedTextSpansMultipleLines(t *testing.T) {
	m := newTestModel(t)
	m.sel.selecting = true
	m.sel.selStartX, m.sel.selStartY = 6, 0
	m.sel.selEndX, m.sel.selEndY = 6, 1

	m.viewport.SetContent("hello world\nsecond line")

	got := m.selectedText()
	want := "world\nsecond"
	if got != want {
		t.Errorf("selectedText() = %q, want %q", got, want)
	}
}

func TestSelectedTextHandlesReversedDrag(t *testing.T) {
	m := newTestModel(t)
	m.sel.selecting = true
	// Dragged from right to left — start is after end.
	m.sel.selStartX, m.sel.selStartY = 5, 0
	m.sel.selEndX, m.sel.selEndY = 0, 0

	m.viewport.SetContent("hello world")

	got := m.selectedText()
	if got != "hello" {
		t.Errorf("selectedText() = %q, want %q (normalizeSelection should sort the corners)", got, "hello")
	}
}
