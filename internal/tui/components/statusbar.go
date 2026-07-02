package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// StatusBarData is everything the status bar displays.
type StatusBarData struct {
	Provider     string
	Model        string
	Connected    bool
	DemoMode     bool
	TotalTokens  int
	LastTPS      float64
	Estimated    bool
	ContextUsed  int
	ContextLimit int
}

// StatusBar renders the one-line status bar.
func StatusBar(t styles.Theme, d StatusBarData, width int) string {
	dot := t.BadgeOK.Render("●")
	state := t.BadgeOK.Render("online")
	if d.DemoMode {
		dot = t.BadgeWarn.Render("●")
		state = t.BadgeWarn.Render("demo")
	} else if !d.Connected {
		dot = t.BadgeWarn.Render("●")
		state = t.BadgeWarn.Render("offline")
	}

	sep := t.StatusKey.Render(" │ ")
	parts := []string{
		fmt.Sprintf("%s %s", dot, state),
		t.StatusKey.Render("provider ") + t.StatusValue.Render(d.Provider),
		t.StatusKey.Render("model ") + t.StatusValue.Render(d.Model),
	}

	tokens := fmt.Sprintf("%d tok", d.TotalTokens)
	if d.Estimated {
		tokens += "~"
	}
	parts = append(parts, t.StatusKey.Render("session ")+t.StatusValue.Render(tokens))
	if d.LastTPS > 0 {
		parts = append(parts, t.StatusKey.Render("speed ")+t.StatusValue.Render(fmt.Sprintf("%.1f tok/s", d.LastTPS)))
	}

	line := strings.Join(parts, sep)
	return lipgloss.NewStyle().MaxWidth(width).Render(line)
}
