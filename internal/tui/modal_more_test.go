package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/history"
)

func TestUsageDialogKeepsTabsRangeAndSize(t *testing.T) {
	m := newTestModel(t)
	m.resize(110, 34)
	m.historyDir = t.TempDir()
	if err := history.AppendUsage(m.historyDir, history.UsageRecord{
		Time: time.Now(), Provider: "mock", Model: "demo-model", PromptTokens: 100, CompletionTokens: 250,
	}); err != nil {
		t.Fatal(err)
	}
	cmdUsage(m, "")
	if !m.modalActive() || !m.usageState.active {
		t.Fatal("/usage did not open as a dialog")
	}
	height := m.modal.innerH
	if height != m.modal.maxInnerH {
		t.Fatalf("usage dialog height = %d, want the full %d so tabs do not resize it", height, m.modal.maxInnerH)
	}
	assertFrameFits(t, m)
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.usageState.tab != usageTabAllTime || !strings.Contains(ansi.Strip(m.render()), "tokens per day") {
		t.Fatal("→ did not switch to the All time tab inside the dialog")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.usageState.tab != usageTabActivity {
		t.Fatal("← did not switch back")
	}
	before := m.usageState.rangeSel
	m.Update(tea.KeyPressMsg{Text: "r", Code: 'r'})
	if m.usageState.rangeSel == before {
		t.Fatal("r did not cycle the range")
	}
	if m.modal.innerH != height || !m.modalActive() {
		t.Fatal("switching tabs or range must keep the dialog open at the same size")
	}
	assertFrameFits(t, m)
	frame := ansi.Strip(m.render())
	if strings.Contains(frame, "esc to close · ← → switch tab") {
		t.Fatal("the in-body footer should move to the dialog border")
	}
}

func TestStatsContextConfigMemoryOpenAsDialogs(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 30)
	cases := []struct {
		title string
		open  func()
	}{
		{"Session statistics", func() {
			m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
			typeText(m, "/stats")
			m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		}},
		{"Session statistics", func() { cmdUsage(m, "session") }},
		{"Context", func() { cmdContext(m, "") }},
		{"Context · summary", func() { cmdContext(m, "summary") }},
		{"Context · preview", func() { cmdContext(m, "preview") }},
		{"Configuration", func() { cmdConfig(m, "") }},
	}
	for _, tc := range cases {
		m.closeOverlay()
		tc.open()
		if !m.modalActive() || !strings.Contains(ansi.Strip(m.render()), tc.title) {
			t.Fatalf("%s did not open as a dialog", tc.title)
		}
		for i, line := range strings.Split(m.viewport.View(), "\n") {
			if w := ansi.StringWidth(line); w > m.modal.innerW {
				t.Fatalf("%s: body line %d is %d wide, dialog body is %d", tc.title, i, w, m.modal.innerW)
			}
		}
		assertFrameFits(t, m)
	}
	m.closeOverlay()
	if cmd := cmdMemory(m, "status"); cmd == nil {
		if !m.modalActive() || !strings.Contains(ansi.Strip(m.render()), "Memory · status") {
			t.Fatal("/memory status did not open as a dialog")
		}
		m.closeOverlay()
		cmdMemory(m, "list")
		if !m.modalActive() || !strings.Contains(ansi.Strip(m.render()), "Memory") {
			t.Fatal("/memory list did not open as a dialog")
		}
		assertFrameFits(t, m)
	}
}
