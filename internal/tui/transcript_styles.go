package tui

import (
	"fmt"
	"strings"
	"time"

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
func (m *Model) renderReasoning(reasoning string, streaming bool, duration time.Duration) string {
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
		return m.theme.ReasoningText.Render(header + " · /thoughts show")
	}
	body := strings.Trim(terminaltext.Sanitize(reasoning), "\n")
	if body == "" {
		return m.theme.ReasoningText.Render(header)
	}
	return m.theme.ReasoningText.Render(header + "\n" + body)
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
