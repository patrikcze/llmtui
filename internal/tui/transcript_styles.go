package tui

import (
	"strings"

	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// renderPrompt marks only human prompts with a heavy left rail. Keeping the
// rest of the transcript borderless avoids spending rows on decorative UI and
// lets answers read like ordinary terminal output.
func (m *Model) renderPrompt(body string) string {
	body = strings.Trim(body, "\n")
	width := m.viewport.Width - m.theme.PromptRail.GetHorizontalFrameSize()
	if width < 1 {
		width = 1
	}
	return m.theme.PromptRail.Copy().Width(width).Render(body)
}

func (m *Model) renderReasoning(reasoning string, streaming bool) string {
	label := "thought"
	if streaming {
		label += " · streaming"
	}
	if !m.showReasoning {
		return m.theme.ReasoningText.Render(label + " · hidden · /thoughts show")
	}
	body := strings.Trim(terminaltext.Sanitize(reasoning), "\n")
	if body == "" {
		return m.theme.ReasoningText.Render(label)
	}
	return m.theme.ReasoningText.Render(label + "\n" + body)
}

func (m *Model) renderAnswer(answer string) string {
	return m.theme.AnswerText.Render(strings.TrimRight(answer, "\n"))
}
