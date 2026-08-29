package tui

import tea "charm.land/bubbletea/v2"

// maxComposerHistoryEntries bounds the in-memory recall buffer. Older entries
// are discarded once the cap is reached; the value is a deliberate constant,
// not a config knob, for v1.
const maxComposerHistoryEntries = 100

// composerHistoryState is a shell-like recall buffer for text the user has
// submitted through the composer — ordinary prompts and slash commands alike.
//
// It is session-local, bounded, text-only, and never persisted. It is entirely
// separate from internal/history (saved chat sessions): /clear wipes the
// conversation but leaves this buffer intact; only process exit discards it.
//
// The zero value is ready to use.
type composerHistoryState struct {
	// entries holds accepted submissions oldest-first.
	entries []string
	// index is the entry the browsing cursor points at; meaningful only while
	// browsing is true.
	index int
	// draft is the composer text captured when browsing began, restored when
	// the user steps back past the newest entry.
	draft string
	// browsing reports whether Up/Down are currently recalling history rather
	// than moving the textarea cursor.
	browsing bool
}

// record appends an accepted submission and ends any browsing session. Empty
// text is ignored (attachment-only sends record nothing). The buffer is capped
// at maxComposerHistoryEntries, discarding the oldest entries first.
func (h *composerHistoryState) record(text string) {
	h.stopBrowsing()
	if text == "" {
		return
	}
	h.entries = append(h.entries, text)
	if len(h.entries) > maxComposerHistoryEntries {
		trimmed := make([]string, maxComposerHistoryEntries)
		copy(trimmed, h.entries[len(h.entries)-maxComposerHistoryEntries:])
		h.entries = trimmed
	}
}

// stopBrowsing exits history navigation without discarding recorded entries.
func (h *composerHistoryState) stopBrowsing() {
	h.browsing = false
	h.index = 0
	h.draft = ""
}

// begin starts a browsing session, saving draft as the text to restore when the
// user returns past the newest entry, and returns the newest entry. It reports
// false (and changes nothing) when there is no history to recall.
func (h *composerHistoryState) begin(draft string) (string, bool) {
	if len(h.entries) == 0 {
		return "", false
	}
	h.browsing = true
	h.draft = draft
	h.index = len(h.entries) - 1
	return h.entries[h.index], true
}

// older steps one entry toward the start of the buffer. It reports false (and
// changes nothing) when not browsing or already at the oldest entry — history
// navigation is ordered, never cyclic.
func (h *composerHistoryState) older() (string, bool) {
	if !h.browsing || h.index == 0 {
		return "", false
	}
	h.index--
	return h.entries[h.index], true
}

// newer steps one entry toward the end of the buffer. Stepping past the newest
// entry ends browsing and returns the saved draft.
func (h *composerHistoryState) newer() (string, bool) {
	if !h.browsing {
		return "", false
	}
	if h.index >= len(h.entries)-1 {
		draft := h.draft
		h.stopBrowsing()
		return draft, true
	}
	h.index++
	return h.entries[h.index], true
}

// current returns the entry the browsing cursor points at, or ("", false) when
// not browsing. It is used to detect whether a recalled entry has been edited.
func (h *composerHistoryState) current() (string, bool) {
	if !h.browsing || h.index < 0 || h.index >= len(h.entries) {
		return "", false
	}
	return h.entries[h.index], true
}

// updateComposerHistory arbitrates a bare Up/Down key press between recalling
// composer history and moving the textarea cursor. It returns true only when it
// fully consumes the key; false means "not mine" — the caller then lets the
// slash-suggestion popup or the textarea handle it, unchanged.
//
// Arbitration (this runs *after* modal owners — approvals, /keys, overlays and
// pickers — have already had their chance earlier in Update):
//
//   - Only bare "up"/"down" are eligible. "shift+up", "ctrl+up", "alt+up",
//     "pgup", "ctrl+p" etc. never match and fall through untouched.
//   - A recalled entry that has since been edited detaches from browsing
//     (detected here by comparing the composer value to the recalled entry), so
//     ordinary editing keeps working and suggestions regain Up/Down.
//   - While browsing: Up/Down move the textarea cursor until it reaches the top
//     or bottom visual row (soft-wrap aware), then step to the older/newer
//     entry. Down past the newest entry restores the pre-browsing draft.
//   - While not browsing: Up on an empty composer enters history at the newest
//     entry. Every other case (non-empty draft, Down) is left to the textarea,
//     so a partially typed prompt is never replaced.
func (m *Model) updateComposerHistory(msg tea.KeyPressMsg) bool {
	dir := msg.String()
	if dir != "up" && dir != "down" {
		return false
	}
	h := &m.composerHistory

	if h.browsing {
		if cur, ok := h.current(); !ok || m.input.Value() != cur {
			h.stopBrowsing()
		}
	}

	if h.browsing {
		if dir == "up" {
			if !m.composerCursorAtTop() {
				return false
			}
			if entry, ok := h.older(); ok {
				m.setComposerFromHistory(entry)
			}
			return true
		}
		if !m.composerCursorAtBottom() {
			return false
		}
		if entry, ok := h.newer(); ok {
			m.setComposerFromHistory(entry)
		}
		return true
	}

	if dir == "up" && m.input.Value() == "" {
		// The draft is "" here by construction; begin still takes it so a
		// future explicit "force history" shortcut can preserve a real draft.
		if entry, ok := h.begin(""); ok {
			m.setComposerFromHistory(entry)
			return true
		}
	}
	return false
}

// setComposerFromHistory replaces the composer contents with a recalled entry,
// puts the cursor at the end, and resizes the input box. Suggestions are
// deliberately not recomputed: a recalled "/cmd" must not pop a dropdown that
// would fight the history keys until the user actually edits it.
func (m *Model) setComposerFromHistory(text string) {
	m.input.SetValue(text)
	m.input.CursorEnd()
	m.syncInputHeight()
}

// composerCursorAtTop reports whether the textarea cursor is on the first
// visual row (accounting for soft-wrapped logical lines).
func (m *Model) composerCursorAtTop() bool {
	return m.input.Line() == 0 && m.input.LineInfo().RowOffset == 0
}

// composerCursorAtBottom reports whether the textarea cursor is on the last
// visual row (accounting for soft-wrapped logical lines).
func (m *Model) composerCursorAtBottom() bool {
	li := m.input.LineInfo()
	return m.input.Line() == m.input.LineCount()-1 && li.RowOffset == li.Height-1
}
