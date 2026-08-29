package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/provider"
)

// --- key helpers ----------------------------------------------------------

func kUp() tea.KeyPressMsg         { return tea.KeyPressMsg{Code: tea.KeyUp} }
func kDown() tea.KeyPressMsg       { return tea.KeyPressMsg{Code: tea.KeyDown} }
func kEnter() tea.KeyPressMsg      { return tea.KeyPressMsg{Code: tea.KeyEnter} }
func kShiftEnter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift} }
func kBackspace() tea.KeyPressMsg  { return tea.KeyPressMsg{Code: tea.KeyBackspace} }
func kCtrl(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl} }

func kType(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// endTurn clears the streaming/busy state a real send() enters synchronously,
// so a test can submit more than one prompt. The returned dispatch Cmd is
// never run in these tests, so no goroutine or provider call is in flight.
func endTurn(m *Model) {
	m.thinking = false
	m.resetTurn(0, "")
}

// submit types text into the composer, presses Enter, and settles the turn.
func submit(m *Model, text string) {
	kType(m, text)
	m.Update(kEnter())
	endTurn(m)
}

// --- pure state-machine tests (no Bubble Tea) --------------------------------

func TestComposerHistoryRecordAndRecall(t *testing.T) {
	var h composerHistoryState

	if _, ok := h.begin(""); ok {
		t.Fatal("begin on empty history should report false")
	}

	h.record("hello")
	h.record("/agent off")
	h.record("explain this function")

	entry, ok := h.begin("")
	if !ok || entry != "explain this function" {
		t.Fatalf("begin = %q,%v; want newest entry", entry, ok)
	}
	if e, ok := h.older(); !ok || e != "/agent off" {
		t.Fatalf("older = %q,%v; want /agent off", e, ok)
	}
	if e, ok := h.older(); !ok || e != "hello" {
		t.Fatalf("older = %q,%v; want hello", e, ok)
	}
	if e, ok := h.older(); ok {
		t.Fatalf("older past oldest = %q,%v; want no-op (no wrap)", e, ok)
	}
	if e, ok := h.newer(); !ok || e != "/agent off" {
		t.Fatalf("newer = %q,%v; want /agent off", e, ok)
	}
	if e, ok := h.newer(); !ok || e != "explain this function" {
		t.Fatalf("newer = %q,%v; want newest", e, ok)
	}
	if e, ok := h.newer(); !ok || e != "" {
		t.Fatalf("newer past newest = %q,%v; want empty draft", e, ok)
	}
	if h.browsing {
		t.Fatal("stepping past newest should end browsing")
	}
	if e, ok := h.newer(); ok {
		t.Fatalf("newer while not browsing = %q,%v; want no-op", e, ok)
	}
}

func TestComposerHistoryDraftPreservation(t *testing.T) {
	var h composerHistoryState
	h.record("one")
	h.record("two")

	if e, ok := h.begin("half-written draft"); !ok || e != "two" {
		t.Fatalf("begin = %q,%v", e, ok)
	}
	h.older() // "one"
	if e, ok := h.newer(); !ok || e != "two" {
		t.Fatalf("newer = %q,%v; want two", e, ok)
	}
	if e, ok := h.newer(); !ok || e != "half-written draft" {
		t.Fatalf("newer past newest = %q,%v; want the saved draft", e, ok)
	}
}

func TestComposerHistoryBound(t *testing.T) {
	var h composerHistoryState
	for i := 0; i < maxComposerHistoryEntries+25; i++ {
		h.record(fmt.Sprintf("entry-%d", i))
	}
	if len(h.entries) != maxComposerHistoryEntries {
		t.Fatalf("len(entries) = %d, want %d", len(h.entries), maxComposerHistoryEntries)
	}
	if h.entries[0] != "entry-25" {
		t.Fatalf("oldest kept entry = %q, want entry-25", h.entries[0])
	}
	if want := fmt.Sprintf("entry-%d", maxComposerHistoryEntries+24); h.entries[len(h.entries)-1] != want {
		t.Fatalf("newest entry = %q, want %q", h.entries[len(h.entries)-1], want)
	}
}

func TestComposerHistoryRecordIgnoresEmptyAndResetsBrowsing(t *testing.T) {
	var h composerHistoryState
	h.record("a")
	h.record("")
	if len(h.entries) != 1 {
		t.Fatalf("empty text must not be recorded: %v", h.entries)
	}
	h.begin("")
	if !h.browsing {
		t.Fatal("expected browsing")
	}
	h.record("b")
	if h.browsing || h.index != 0 || h.draft != "" {
		t.Fatalf("record must reset browsing state: %+v", h)
	}
}

func TestComposerHistoryStopBrowsingKeepsEntries(t *testing.T) {
	var h composerHistoryState
	h.record("a")
	h.record("b")
	h.begin("")
	h.stopBrowsing()
	if h.browsing {
		t.Fatal("stopBrowsing should clear the browsing flag")
	}
	if len(h.entries) != 2 {
		t.Fatalf("stopBrowsing must not discard entries: %v", h.entries)
	}
}

// --- TUI integration: recall through Model.Update ---------------------------

func TestComposerRecallEmptyHistoryUpIsNoop(t *testing.T) {
	m := newTestModel(t)
	m.Update(kUp())
	if m.input.Value() != "" {
		t.Fatalf("Up with no history changed the composer: %q", m.input.Value())
	}
	if m.composerHistory.browsing {
		t.Fatal("Up with no history must not enter browsing")
	}
}

func TestComposerRecallNormalPromptAndSlashCommand(t *testing.T) {
	m := newTestModel(t)

	submit(m, "hello")
	submit(m, "/agent off")

	if got := m.composerHistory.entries; len(got) != 2 || got[0] != "hello" || got[1] != "/agent off" {
		t.Fatalf("entries = %#v; want [hello /agent off]", got)
	}

	m.Update(kUp())
	if m.input.Value() != "/agent off" {
		t.Fatalf("first Up = %q; want /agent off", m.input.Value())
	}
	m.Update(kUp())
	if m.input.Value() != "hello" {
		t.Fatalf("second Up = %q; want hello", m.input.Value())
	}
	m.Update(kUp()) // at oldest, no wrap
	if m.input.Value() != "hello" {
		t.Fatalf("Up at oldest = %q; want hello (no wrap)", m.input.Value())
	}
	m.Update(kDown())
	if m.input.Value() != "/agent off" {
		t.Fatalf("Down = %q; want /agent off", m.input.Value())
	}
	m.Update(kDown()) // past newest -> draft
	if m.input.Value() != "" {
		t.Fatalf("Down past newest = %q; want empty draft", m.input.Value())
	}
	m.Update(kDown())
	if m.input.Value() != "" || m.composerHistory.browsing {
		t.Fatalf("Down again = %q browsing=%v", m.input.Value(), m.composerHistory.browsing)
	}
}

func TestComposerRecallDoesNotReplaceTypedDraft(t *testing.T) {
	m := newTestModel(t)
	submit(m, "old prompt")

	kType(m, "Please explain this function")
	m.Update(kUp())
	if m.input.Value() != "Please explain this function" {
		t.Fatalf("Up replaced a typed draft: %q", m.input.Value())
	}
	if m.composerHistory.browsing {
		t.Fatal("Up on a non-empty composer must not enter browsing")
	}
}

func TestComposerRecalledEntryIsEditableAndReSubmits(t *testing.T) {
	m := newTestModel(t)
	submit(m, "/agent off")

	m.Update(kUp())
	if m.input.Value() != "/agent off" {
		t.Fatalf("recall = %q", m.input.Value())
	}
	m.Update(kBackspace())
	m.Update(kBackspace())
	m.Update(kBackspace())
	kType(m, "on")
	if m.input.Value() != "/agent on" {
		t.Fatalf("edited recall = %q; want /agent on", m.input.Value())
	}
	m.Update(kEnter())
	endTurn(m)
	if got := m.composerHistory.entries; len(got) != 2 || got[1] != "/agent on" {
		t.Fatalf("edited command not recorded as a new entry: %#v", got)
	}
}

func TestComposerEditDetachesFromBrowsing(t *testing.T) {
	m := newTestModel(t)
	submit(m, "first")
	submit(m, "second")

	m.Update(kUp()) // "second"
	m.Update(kUp()) // "first"
	m.Update(kBackspace())
	if m.input.Value() != "firs" {
		t.Fatalf("edit = %q", m.input.Value())
	}
	m.Update(kUp())
	if m.input.Value() != "firs" {
		t.Fatalf("Up after edit changed value: %q", m.input.Value())
	}
	if m.composerHistory.browsing {
		t.Fatal("editing a recalled entry must detach from browsing")
	}
}

func TestComposerAttachmentOnlySendRecordsNothing(t *testing.T) {
	m := newTestModel(t)
	m.attachments = []provider.Image{{Data: []byte("png"), MIME: "image/png"}}
	m.send()
	if len(m.composerHistory.entries) != 0 {
		t.Fatalf("attachment-only send recorded an entry: %#v", m.composerHistory.entries)
	}
}

func TestComposerHistorySurvivesClear(t *testing.T) {
	m := newTestModel(t)
	submit(m, "keep me")

	runCommand(m, "/clear")
	endTurn(m)

	// /clear clears the conversation, not the composer buffer. It is itself a
	// submitted slash command, so it becomes the newest entry; "keep me" is
	// still recallable behind it.
	if got := m.composerHistory.entries; len(got) != 2 || got[0] != "keep me" || got[1] != "/clear" {
		t.Fatalf("entries after /clear = %#v; want [keep me /clear]", got)
	}
	m.Update(kUp())
	m.Update(kUp())
	if m.input.Value() != "keep me" {
		t.Fatalf("prompt submitted before /clear not recallable: %q", m.input.Value())
	}
}

func TestComposerHistoryIsPerModel(t *testing.T) {
	m1 := newTestModel(t)
	m2 := newTestModel(t)
	submit(m1, "only in m1")

	m2.Update(kUp())
	if m2.input.Value() != "" || m2.composerHistory.browsing {
		t.Fatalf("composer history leaked across Models: %q", m2.input.Value())
	}
}

func TestComposerCtrlUClearsRecalledInputKeepsHistory(t *testing.T) {
	m := newTestModel(t)
	submit(m, "recall target")

	m.Update(kUp())
	m.Update(kCtrl('u'))
	if m.input.Value() != "" {
		t.Fatalf("ctrl+u did not clear: %q", m.input.Value())
	}
	m.Update(kUp())
	if m.input.Value() != "recall target" {
		t.Fatalf("ctrl+u damaged history: %q", m.input.Value())
	}
}

func TestComposerCtrlCInputClearResetsBrowsing(t *testing.T) {
	m := newTestModel(t)
	submit(m, "abc")

	m.Update(kUp()) // browsing "abc"
	m.Update(kCtrl('c'))
	if m.input.Value() != "" {
		t.Fatalf("ctrl+c did not clear input: %q", m.input.Value())
	}
	m.Update(kUp())
	if m.input.Value() != "abc" {
		t.Fatalf("history lost after ctrl+c: %q", m.input.Value())
	}
}

// --- multiline / soft-wrap safety -----------------------------------------

func TestComposerMultilineArrowsNavigateNotRecall(t *testing.T) {
	m := newTestModel(t)
	submit(m, "history entry")

	kType(m, "line one")
	m.Update(kShiftEnter())
	kType(m, "line two")
	m.Update(kShiftEnter())
	kType(m, "line three")

	want := "line one\nline two\nline three"
	if m.input.Value() != want {
		t.Fatalf("multiline draft = %q", m.input.Value())
	}

	for i := 0; i < 5; i++ {
		m.Update(kUp())
		if m.input.Value() != want {
			t.Fatalf("Up #%d replaced the multiline draft: %q", i, m.input.Value())
		}
		if m.composerHistory.browsing {
			t.Fatalf("Up #%d entered history browsing on a multiline draft", i)
		}
	}
	for i := 0; i < 5; i++ {
		m.Update(kDown())
		if m.input.Value() != want {
			t.Fatalf("Down #%d replaced the multiline draft: %q", i, m.input.Value())
		}
	}
}

func TestComposerSoftWrappedDraftArrowsDoNotRecall(t *testing.T) {
	m := newTestModel(t)
	m.resize(24, 24) // narrow: force soft wrapping
	submit(m, "history entry")

	long := "this is a single logical line long enough to wrap across a very narrow composer several times over and over"
	kType(m, long)
	if strings.Contains(m.input.Value(), "\n") {
		t.Fatalf("expected a single logical line, got newlines: %q", m.input.Value())
	}
	if h := m.input.LineInfo().Height; h < 2 {
		t.Fatalf("draft did not soft-wrap (height %d)", h)
	}

	for i := 0; i < 6; i++ {
		m.Update(kUp())
		if m.input.Value() != long || m.composerHistory.browsing {
			t.Fatalf("Up #%d disturbed a soft-wrapped draft: %q browsing=%v", i, m.input.Value(), m.composerHistory.browsing)
		}
	}
}

func TestComposerRecalledMultilineEntryStaysEditable(t *testing.T) {
	m := newTestModel(t)

	kType(m, "para one")
	m.Update(kShiftEnter())
	kType(m, "para two")
	m.Update(kShiftEnter())
	kType(m, "para three")
	m.Update(kEnter())
	endTurn(m)

	m.Update(kUp())
	want := "para one\npara two\npara three"
	if m.input.Value() != want {
		t.Fatalf("recalled multiline = %q", m.input.Value())
	}
	m.Update(kUp())
	if m.input.Value() != want {
		t.Fatalf("Up inside recalled multiline changed value: %q", m.input.Value())
	}
	m.Update(kBackspace())
	if m.input.Value() == want {
		t.Fatalf("backspace had no effect on recalled multiline entry")
	}
}

// --- key ownership regressions -------------------------------------------

func TestComposerHistoryDoesNotStealSuggestionArrows(t *testing.T) {
	m := newTestModel(t)
	kType(m, "/mo")
	if len(m.suggest.sugs) == 0 {
		t.Fatal("expected slash suggestions for /mo")
	}
	start := m.suggest.sugIdx
	m.Update(kDown())
	if m.suggest.sugIdx == start {
		t.Fatal("Down did not move the suggestion selection")
	}
	m.Update(kUp())
	if m.suggest.sugIdx != start {
		t.Fatal("Up did not move the suggestion selection back")
	}
	if m.composerHistory.browsing {
		t.Fatal("suggestion navigation must not enter history browsing")
	}
}

func TestComposerRecalledBareCommandDoesNotTrapInSuggestions(t *testing.T) {
	m := newTestModel(t)
	submit(m, "hello there")
	submit(m, "/model") // bare command name — would normally show suggestions

	m.Update(kUp())
	if m.input.Value() != "/model" {
		t.Fatalf("recall = %q; want /model", m.input.Value())
	}
	if len(m.suggest.sugs) != 0 {
		t.Fatalf("recall should not open the suggestion popup: %d sugs", len(m.suggest.sugs))
	}
	// History keys keep working while the recalled command sits in the box.
	m.Update(kUp())
	if m.input.Value() != "hello there" {
		t.Fatalf("second Up = %q; want older entry", m.input.Value())
	}
	// Once the user edits, suggestions take over again.
	m.Update(kDown()) // back to /model
	kType(m, "s")     // -> /models
	if len(m.suggest.sugs) == 0 {
		t.Fatal("editing a recalled command should re-enable suggestions")
	}
	if m.composerHistory.browsing {
		t.Fatal("editing must detach from history browsing")
	}
}

func TestComposerHistoryYieldsToOpenOverlay(t *testing.T) {
	m := newTestModel(t)
	submit(m, "recorded")

	runCommand(m, "/profile list")
	if !m.overlayOpen || m.picker.pickerKind != pickerProfile {
		t.Fatalf("profile picker did not open (overlay=%v kind=%v)", m.overlayOpen, m.picker.pickerKind)
	}
	if len(m.picker.pickerItems) < 2 {
		t.Skip("need at least two profiles to test picker arrow ownership")
	}
	before := m.picker.pickerIdx
	m.Update(kDown())
	if m.picker.pickerIdx == before {
		t.Fatal("Down did not move the picker selection while an overlay is open")
	}
	if m.composerHistory.browsing {
		t.Fatal("composer history must not activate while an overlay owns the keyboard")
	}
}

func TestComposerHistoryIgnoresModifiedArrows(t *testing.T) {
	m := newTestModel(t)
	submit(m, "entry a")
	submit(m, "entry b")

	for _, mod := range []tea.KeyMod{tea.ModShift, tea.ModCtrl, tea.ModAlt} {
		m.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: mod})
		if m.composerHistory.browsing {
			t.Fatalf("modified arrow (mod %v) entered history browsing", mod)
		}
		if m.input.Value() != "" {
			t.Fatalf("modified arrow (mod %v) recalled history: %q", mod, m.input.Value())
		}
	}
}
