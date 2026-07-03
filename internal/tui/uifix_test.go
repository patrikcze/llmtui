package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestWrapLinesCountsWordWrap(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  int
	}{
		{"empty", "", 10, 1},
		{"single short line", "hello", 10, 1},
		{"explicit newlines", "a\nb\nc", 10, 3},
		// "hello world foo" at width 12: "hello world " + "foo" — a plain
		// character count (15/12) would say 1 row + remainder ≈ 2; make sure
		// word wrap agrees where it matters:
		{"word wrap overflows earlier than char wrap", "aaaa bbbb cccc", 10, 2},
		// Ten 6-char words at width 20: char count = 69/20 ≈ 4 rows, word
		// wrap fits only 2 words (14 cells) per row = 5 rows.
		{"many medium words", strings.TrimSpace(strings.Repeat("worddd ", 10)), 20, 5},
		{"long word hard-breaks", strings.Repeat("x", 25), 10, 3},
		{"cap at six rows", strings.Repeat("word ", 200), 10, 6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := wrapLines(tc.value, tc.width); got != tc.want {
				t.Errorf("wrapLines(%q, %d) = %d, want %d", tc.value, tc.width, got, tc.want)
			}
		})
	}
}

func TestTypingDoesNotScrollViewport(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	// Fill the viewport so there is something to scroll.
	for i := 0; i < 40; i++ {
		m.session.AddAssistant("line")
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	before := m.viewport.YOffset

	// Space, letters bound in the viewport keymap (j/k/u/d/b/f), arrows —
	// none of them may move the chat while the user is typing.
	for _, r := range "hello worldjkudbf" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.viewport.YOffset != before {
		t.Errorf("viewport scrolled from %d to %d while typing", before, m.viewport.YOffset)
	}
	if !strings.Contains(m.input.Value(), "hello world") {
		t.Errorf("input lost keystrokes: %q", m.input.Value())
	}

	// The dedicated scroll keys still work.
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.viewport.YOffset >= before {
		t.Error("PgUp did not scroll the viewport")
	}
}

func TestInputGrowsWithWrappedText(t *testing.T) {
	m := newTestModel(t)
	m.resize(40, 24)
	// Type enough words to word-wrap well past two rows at width 32.
	for _, r := range strings.Repeat("word medium words here ", 6) {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.inputLines < 3 {
		t.Errorf("inputLines = %d, want >= 3 for long wrapped text", m.inputLines)
	}
	if m.input.Height() != m.inputLines {
		t.Errorf("textarea height %d != tracked lines %d", m.input.Height(), m.inputLines)
	}
}

func TestToolOutputCollapsedByDefault(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	long := strings.Repeat("drwxr-xr-x file\n", 30)
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "run_command", Arguments: `{"command":"ls -la"}`},
		},
	})
	m.session.AddMessage(provider.Message{
		Role: provider.RoleTool, ToolName: "run_command", ToolCallID: "c1", Content: long,
	})
	m.refreshViewport()

	view := m.viewport.View()
	joined := strings.Join(strings.Fields(view), " ") // collapse styling/wrapping
	if strings.Contains(joined, "drwxr-xr-x") {
		t.Error("full tool output rendered in compact mode")
	}
	if !strings.Contains(joined, "30 lines of output") {
		t.Errorf("missing collapsed summary in view")
	}
	if !strings.Contains(joined, "run: ls -la") {
		t.Error("missing tool call description")
	}

	// /tools output shows everything again.
	m.toolsShowOutput = true
	m.refreshViewport()
	if !strings.Contains(m.viewport.View(), "drwxr-xr-x") {
		t.Error("full output not shown after /tools output")
	}
}
