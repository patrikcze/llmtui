package tui

import (
	"context"
	"strings"
	"time"

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
// transcript or an open dialog. Both corners are *content* coordinates: the
// line index into the viewport's content and the cell column within that
// line (see contentPoint), not screen rows — so a selection survives the
// viewport scrolling under it, whether by edge auto-scroll during a drag or
// by the mouse wheel. They are not normalized (start may be after end —
// normalizeSelection sorts them). selecting is true only while the button is
// held; hasSelection stays true after release so the highlight and the
// copied text remain visible until the next click or Esc clears it. Every
// field's zero value is the correct "no selection" state.
type selectionState struct {
	selecting            bool
	hasSelection         bool
	selStartX, selStartY int
	selEndX, selEndY     int

	// overlay records whether the selection was made over an overlay
	// (m.overlayOpen at click time). Opening or closing one swaps the
	// viewport's content, so a selection from the other side is not shown
	// or copied.
	overlay bool
	// pointerX/pointerY are the last viewport-relative pointer cell seen
	// during the drag, clamped to the viewport. Auto-scroll and wheel
	// scrolling re-derive the selection end from them as content moves
	// under a stationary pointer.
	pointerX, pointerY int
	// scrollDir is the edge auto-scroll direction while dragging: -1 up,
	// +1 down, 0 none. scrollGen tags the live tick chain; a tick carrying
	// an older generation is stale and dropped, so a new drag never runs
	// two chains at once. scrollTicking is true while a tick is pending.
	scrollDir     int
	scrollGen     int
	scrollTicking bool
}

// selectionScrollInterval paces edge auto-scroll while a drag holds the
// pointer at or past the top/bottom of the selectable area.
const selectionScrollInterval = 50 * time.Millisecond

// selectionScrollTickMsg advances edge auto-scroll by one line.
type selectionScrollTickMsg struct{ gen int }

func selectionScrollTick(gen int) tea.Cmd {
	return tea.Tick(selectionScrollInterval, func(time.Time) tea.Msg {
		return selectionScrollTickMsg{gen: gen}
	})
}

// contentPoint converts a viewport-relative cell into content coordinates.
func (m *Model) contentPoint(relX, relY int) (int, int) {
	return relX + m.viewport.XOffset(), relY + m.viewport.YOffset()
}

// selectionLive reports whether the stored selection belongs to the content
// the viewport shows now.
func (m *Model) selectionLive() bool {
	return (m.sel.selecting || m.sel.hasSelection) && m.sel.overlay == m.overlayOpen
}

// isPlainClick reports whether a release at msg lands on the cell where the
// current drag began — a click, not a drag — so clickable controls inside
// the selectable area (thought headers, tool output captions) still toggle.
// With no drag in progress every release counts as a click.
func (m *Model) isPlainClick(z *zone.ZoneInfo, msg tea.MouseReleaseMsg) bool {
	if !m.sel.selecting || z == nil {
		return true
	}
	x, y := m.contentPoint(clampToZone(z, msg.X, msg.Y))
	return x == m.sel.selStartX && y == m.sel.selStartY
}

// beginSelection starts a new selection at the clicked cell, replacing any
// prior one. Only left-clicks inside the viewport start a selection; a click
// elsewhere (input box or status bar) leaves selection state untouched. An
// overlay still uses this viewport, so it is selectable too — especially
// important for /debug last diagnostics.
func (m *Model) beginSelection(msg tea.MouseClickMsg) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	z := zone.Get(m.selectionZoneID())
	if z == nil || !z.InBounds(msg) {
		return m, nil
	}
	relX, relY := z.Pos(msg)
	x, y := m.contentPoint(relX, relY)
	m.sel.selecting = true
	m.sel.hasSelection = false
	m.sel.overlay = m.overlayOpen
	m.sel.pointerX, m.sel.pointerY = relX, relY
	// Retire any tick still pending from an earlier drag; this drag starts
	// its own chain when it reaches an edge.
	m.sel.scrollDir, m.sel.scrollTicking = 0, false
	m.sel.scrollGen++
	m.sel.selStartX, m.sel.selStartY = x, y
	m.sel.selEndX, m.sel.selEndY = x, y
	return m, nil
}

// extendSelection updates the selection's end point while the button is
// held. Motion outside the viewport clamps to the nearest in-bounds cell
// (matches native terminal selection: dragging off the pane still extends
// the selection to the edge) rather than dropping the event. Motion at or
// past the top/bottom edge starts edge auto-scroll, which keeps extending
// the selection on a tick until the pointer moves back inside, the button
// is released, or the content runs out.
func (m *Model) extendSelection(msg tea.MouseMotionMsg) (tea.Model, tea.Cmd) {
	if !m.sel.selecting {
		return m, nil
	}
	z := zone.Get(m.selectionZoneID())
	if z == nil {
		return m, nil
	}
	m.sel.pointerX, m.sel.pointerY = clampToZone(z, msg.X, msg.Y)
	m.sel.selEndX, m.sel.selEndY = m.contentPoint(m.sel.pointerX, m.sel.pointerY)
	m.sel.scrollDir = edgeScrollDir(z, msg.Y)
	if m.sel.scrollDir == 0 || m.sel.scrollTicking {
		return m, nil
	}
	m.sel.scrollTicking = true
	m.sel.scrollGen++
	return m, selectionScrollTick(m.sel.scrollGen)
}

// edgeScrollDir is the auto-scroll direction for a pointer at absolute row
// y: up at or above the zone's first row, down at or below its last.
func edgeScrollDir(z *zone.ZoneInfo, y int) int {
	switch {
	case z.EndY <= z.StartY:
		return 0
	case y <= z.StartY:
		return -1
	case y >= z.EndY:
		return 1
	}
	return 0
}

// autoScrollSelection handles one edge auto-scroll tick: scroll a line in
// the drag's direction and move the selection end to the content now under
// the (stationary) pointer. The chain stops once the drag ends, the pointer
// leaves the edge, or the viewport cannot scroll further.
func (m *Model) autoScrollSelection(msg selectionScrollTickMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.sel.scrollGen {
		return m, nil
	}
	if !m.sel.selecting || m.sel.scrollDir == 0 {
		m.sel.scrollTicking = false
		return m, nil
	}
	before := m.viewport.YOffset()
	if m.sel.scrollDir < 0 {
		m.viewport.ScrollUp(1)
	} else {
		m.viewport.ScrollDown(1)
	}
	if m.viewport.YOffset() == before {
		m.sel.scrollTicking = false
		return m, nil
	}
	m.sel.selEndX, m.sel.selEndY = m.contentPoint(m.sel.pointerX, m.sel.pointerY)
	return m, selectionScrollTick(m.sel.scrollGen)
}

// scrollWithSelection forwards a wheel event to the viewport while keeping
// the selection: it is anchored to content, so it stays put as the content
// scrolls, and during a drag the end follows the content now under the
// pointer — wheel-scrolling mid-drag extends the selection.
func (m *Model) scrollWithSelection(msg tea.MouseWheelMsg) tea.Cmd {
	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	if !m.sel.selecting {
		return cmd
	}
	if z := zone.Get(m.selectionZoneID()); z != nil {
		mouse := msg.Mouse()
		m.sel.pointerX, m.sel.pointerY = clampToZone(z, mouse.X, mouse.Y)
	}
	m.sel.selEndX, m.sel.selEndY = m.contentPoint(m.sel.pointerX, m.sel.pointerY)
	return cmd
}

// endSelection finalizes the drag and, if it covers more than a single
// cell, copies the selected text to the clipboard — reusing the same
// clipboard.WriteText + copyResultMsg path ctrl+y's copyLastReply already
// uses, so the notice/confirmation behavior is identical. The copy covers
// the whole selected content range, including lines scrolled off screen.
func (m *Model) endSelection(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd) {
	if !m.sel.selecting {
		return m, nil
	}
	m.sel.selecting = false
	m.sel.scrollDir = 0
	if z := zone.Get(m.selectionZoneID()); z != nil {
		m.sel.pointerX, m.sel.pointerY = clampToZone(z, msg.X, msg.Y)
		m.sel.selEndX, m.sel.selEndY = m.contentPoint(m.sel.pointerX, m.sel.pointerY)
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
// Esc, on mouse-capture toggles, when a click toggles a control that
// re-renders the content, and when a resize rewraps it.
func (m *Model) clearSelection() {
	m.sel.selecting = false
	m.sel.hasSelection = false
	m.sel.scrollDir = 0
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
// viewport's content lines — all of them, not just the visible screen, so a
// selection extended by scrolling copies in full. Column ranges are cut with
// ansi.Cut (styling-aware) and then stripped, so wide characters and
// embedded ANSI codes don't corrupt the boundary.
//
// Deliberately doesn't gate on m.sel.selecting/m.sel.hasSelection: endSelection
// needs to compute this after already clearing m.sel.selecting (so a caller
// checking that flag afterward sees the drag is over), and a start==end
// coordinate pair already means "no selection" on its own.
func (m *Model) selectedText() string {
	if m.sel.overlay != m.overlayOpen {
		return ""
	}
	return selectionText(strings.Split(m.viewport.GetContent(), "\n"),
		m.sel.selStartX, m.sel.selStartY, m.sel.selEndX, m.sel.selEndY)
}

// selectionText cuts the selected range out of content lines, given
// unnormalized content-coordinate corners. Lines past the end of content
// (it may have shrunk since the drag) are ignored.
func selectionText(lines []string, startX, startY, endX, endY int) string {
	x0, y0, x1, y1 := normalizeSelection(startX, startY, endX, endY)
	if x0 == x1 && y0 == y1 {
		return ""
	}
	if y0 < 0 || y0 >= len(lines) {
		return ""
	}
	if y1 >= len(lines) {
		y1, x1 = len(lines)-1, lipgloss.Width(lines[len(lines)-1])
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
		if right >= width {
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
// visible line with a reverse-video style, given the viewport's already
// fully rendered view. Runs on every render while a selection is active or
// being dragged; view is otherwise returned unchanged.
func (m *Model) applySelectionHighlight(view string) string {
	if !m.selectionLive() {
		return view
	}
	return highlightSelection(view, m.viewport.XOffset(), m.viewport.YOffset(),
		m.sel.selStartX, m.sel.selStartY, m.sel.selEndX, m.sel.selEndY)
}

// highlightSelection reverses the part of a rendered view (whose first row
// is content line yOffset and first column content column xOffset) covered
// by the content-coordinate selection; rows scrolled out of view are skipped.
func highlightSelection(view string, xOffset, yOffset, startX, startY, endX, endY int) string {
	x0, y0, x1, y1 := normalizeSelection(startX, startY, endX, endY)
	if x0 == x1 && y0 == y1 {
		return view
	}
	lines := strings.Split(view, "\n")
	highlight := lipgloss.NewStyle().Reverse(true)
	for cy := max(y0, yOffset); cy <= y1 && cy-yOffset < len(lines); cy++ {
		row := cy - yOffset
		line := lines[row]
		width := lipgloss.Width(line)
		left, right := 0, width
		if cy == y0 {
			left = min(max(x0-xOffset, 0), width)
		}
		if cy == y1 {
			right = min(max(x1-xOffset, 0), width)
		}
		if left >= right {
			continue
		}
		before := ansi.Cut(line, 0, left)
		mid := ansi.Strip(ansi.Cut(line, left, right))
		after := ansi.Cut(line, right, width)
		lines[row] = before + highlight.Render(mid) + after
	}
	return strings.Join(lines, "\n")
}
