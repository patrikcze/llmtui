package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestSelectionText(t *testing.T) {
	lines := []string{"alpha one", "bravo two", "charlie three", "delta four"}
	tests := []struct {
		name                       string
		startX, startY, endX, endY int
		want                       string
	}{
		{name: "single line range", startX: 0, startY: 1, endX: 5, endY: 1, want: "bravo"},
		{name: "spans lines", startX: 6, startY: 0, endX: 7, endY: 2, want: "one\nbravo two\ncharlie"},
		{name: "reversed drag", startX: 7, startY: 2, endX: 6, endY: 0, want: "one\nbravo two\ncharlie"},
		{name: "all lines including off-screen rows", startX: 0, startY: 0, endX: 10, endY: 3, want: strings.Join(lines, "\n")},
		{name: "end past content clamps to last line", startX: 0, startY: 2, endX: 3, endY: 9, want: "charlie three\ndelta four"},
		{name: "start past content is empty", startX: 0, startY: 7, endX: 3, endY: 9, want: ""},
		{name: "single cell is empty", startX: 3, startY: 1, endX: 3, endY: 1, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectionText(lines, tt.startX, tt.startY, tt.endX, tt.endY); got != tt.want {
				t.Errorf("selectionText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHighlightSelectionMapsContentToScreen(t *testing.T) {
	// The view shows content lines 10..12; the selection is in content
	// coordinates, so only its on-screen rows change.
	view := "line10\nline11\nline12"
	tests := []struct {
		name                       string
		startX, startY, endX, endY int
		changed                    []bool
	}{
		{name: "selection above view", startX: 0, startY: 2, endX: 4, endY: 5, changed: []bool{false, false, false}},
		{name: "selection below view", startX: 0, startY: 20, endX: 4, endY: 25, changed: []bool{false, false, false}},
		{name: "starts above, ends on screen", startX: 0, startY: 3, endX: 4, endY: 11, changed: []bool{true, true, false}},
		{name: "starts on screen, ends below", startX: 2, startY: 12, endX: 4, endY: 30, changed: []bool{false, false, true}},
		{name: "covers whole view", startX: 0, startY: 0, endX: 1, endY: 99, changed: []bool{true, true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strings.Split(highlightSelection(view, 0, 10, tt.startX, tt.startY, tt.endX, tt.endY), "\n")
			want := strings.Split(view, "\n")
			for i := range want {
				if changed := got[i] != want[i]; changed != tt.changed[i] {
					t.Errorf("row %d changed = %v, want %v (%q)", i, changed, tt.changed[i], got[i])
				}
				if ansi.Strip(got[i]) != want[i] {
					t.Errorf("row %d text = %q, want %q", i, ansi.Strip(got[i]), want[i])
				}
			}
		})
	}
}

// runAutoScroll feeds edge auto-scroll ticks the way the tea.Tick chain
// would, until the chain stops or n ticks have run.
func runAutoScroll(t *testing.T, m *Model, n int) int {
	t.Helper()
	ticks := 0
	for ; ticks < n && m.sel.scrollTicking; ticks++ {
		m.Update(selectionScrollTickMsg{gen: m.sel.scrollGen})
	}
	return ticks
}

func longTranscriptModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	for i := 0; i < 60; i++ {
		m.session.AddAssistant(fmt.Sprintf("reply line %02d", i))
	}
	m.refreshViewport()
	return m
}

func TestDragSelectionAutoScrollsAndCopiesOffscreenText(t *testing.T) {
	tests := []struct {
		name string
		// setup prepares the model and returns the zone ID the drag uses.
		setup func(*Model) string
		// up drags past the top edge (true) or the bottom edge (false).
		up bool
	}{
		{
			name: "chat transcript upward",
			setup: func(m *Model) string {
				m.viewport.GotoBottom()
				return chatViewportZoneID
			},
			up: true,
		},
		{
			name: "chat transcript downward",
			setup: func(m *Model) string {
				m.viewport.GotoTop()
				return chatViewportZoneID
			},
		},
		{
			name: "modal downward",
			setup: func(m *Model) string {
				var b strings.Builder
				for i := 0; i < 80; i++ {
					fmt.Fprintf(&b, "debug row %02d\n", i)
				}
				text := b.String()
				m.openModalOverlay("Debug", "esc close", func() string { return text })
				return modalBodyZoneID
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := longTranscriptModel(t)
			zoneID := tt.setup(m)
			if m.viewport.TotalLineCount() <= m.viewport.VisibleLineCount() {
				t.Fatal("setup: content must be longer than the viewport")
			}
			z := renderedZone(t, m, zoneID)
			startOffset := m.viewport.YOffset()

			clickY, edgeY := z.StartY+1, z.EndY+3
			if tt.up {
				clickY, edgeY = z.EndY-1, z.StartY-3
			}
			m.Update(tea.MouseClickMsg{X: z.StartX, Y: clickY, Button: tea.MouseLeft})
			anchorX, anchorY := m.sel.selStartX, m.sel.selStartY
			_, cmd := m.Update(tea.MouseMotionMsg{X: z.StartX + 4, Y: edgeY, Button: tea.MouseLeft})
			if cmd == nil || !m.sel.scrollTicking {
				t.Fatal("dragging past the edge should start the auto-scroll tick")
			}
			if ticks := runAutoScroll(t, m, 500); ticks == 0 {
				t.Fatal("auto-scroll ran no ticks")
			}
			if m.sel.scrollTicking {
				t.Fatal("auto-scroll should stop once the content runs out")
			}
			if m.viewport.YOffset() == startOffset {
				t.Fatal("auto-scroll did not move the viewport")
			}
			if tt.up && !m.viewport.AtTop() || !tt.up && !m.viewport.AtBottom() {
				t.Errorf("auto-scroll stopped at offset %d before reaching the edge", m.viewport.YOffset())
			}
			if m.sel.selStartX != anchorX || m.sel.selStartY != anchorY {
				t.Errorf("anchor moved during scroll: (%d,%d) -> (%d,%d)", anchorX, anchorY, m.sel.selStartX, m.sel.selStartY)
			}

			_, cmd = m.Update(tea.MouseReleaseMsg{X: z.StartX + 4, Y: edgeY, Button: tea.MouseLeft})
			if cmd == nil || !m.sel.hasSelection {
				t.Fatal("release should keep the selection and copy it")
			}
			got := strings.Split(m.selectedText(), "\n")
			if want := abs(anchorY-m.sel.selEndY) + 1; len(got) != want {
				t.Fatalf("copied %d lines, want %d (the full scrolled range)", len(got), want)
			}
			if len(got) <= m.viewport.VisibleLineCount() {
				t.Errorf("copied %d lines, no more than one screen (%d)", len(got), m.viewport.VisibleLineCount())
			}
		})
	}
}

func TestAutoScrollStopsWhenPointerReturnsInside(t *testing.T) {
	m := longTranscriptModel(t)
	m.viewport.GotoBottom()
	z := renderedZone(t, m, chatViewportZoneID)

	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.EndY - 1, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: z.StartX, Y: z.StartY - 1, Button: tea.MouseLeft})
	m.Update(selectionScrollTickMsg{gen: m.sel.scrollGen})
	offset := m.viewport.YOffset()

	m.Update(tea.MouseMotionMsg{X: z.StartX, Y: z.StartY + 2, Button: tea.MouseLeft})
	if ticks := runAutoScroll(t, m, 10); ticks != 1 {
		t.Errorf("ran %d ticks after the pointer moved inside, want the one pending tick", ticks)
	}
	if m.viewport.YOffset() != offset {
		t.Errorf("viewport kept scrolling after the pointer left the edge: %d -> %d", offset, m.viewport.YOffset())
	}
}

func TestStaleAutoScrollTickIsIgnored(t *testing.T) {
	m := longTranscriptModel(t)
	m.viewport.GotoBottom()
	z := renderedZone(t, m, chatViewportZoneID)

	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.EndY - 1, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: z.StartX, Y: z.StartY - 1, Button: tea.MouseLeft})
	stale := m.sel.scrollGen
	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	// A new drag starts a fresh chain; the old one's tick must not scroll.
	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.EndY - 1, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: z.StartX, Y: z.StartY - 1, Button: tea.MouseLeft})
	offset := m.viewport.YOffset()

	m.Update(selectionScrollTickMsg{gen: stale})
	if m.viewport.YOffset() != offset {
		t.Errorf("a stale tick scrolled the viewport: %d -> %d", offset, m.viewport.YOffset())
	}
}

func TestWheelDuringDragExtendsSelection(t *testing.T) {
	m := longTranscriptModel(t)
	m.viewport.GotoTop()
	z := renderedZone(t, m, chatViewportZoneID)

	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseMotionMsg{X: z.StartX + 3, Y: z.StartY + 2, Button: tea.MouseLeft})
	endBefore := m.sel.selEndY
	for i := 0; i < 3; i++ {
		m.Update(tea.MouseWheelMsg{X: z.StartX + 3, Y: z.StartY + 2, Button: tea.MouseWheelDown})
	}
	if !m.sel.selecting {
		t.Fatal("wheel scrolling must not end an active drag")
	}
	scrolled := m.viewport.YOffset()
	if scrolled == 0 {
		t.Fatal("setup: wheel did not scroll the viewport")
	}
	if m.sel.selEndY != endBefore+scrolled {
		t.Errorf("selection end = line %d, want %d (follows content under the pointer)", m.sel.selEndY, endBefore+scrolled)
	}
	m.Update(tea.MouseReleaseMsg{X: z.StartX + 3, Y: z.StartY + 2, Button: tea.MouseLeft})
	if got := strings.Count(m.selectedText(), "\n") + 1; got != m.sel.selEndY+1 {
		t.Errorf("copied %d lines, want %d", got, m.sel.selEndY+1)
	}
}

func TestClickOnControlAfterScrollStillCountsAsClick(t *testing.T) {
	m := longTranscriptModel(t)
	m.viewport.GotoBottom()
	z := renderedZone(t, m, chatViewportZoneID)
	m.Update(tea.MouseClickMsg{X: z.StartX + 1, Y: z.StartY + 1, Button: tea.MouseLeft})
	release := tea.MouseReleaseMsg{X: z.StartX + 1, Y: z.StartY + 1, Button: tea.MouseLeft}
	if !m.isPlainClick(z, release) {
		t.Error("a release on the press cell of a scrolled viewport should be a plain click")
	}
	release.Y++
	if m.isPlainClick(z, release) {
		t.Error("a release on another cell should be a drag, not a click")
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
