package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/tools"
)

// fakeCSI mimics Bubble Tea's unexported unknownCSISequenceMsg, which prints
// as "?CSI[<bytes>]?" via fmt.Stringer.
type fakeCSI string

func (f fakeCSI) String() string {
	var fields []string
	for _, b := range []byte(f) {
		fields = append(fields, fmt.Sprintf("%d", b))
	}
	return "?CSI[" + strings.Join(fields, " ") + "]?"
}

func TestExtendedKeySeq(t *testing.T) {
	seq, ok := extendedKeySeq(fakeCSI("27;2;13~"))
	if !ok || seq != "27;2;13~" {
		t.Errorf("extendedKeySeq = (%q, %v), want round-trip", seq, ok)
	}
	if _, ok := extendedKeySeq(tea.KeyMsg{Type: tea.KeyEnter}); ok {
		t.Error("plain KeyMsg must not match")
	}
	if _, ok := extendedKeySeq("random string"); ok {
		t.Error("non-Stringer must not match")
	}
}

func TestIsModifiedEnter(t *testing.T) {
	modified := []string{
		"27;2;13~", // xterm format 0: shift+enter
		"27;3;13~", // alt+enter
		"27;5;13~", // ctrl+enter
		"13;2u",    // CSI-u: shift+enter
		"13;2:1u",  // kitty CSI-u with event type
	}
	for _, seq := range modified {
		if !isModifiedEnter(seq) {
			t.Errorf("isModifiedEnter(%q) = false, want true", seq)
		}
	}
	plain := []string{
		"27;1;13~", // unmodified
		"13;1u",
		"27;2;65~", // shift+A, not enter
		"65;2u",
		"200~", // bracketed paste start
		"1;2A", // shift+up arrow
	}
	for _, seq := range plain {
		if isModifiedEnter(seq) {
			t.Errorf("isModifiedEnter(%q) = true, want false", seq)
		}
	}
}

func TestShiftEnterSequenceInsertsNewline(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "first")

	m.Update(fakeCSI("27;2;13~"))
	typeText(m, "second")

	if got := m.input.Value(); got != "first\nsecond" {
		t.Errorf("input = %q, want newline inserted", got)
	}
	if userMessages(m) != 0 {
		t.Error("shift+enter must not send")
	}
	if m.inputLines != 2 {
		t.Errorf("inputLines = %d, want 2", m.inputLines)
	}
}

func TestShiftEnterIsSwallowedDuringToolApproval(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "unchanged")
	m.pendingCalls = []tools.Call{{ID: "call-1", Tool: tools.ToolWriteFile, Path: "a.txt", Body: "x"}}

	m.Update(fakeCSI("27;2;13~"))

	if got := m.input.Value(); got != "unchanged" {
		t.Errorf("input = %q, pending approval must own modified key events", got)
	}
	if len(m.pendingCalls) != 1 {
		t.Fatal("modified key unexpectedly resolved the approval")
	}
}

func TestBackslashEnterContinuesLine(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "first line\\")

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "first line\n" {
		t.Errorf("input = %q, want backslash replaced by newline", got)
	}
	if userMessages(m) != 0 {
		t.Error("backslash+enter must not send")
	}

	typeText(m, "second line")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if userMessages(m) == 0 {
		t.Fatal("plain enter should send")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Content != "first line\nsecond line" {
		t.Errorf("sent = %q, want both lines", last.Content)
	}
}

func TestUnknownCSIIgnoredHarmlessly(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "text")

	// A non-enter extended sequence (e.g. shift+up) must not touch the input.
	m.Update(fakeCSI("1;2A"))
	if got := m.input.Value(); got != "text" {
		t.Errorf("input = %q, unknown sequence must be swallowed", got)
	}
}
