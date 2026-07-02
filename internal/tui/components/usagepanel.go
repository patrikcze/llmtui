package components

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// UsagePanelData feeds the usage chart panel.
type UsagePanelData struct {
	TokenHistory []int
	PromptTotal  int
	ReplyTotal   int
	Estimated    bool
	ASCIIOnly    bool
}

// UsagePanel renders the token usage sparkline with totals underneath.
func UsagePanel(t styles.Theme, d UsagePanelData, width int) string {
	inner := width - 4 // panel border + padding
	if inner < 8 {
		inner = 8
	}

	chart := t.ChartBar.Render(Sparkline(d.TokenHistory, inner, d.ASCIIOnly))

	label := fmt.Sprintf("usage  prompt %d · reply %d · total %d", d.PromptTotal, d.ReplyTotal, d.PromptTotal+d.ReplyTotal)
	if d.Estimated {
		label += " (estimated)"
	}
	stats := t.ChartLabel.Render(truncate(label, inner))

	content := lipgloss.JoinVertical(lipgloss.Left, chart, stats)
	return t.Panel.Width(width - 2).Render(content)
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}
