package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/patrikcze/llmtui/internal/tui/components"
)

// modalBodyZoneID marks the dialog body so text selection maps clicks into
// the dialog's viewport instead of the hidden transcript.
const modalBodyZoneID = "modal-body"

// modalState presents the open overlay as a centered dialog over the dimmed
// chat instead of letting it take over the whole transcript area. The overlay
// machinery is unchanged: its content still lives in m.viewport (so keys,
// scrolling, selection-visibility and click zones keep working); only the
// viewport is shrunk to the dialog's body size and render() frames it.
type modalState struct {
	title, hint string
	// backdrop is the transcript as it looked when the dialog opened, shown
	// dimmed behind it.
	backdrop string
	// innerW/innerH are the dialog body size; areaW/areaH the transcript
	// area it is centered in. innerW is 0 when the terminal is too small for
	// a dialog, in which case the overlay falls back to the full area.
	innerW, innerH int
	areaW, areaH   int
	// maxInnerH is the tallest body the area allows; innerH shrinks to the
	// content so a short list is not drawn in a mostly empty dialog.
	maxInnerH int
}

// minModalBodyRows keeps a dialog from collapsing below a readable height.
const minModalBodyRows = 6

// modalActive reports whether the open overlay is drawn as a dialog.
func (m *Model) modalActive() bool {
	return m.overlayOpen && m.modal.title != "" && m.modal.innerW > 0
}

// ensureModal presents the overlay that is opening (or already open) as a
// dialog. The first call captures the transcript as the backdrop, so it must
// run before the overlay's content replaces the chat in the viewport.
func (m *Model) ensureModal(title, hint string) {
	if !m.ready {
		return
	}
	if m.modal.title == "" {
		m.modal.backdrop = m.viewport.View()
	}
	m.modal.title, m.modal.hint = title, hint
	m.applyModalSize(m.width, m.viewportHeightFor(m.inputLines))
}

// dropModal returns the viewport to the full transcript area.
func (m *Model) dropModal() {
	if m.modal.title == "" {
		return
	}
	m.modal = modalState{}
	if m.ready {
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(m.viewportHeightFor(m.inputLines))
	}
}

// applyModalSize sizes the viewport to the dialog body for an areaW×areaH
// transcript area, or to the full area when it is too small for a dialog.
func (m *Model) applyModalSize(areaW, areaH int) {
	innerW, innerH, ok := components.ModalInnerSize(areaW, areaH)
	m.modal.areaW, m.modal.areaH = areaW, areaH
	if !ok {
		m.modal.innerW, m.modal.innerH = 0, 0
		m.viewport.SetWidth(areaW)
		m.viewport.SetHeight(areaH)
		return
	}
	m.modal.innerW, m.modal.innerH, m.modal.maxInnerH = innerW, innerH, innerH
	m.viewport.SetWidth(innerW)
	m.viewport.SetHeight(innerH)
}

// fitModalHeight shrinks the dialog body to content (never below
// minModalBodyRows or above the area's maximum). Call after the overlay's
// content is set and before scrolling to a selection.
func (m *Model) fitModalHeight(content string) {
	if !m.modalActive() {
		return
	}
	rows := strings.Count(strings.TrimRight(content, "\n"), "\n") + 1
	m.modal.innerH = min(max(rows, minModalBodyRows), m.modal.maxInnerH)
	m.viewport.SetHeight(m.modal.innerH)
}

// overlayWidth is the width overlay content should be laid out for: the
// dialog body when one is shown, otherwise the transcript width.
func (m *Model) overlayWidth() int {
	if m.modalActive() {
		return m.modal.innerW
	}
	return max(m.width-2, 20)
}

// openModalOverlay opens a static (non-picker) overlay as a dialog.
func (m *Model) openModalOverlay(title, hint string, render func() string) {
	m.clearPicker()
	m.ensureModal(title, hint)
	m.overlayOpen = true
	// Static text is word-wrapped to the dialog body instead of being cut
	// off at its edge; resize re-runs this at the new width.
	m.overlayRender = func() string { return m.wrapForModal(render()) }
	content := m.overlayRender()
	m.viewport.SetContent(content)
	m.fitModalHeight(content)
	m.viewport.GotoTop()
}

// wrapForModal word-wraps static overlay text to the dialog body width,
// breaking long tokens such as paths when they cannot fit.
func (m *Model) wrapForModal(s string) string {
	if !m.modalActive() {
		return s
	}
	return ansi.Wrap(s, m.modal.innerW, "/-_.,")
}

// selectionZoneID is the zone text selection maps clicks through: the
// dialog body while a dialog is open, otherwise the transcript.
func (m *Model) selectionZoneID() string {
	if m.modalActive() {
		return modalBodyZoneID
	}
	return chatViewportZoneID
}

// renderModal frames the viewport as the dialog over the dimmed backdrop.
// The body (with any selection highlight) is padded to full width and
// zone-marked so a drag selects text inside the dialog.
func (m *Model) renderModal() string {
	lines := strings.Split(m.applySelectionHighlight(m.viewport.View()), "\n")
	for i, line := range lines {
		lines[i] = components.FitWidth(line, m.modal.innerW)
	}
	body := zone.Mark(modalBodyZoneID, strings.Join(lines, "\n"))
	box := components.Modal(m.theme, m.modal.title, m.modal.hint, body, m.modal.innerW, m.modal.innerH)
	return components.Overlay(m.theme, m.modal.backdrop, m.modal.areaW, m.modal.areaH, box)
}
