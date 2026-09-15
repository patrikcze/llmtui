package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestMainFrameFitsLayoutMatrix(t *testing.T) {
	for _, size := range [][2]int{{60, 6}, {60, 10}, {80, 12}, {80, 18}, {100, 24}, {120, 30}, {160, 40}, {200, 40}} {
		t.Run(frameSizeName(size[0], size[1]), func(t *testing.T) {
			m := newTestModel(t)
			m.cfg.UI.ShowUsageChart = true
			m.cfg.UI.ShowTokenStats = true
			m.session.AddUser("A short prompt")
			m.session.AddAssistant("A short response")
			m.resize(size[0], size[1])

			frame := ansi.Strip(m.render())
			if got := lipgloss.Height(frame); got > size[1] {
				t.Errorf("frame height = %d, terminal height = %d:\n%s", got, size[1], frame)
			}
			for row, line := range strings.Split(frame, "\n") {
				if got := lipgloss.Width(line); got > size[0] {
					t.Errorf("row %d width = %d, terminal width = %d: %q", row, got, size[0], line)
				}
			}
		})
	}
}

func TestLayoutRespectsChromeSettings(t *testing.T) {
	m := newTestModel(t)
	m.cfg.UI.ShowUsageChart = false
	m.cfg.UI.ShowTokenStats = false
	m.cfg.UI.CompactMode = true
	m.model = "registry.example.com/very-long-model-name:Q4_K_M"
	m.attachments = []provider.Image{{Data: []byte("one")}, {Data: []byte("two")}}
	m.resize(100, 40)

	frame := ansi.Strip(m.render())
	if strings.Contains(frame, "usage  prompt") {
		t.Error("usage chart rendered while ui.show_usage_chart is false")
	}
	if strings.Contains(frame, "session ") || strings.Contains(frame, "speed ") {
		t.Error("token telemetry rendered while ui.show_token_stats is false")
	}
	if !strings.Contains(frame, "2 images") || strings.Contains(frame, "image 1") {
		t.Errorf("compact attachment summary missing:\n%s", frame)
	}
}

func TestRenderDoesNotMutateLayout(t *testing.T) {
	m := newTestModel(t)
	m.resize(80, 24)
	before := m.layout
	beforeStatusLines := m.statusLines
	beforeViewportHeight := m.viewport.Height()

	_ = m.render()

	if m.layout != before || m.statusLines != beforeStatusLines || m.viewport.Height() != beforeViewportHeight {
		t.Fatalf("render mutated layout: before=%+v/%d/%d after=%+v/%d/%d",
			before, beforeStatusLines, beforeViewportHeight,
			m.layout, m.statusLines, m.viewport.Height())
	}
}

func frameSizeName(width, height int) string {
	return fmt.Sprintf("%dx%d", width, height)
}
