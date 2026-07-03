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
	Profile      string
	PromptMode   string
	Template     string
	CacheOn      bool
	SummaryOn    bool
	ToolsOn      bool
	WebOn        bool
}

// StatusBar renders the status bar: one line when everything fits, two rows
// on narrower terminals so indicators wrap instead of being cut off. When
// even two rows cannot hold everything, the model name — the only unbounded
// field — is shortened first, so the small indicators always stay visible.
func StatusBar(t styles.Theme, d StatusBarData, width int) string {
	parts := statusParts(t, d)
	sep := t.StatusKey.Render(" │ ")

	line := strings.Join(parts, sep)
	if lipgloss.Width(line) <= width {
		return line
	}

	best, over := bestSplit(parts, sep, width)
	for over > 0 {
		shortened := shortenModel(d, over)
		if shortened == d.Model {
			break // model is already at its minimum; give up
		}
		d.Model = shortened
		parts = statusParts(t, d)
		best, over = bestSplit(parts, sep, width)
	}

	// The final cap is a last resort for pathological widths.
	capped := lipgloss.NewStyle().MaxWidth(width)
	return capped.Render(strings.Join(parts[:best], sep)) + "\n" +
		capped.Render(strings.Join(parts[best:], sep))
}

// statusParts builds the styled status segments in display order.
func statusParts(t styles.Theme, d StatusBarData) []string {
	dot := t.BadgeOK.Render("●")
	state := t.BadgeOK.Render("online")
	if d.DemoMode {
		dot = t.BadgeWarn.Render("●")
		state = t.BadgeWarn.Render("demo")
	} else if !d.Connected {
		dot = t.BadgeWarn.Render("●")
		state = t.BadgeWarn.Render("offline")
	}

	parts := []string{
		fmt.Sprintf("%s %s", dot, state),
		t.StatusKey.Render("provider ") + t.StatusValue.Render(d.Provider),
		t.StatusKey.Render("model ") + t.StatusValue.Render(d.Model),
	}

	if d.Profile != "" {
		parts = append(parts, t.StatusKey.Render("profile ")+t.StatusValue.Render(d.Profile))
	}
	if d.PromptMode != "" {
		parts = append(parts, t.StatusKey.Render("prompt ")+t.StatusValue.Render(d.PromptMode))
	}
	if d.Template != "" {
		parts = append(parts, t.StatusKey.Render("template ")+t.StatusValue.Render(d.Template))
	}
	if d.ContextLimit > 0 {
		ctx := FormatTokens(d.ContextUsed) + "/" + FormatTokens(d.ContextLimit)
		if d.SummaryOn {
			ctx += "·sum"
		}
		parts = append(parts, t.StatusKey.Render("ctx ")+t.StatusValue.Render(ctx))
	}
	if d.CacheOn {
		parts = append(parts, t.StatusKey.Render("cache ")+t.StatusValue.Render("on"))
	}
	if d.ToolsOn {
		parts = append(parts, t.StatusKey.Render("tools ")+t.BadgeOK.Render("on"))
	}
	if d.WebOn {
		parts = append(parts, t.StatusKey.Render("web ")+t.BadgeOK.Render("on"))
	}

	tokens := fmt.Sprintf("%d tok", d.TotalTokens)
	if d.Estimated {
		tokens += "~"
	}
	parts = append(parts, t.StatusKey.Render("session ")+t.StatusValue.Render(tokens))
	if d.LastTPS > 0 {
		parts = append(parts, t.StatusKey.Render("speed ")+t.StatusValue.Render(fmt.Sprintf("%.1f tok/s", d.LastTPS)))
	}
	return parts
}

// bestSplit picks the boundary that lets both rows fit, or failing that the
// one with the least total overflow. It returns that overflow so the caller
// can decide whether shortening is needed.
func bestSplit(parts []string, sep string, width int) (split, overflow int) {
	rowW := func(ps []string) int { return lipgloss.Width(strings.Join(ps, sep)) }
	best, bestOver := 1, -1
	for s := 1; s < len(parts); s++ {
		over := max(rowW(parts[:s])-width, 0) + max(rowW(parts[s:])-width, 0)
		if bestOver < 0 || over < bestOver {
			best, bestOver = s, over
		}
		if over == 0 {
			break
		}
	}
	return best, bestOver
}

// shortenModel trims the model name by the given overflow (plus an ellipsis),
// keeping the tail — the tag and quant suffix are the distinguishing end of
// long registry paths like hf.co/org/Model-9B-GGUF:Q4_K_M.
func shortenModel(d StatusBarData, over int) string {
	r := []rune(d.Model)
	keep := len(r) - over - 1
	if keep < 10 {
		keep = 10
	}
	if keep >= len(r) {
		return d.Model
	}
	return "…" + string(r[len(r)-keep:])
}
