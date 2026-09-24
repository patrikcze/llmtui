package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestWelcomePanelFitsTerminal(t *testing.T) {
	for _, width := range []int{20, 40, 75, 76, 100, 160} {
		m := newTestModel(t)
		m.width = width
		m.model = strings.Repeat("model-", 40)
		panel := m.renderWelcomePanel()
		for _, line := range strings.Split(panel, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("terminal %d: row width %d: %q", width, got, line)
			}
		}
	}
}

func TestWelcomePanelFollowsConversationAndModel(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.model = "first-model"
	if !strings.Contains(ansi.Strip(m.settledTranscriptCached()), "first-model") {
		t.Fatal("missing initial model")
	}
	m.model = "second-model"
	if !strings.Contains(ansi.Strip(m.settledTranscriptCached()), "second-model") {
		t.Fatal("model change did not invalidate welcome panel")
	}
	m.session.AddUser("Hello")
	if strings.Contains(ansi.Strip(m.settledTranscriptCached()), "Welcome back") {
		t.Fatal("welcome panel remained after first message")
	}
}

func TestWorkspacePanelPreservesDisclosure(t *testing.T) {
	m := newTestModel(t)
	m.width = 200
	root := t.TempDir()
	m.toolRunner = tools.NewRunner(root, 64)
	m.toolsOn = true
	for _, auto := range []bool{false, true} {
		m.toolsAutoApprove = auto
		policy := "asks before writes & commands"
		if auto {
			policy = "auto-approve"
		}
		for _, content := range []string{m.renderWelcomePanel(), m.renderWorkspacePanel()} {
			plain := ansi.Strip(content)
			for _, want := range []string{"Workspace tools on", policy, root, "/tools off"} {
				if !strings.Contains(plain, want) {
					t.Errorf("missing %q in %s", want, plain)
				}
			}
		}
	}
}

func TestWelcomeDividerAlignment(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	column := -1
	rows := 0
	for _, line := range strings.Split(ansi.Strip(m.renderWelcomePanel()), "\n") {
		// Ignore the outer frame; the remaining vertical stroke is the logo rail.
		inner := strings.TrimSuffix(strings.TrimPrefix(line, "│"), "│")
		before, _, found := strings.Cut(inner, "│")
		if !found {
			continue
		}
		got := lipgloss.Width(before)
		if column >= 0 && got != column {
			t.Fatalf("divider shifted from column %d to %d: %q", column, got, line)
		}
		column = got
		rows++
	}
	if rows != 6 {
		t.Fatalf("divider rows = %d, want 6", rows)
	}
}
