package tui

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/provider"
)

var (
	downKey = tea.KeyPressMsg{Code: tea.KeyDown}
	upKey   = tea.KeyPressMsg{Code: tea.KeyUp}
)

// TestPickerNavigationKeepsSelectionVisible is the picker bug the plan
// flagged as unfixed: renderPicker called viewport.GotoTop() after every
// selection change, so navigating a long list could select a row scrolled
// off-screen. Confirmed still present before this fix (PR #92 only patched
// openOverlay, which picker navigation doesn't go through — pickerIdx is
// always 0 there because clearPicker resets it first).
func TestPickerNavigationKeepsSelectionVisible(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 15) // short enough that 40 models can't all be visible
	models := make([]provider.ModelInfo, 40)
	for i := range models {
		models[i] = provider.ModelInfo{ID: "model-" + strconv.Itoa(i)}
	}
	m.openModelsPicker(models)

	// Navigate to the last item — far below the first screenful.
	for i := 0; i < len(models)-1; i++ {
		m.updatePicker(downKey)
	}
	if m.picker.pickerIdx != len(models)-1 {
		t.Fatalf("pickerIdx = %d, want %d after navigating to the end", m.picker.pickerIdx, len(models)-1)
	}
	if m.viewport.AtTop() {
		t.Error("viewport is still scrolled to the top after navigating to the last item — selection is off-screen")
	}

	// And back to the first item: it must become visible again too (not
	// necessarily AtTop() — EnsureVisible scrolls minimally, it doesn't
	// re-center or top-align).
	for i := 0; i < len(models)-1; i++ {
		m.updatePicker(upKey)
	}
	target := pickerHeaderLines[pickerModel] + m.picker.pickerIdx
	offset, height := m.viewport.YOffset(), m.viewport.Height()
	if target < offset || target > offset+height-1 {
		t.Errorf("selected row (line %d) is outside the visible window [%d, %d) after navigating back to the first item", target, offset, offset+height)
	}
}

// TestModelsPickerOpensWithActiveModelVisible: opening the picker while a
// non-default model is selected should show that model highlighted and
// scrolled into view immediately, not at the top with the highlight
// off-screen.
func TestModelsPickerOpensWithActiveModelVisible(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 15)
	models := make([]provider.ModelInfo, 40)
	for i := range models {
		models[i] = provider.ModelInfo{ID: "model-" + strconv.Itoa(i)}
	}
	m.model = "model-35"

	m.openModelsPicker(models)

	if m.picker.pickerIdx != 35 {
		t.Fatalf("pickerIdx = %d, want 35 (the active model)", m.picker.pickerIdx)
	}
	if m.viewport.AtTop() {
		t.Error("opening the picker on a far-down active model left the viewport at the top")
	}
}

// TestOverlayReflowsOnResize is the other half of the plan's Phase 4
// keyboard-access/overlay item: a static overlay's content must rebuild at
// the new width on resize instead of staying stale at whatever width it was
// opened at (refreshViewport is a no-op while an overlay is open, so
// nothing rebuilt it before this fix).
func TestOverlayReflowsOnResize(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 24)
	calls := 0
	m.openOverlay(func() string {
		calls++
		return strings.Repeat("x", m.width)
	})
	if calls != 1 {
		t.Fatalf("openOverlay should invoke render once on open, got %d calls", calls)
	}

	m.resize(60, 24)
	if calls != 2 {
		t.Errorf("resize while an overlay is open should re-invoke its render func, got %d calls", calls)
	}
	if got := len([]rune(m.viewport.GetContent())); got != 60 {
		t.Errorf("overlay content is %d runes wide after resizing to 60, want 60 (stale content)", got)
	}
}

// TestOverlayReflowDoesNotFireWhenClosed guards the other direction: once
// the overlay is closed, resize must not keep invoking a stale render func.
func TestOverlayReflowDoesNotFireWhenClosed(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 24)
	calls := 0
	m.openOverlay(func() string { calls++; return "content" })
	m.closeOverlay()

	m.resize(60, 24)
	if calls != 1 {
		t.Errorf("resize after closeOverlay invoked the old render func %d times, want 1 (only the initial open)", calls)
	}
}

// TestPickerReflowsOnResize checks the picker side of the same fix: a
// resize while a picker overlay is open must rebuild it (and keep the
// selection visible) rather than leaving refreshViewport's no-op guard
// silently skip it.
func TestPickerReflowsOnResize(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 15)
	models := make([]provider.ModelInfo, 40)
	for i := range models {
		models[i] = provider.ModelInfo{ID: "model-" + strconv.Itoa(i)}
	}
	m.openModelsPicker(models)
	for i := 0; i < 35; i++ {
		m.updatePicker(downKey)
	}
	if m.viewport.AtTop() {
		t.Fatal("setup: selection should be scrolled into view before resizing")
	}

	m.resize(60, 15)

	if !strings.Contains(m.viewport.GetContent(), "model-35") {
		t.Error("picker content was not rebuilt at the new width on resize")
	}
	if m.viewport.AtTop() {
		t.Error("resize while a picker is open reset the scroll position, hiding the selection again")
	}
}
