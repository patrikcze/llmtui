package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/patrikcze/llmtui/internal/clipboard"
)

// chatViewportZoneID marks the chat viewport's on-screen position so a raw
// mouse event (absolute terminal coordinates) can be converted into
// viewport-relative coordinates by subtracting the zone's StartX/StartY.
// Selection is built against the viewport's *rendered* lines (word-wrapped,
// markdown-rendered, styled) rather than the underlying message source —
// that's what a native terminal selection would give you, and there's no
// reliable 1:1 mapping back to source once markdown/wrapping have run.
const chatViewportZoneID = "chat-viewport"

// selectionState holds the click-drag text selection over the chat
// transcript. Coordinates are relative to the chat viewport's on-screen
// position (see chatViewportZoneID), not absolute terminal coordinates, and
// not normalized (start may be after end — normalizeSelection sorts them).
// selecting is true only while the button is held; hasSelection stays true
// after release so the highlight and the copied text remain visible until
// the next click, a scroll, or Esc clears it. Every field's zero value is
// the correct "no selection" state.
type selectionState struct {
	selecting            bool
	hasSelection         bool
	selStartX, selStartY int
	selEndX, selEndY     int
}

// beginSelection starts a new selection at the clicked cell, replacing any
// prior one. Only left-clicks inside the chat viewport start a selection;
// a click elsewhere (input box, status bar) or while an overlay owns the
// viewport leaves selection state untouched.
func (m *Model) beginSelection(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft || m.overlayOpen {
		return m, nil
	}
	z := zone.Get(chatViewportZoneID)
	if z == nil || !z.InBounds(msg) {
		return m, nil
	}
	x, y := z.Pos(msg)
	m.sel.selecting = true
	m.sel.hasSelection = false
	m.sel.selStartX, m.sel.selStartY = x, y
	m.sel.selEndX, m.sel.selEndY = x, y
	return m, nil
}

// extendSelection updates the selection's end point while the button is
// held. Motion outside the viewport clamps to the nearest in-bounds cell
// (matches native terminal selection: dragging off the pane still extends
// the selection to the edge) rather than dropping the event.
func (m *Model) extendSelection(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if !m.sel.selecting {
		return m, nil
	}
	z := zone.Get(chatViewportZoneID)
	if z == nil {
		return m, nil
	}
	x, y := clampToZone(z, msg.X, msg.Y)
	m.sel.selEndX, m.sel.selEndY = x, y
	return m, nil
}

// endSelection finalizes the drag and, if it covers more than a single
// cell, copies the selected text to the clipboard — reusing the same
// clipboard.WriteText + copyResultMsg path ctrl+y's copyLastReply already
// uses, so the notice/confirmation behavior is identical.
func (m *Model) endSelection(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if !m.sel.selecting {
		return m, nil
	}
	m.sel.selecting = false
	if z := zone.Get(chatViewportZoneID); z != nil {
		x, y := clampToZone(z, msg.X, msg.Y)
		m.sel.selEndX, m.sel.selEndY = x, y
	}
	text := m.selectedText()
	if text == "" {
		m.sel.hasSelection = false
		return m, nil
	}
	m.sel.hasSelection = true
	return m, func() tea.Msg {
		err := clipboard.WriteText(context.Background(), text)
		return copyResultMsg{chars: len(text), label: "selection", err: err}
	}
}

// clearSelection drops the current selection and its highlight — called on
// scroll (the coordinates are viewport-relative and don't track content
// under a scroll, so keeping a stale highlight around would be visibly
// wrong) and on Esc.
func (m *Model) clearSelection() {
	m.sel.selecting = false
	m.sel.hasSelection = false
}

// clampToZone converts absolute terminal coordinates into viewport-relative
// coordinates, clamped to the zone's own bounds.
func clampToZone(z *zone.ZoneInfo, x, y int) (int, int) {
	relX, relY := x-z.StartX, y-z.StartY
	maxX, maxY := z.EndX-z.StartX, z.EndY-z.StartY
	switch {
	case relX < 0:
		relX = 0
	case relX > maxX:
		relX = maxX
	}
	switch {
	case relY < 0:
		relY = 0
	case relY > maxY:
		relY = maxY
	}
	return relX, relY
}

// normalizeSelection returns the selection's corners in top-left/
// bottom-right order regardless of which direction the user dragged.
func normalizeSelection(startX, startY, endX, endY int) (x0, y0, x1, y1 int) {
	if startY > endY || (startY == endY && startX > endX) {
		return endX, endY, startX, startY
	}
	return startX, startY, endX, endY
}

// selectedText extracts the plain text of the current selection from the
// chat viewport's currently rendered lines. Column ranges are cut with
// ansi.Cut (styling-aware) and then stripped, so wide characters and
// embedded ANSI codes don't corrupt the boundary.
//
// Deliberately doesn't gate on m.sel.selecting/m.sel.hasSelection: endSelection
// needs to compute this after already clearing m.sel.selecting (so a caller
// checking that flag afterward sees the drag is over), and a start==end
// coordinate pair already means "no selection" on its own.
func (m *Model) selectedText() string {
	x0, y0, x1, y1 := normalizeSelection(m.sel.selStartX, m.sel.selStartY, m.sel.selEndX, m.sel.selEndY)
	if x0 == x1 && y0 == y1 {
		return ""
	}
	lines := strings.Split(m.viewport.View(), "\n")
	if y0 < 0 || y1 >= len(lines) {
		return ""
	}
	var b strings.Builder
	for y := y0; y <= y1; y++ {
		line := lines[y]
		width := lipgloss.Width(line)
		left, right := 0, width
		if y == y0 {
			left = x0
		}
		if y == y1 {
			right = x1
		}
		if left > right {
			left = right
		}
		cut := ansi.Strip(ansi.Cut(line, left, right))
		if right == width {
			// Selected to (or past) the line's rendered content — the
			// viewport pads short lines with spaces out to its full
			// width, which isn't part of what a user would consider
			// "the text"; a real terminal's copy doesn't include it either.
			cut = strings.TrimRight(cut, " ")
		}
		b.WriteString(cut)
		if y != y1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// applySelectionHighlight re-renders the selected column range of each
// affected line with a reverse-video style, given the viewport's already
// fully rendered view. Runs on every render while a selection is active or
// being dragged; view is otherwise returned unchanged.
func (m *Model) applySelectionHighlight(view string) string {
	if !m.sel.selecting && !m.sel.hasSelection {
		return view
	}
	x0, y0, x1, y1 := normalizeSelection(m.sel.selStartX, m.sel.selStartY, m.sel.selEndX, m.sel.selEndY)
	if x0 == x1 && y0 == y1 {
		return view
	}
	lines := strings.Split(view, "\n")
	highlight := lipgloss.NewStyle().Reverse(true)
	for y := y0; y <= y1 && y < len(lines); y++ {
		if y < 0 {
			continue
		}
		line := lines[y]
		width := lipgloss.Width(line)
		left, right := 0, width
		if y == y0 {
			left = x0
		}
		if y == y1 {
			right = x1
		}
		if left >= right {
			continue
		}
		before := ansi.Cut(line, 0, left)
		mid := ansi.Strip(ansi.Cut(line, left, right))
		after := ansi.Cut(line, right, width)
		lines[y] = before + highlight.Render(mid) + after
	}
	return strings.Join(lines, "\n")
}
