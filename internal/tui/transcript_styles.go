package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// renderPrompt marks only human prompts with a heavy left rail. Keeping the
// rest of the transcript borderless avoids spending rows on decorative UI and
// lets answers read like ordinary terminal output.
func (m *Model) renderPrompt(body string) string {
	body = strings.Trim(body, "\n")
	width := m.viewport.Width() - m.theme.PromptRail.GetHorizontalFrameSize()
	if width < 1 {
		width = 1
	}
	return m.theme.PromptRail.Width(width).Render(body)
}

// renderReasoning renders a captured reasoning block's header (and, when
// expanded, its body). The header follows OpenCode's convention: a "+"
// (collapsed) or "-" (expanded) toggle glyph, the word "Thought", and — once
// known — the reasoning phase's duration to one decimal place ("Thought:
// 4.4s"). streaming appends "…" to a still-ticking duration; duration == 0
// means "not known yet" and falls back to a bare "Thought" with no colon
// (e.g. a message saved before this feature existed, or a turn that
// produced no reasoning at all).
//
// zoneID marks the header (only the header, never the body) as a click
// target — see updateReasoningClick — so clicking it toggles m.showReasoning
// the same way /thoughts show|hide does. The header renders in the theme's
// accent color (bold) instead of the body's muted gray, to read as
// interactive — not underlined: lipgloss v2's Width()-constrained Render(),
// which refreshViewport applies to the whole composed transcript afterward,
// re-emits per-character SGR codes for underlined text specifically (verified
// in isolation; plain color/bold text passes through as one contiguous run).
// That's still visually correct but breaks literal-substring test assertions
// against the raw view, so it's avoided rather than worked around per-test.
func (m *Model) renderReasoning(zoneID, reasoning string, streaming bool, duration time.Duration) string {
	glyph := "-"
	if !m.showReasoning {
		glyph = "+"
	}
	header := glyph + " Thought"
	if formatted := formatThoughtDuration(duration); formatted != "" {
		header += ": " + formatted
		if streaming {
			header += "…"
		}
	}
	if !m.showReasoning {
		header += " · click or /thoughts show"
	}
	headerStyle := lipgloss.NewStyle().Foreground(m.theme.Accent).Bold(true)
	styledHeader := zone.Mark(zoneID, headerStyle.Render(header))
	if !m.showReasoning {
		return styledHeader
	}
	body := strings.Trim(terminaltext.Sanitize(reasoning), "\n")
	if body == "" {
		return styledHeader
	}
	return styledHeader + "\n" + m.theme.ReasoningText.Render(body)
}

// formatThoughtDuration renders a reasoning-phase duration the way OpenCode
// does: one decimal place, always in seconds ("4.4s"). Returns "" for a
// non-positive/unknown duration so the caller can fall back to a bare label.
func formatThoughtDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func (m *Model) renderAnswer(answer string) string {
	return m.theme.AnswerText.Render(strings.TrimRight(answer, "\n"))
}
