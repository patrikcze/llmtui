package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// renderConversationCard gives each transcript event a stable visual
// boundary. Terminals cannot draw true curved surfaces, but Lip Gloss's
// rounded border and adaptive background colors provide the same hierarchy
// without depending on a particular terminal color depth.
func (m *Model) renderConversationCard(
	style lipgloss.Style,
	label string,
	labelStyle lipgloss.Style,
	body string,
) string {
	body = strings.Trim(body, "\n")
	content := labelStyle.Render(terminaltext.Sanitize(label))
	if body != "" {
		content += "\n" + body
	}

	width := m.viewport.Width - style.GetHorizontalFrameSize()
	if width < 1 {
		width = 1
	}
	return style.Copy().Width(width).Render(content)
}

func (m *Model) renderReasoningCard(reasoning string, streaming bool) string {
	label := "thought"
	if streaming {
		label += " · streaming"
	}
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Thought)
	body := terminaltext.Sanitize(reasoning)
	if !m.showReasoning {
		body = m.theme.SystemNote.Render("reasoning hidden · /thoughts show")
	}
	return m.renderConversationCard(m.theme.ReasoningCard, label, labelStyle, body)
}
