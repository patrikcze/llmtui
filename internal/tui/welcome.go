package tui

import (
	"charm.land/lipgloss/v2"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// renderWelcomePanel uses only transcript-cache state. It gives way to the
// conversation after the first message; the workspace disclosure stays.
func (m *Model) renderWelcomePanel() string {
	accent := lipgloss.NewStyle().Foreground(m.theme.Accent).Bold(true)
	text := lipgloss.NewStyle().Foreground(m.theme.Text)
	muted := lipgloss.NewStyle().Foreground(m.theme.Subtle)
	title := accent.Render("llmtui") + text.Render("  /  Welcome back")
	model := terminaltext.Sanitize(m.model)
	if model == "" {
		model = "Choose a model with /models"
	}
	details := title + "\n" + muted.Render(model) + "\n\n" +
		accent.Render("Get started") + "\n" +
		text.Render("Type a message to begin") + "\n" +
		muted.Render("/models  models   /history  sessions\n/help    commands and shortcuts")
	if m.width >= 76 {
		logo := accent.Render("  ▄▄▄▄▄▄▄  \n  ██       \n  ██       ") + "\n" +
			lipgloss.NewStyle().Foreground(m.theme.Good).Render("  ██       \n  ███████  \n  ▀▀▀▀▀▀▀  ")
		rail := lipgloss.NewStyle().BorderRight(true).BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(m.theme.PanelEdge).Padding(0, 3, 0, 1).Render(logo)
		details = lipgloss.JoinHorizontal(lipgloss.Top, rail, "  ", details)
	}
	details += "\n\n" + m.workspaceStatus()
	return m.welcomeFrame(details)
}

func (m *Model) renderWorkspacePanel() string {
	return m.welcomeFrame(m.workspaceStatus())
}

// Keep the full root and the approval policy visible, including after a
// conversation starts. Styling must not hide the scope of enabled tools.
func (m *Model) workspaceStatus() string {
	label := lipgloss.NewStyle().Foreground(m.theme.Accent).Bold(true)
	text := lipgloss.NewStyle().Foreground(m.theme.Text)
	if !m.toolsOn || m.toolRunner == nil {
		return label.Render("Workspace tools off") + text.Render("  ·  /tools on to enable")
	}
	policy := "asks before writes & commands"
	policyStyle := lipgloss.NewStyle().Foreground(m.theme.Good)
	if m.toolsAutoApprove {
		policy = "auto-approve"
		policyStyle = policyStyle.Foreground(m.theme.Warning).Bold(true)
	}
	return label.Render("Workspace tools on") + "  ·  " + policyStyle.Render(policy) + "\n" +
		text.Render("Files and commands are limited to:") + "\n" +
		text.Render(terminaltext.Sanitize(m.toolRunner.Root())) + "\n" +
		lipgloss.NewStyle().Foreground(m.theme.Subtle).Render("/tools off to disable")
}

func (m *Model) welcomeFrame(content string) string {
	width := max(1, m.width)
	if width < 8 {
		return lipgloss.NewStyle().Width(width).Render(content)
	}
	// Width includes padding; the two border cells are additional.
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.PanelEdge).Padding(1, 2).
		Width(width - 2).Render(content)
}

func (m *Model) hasConversation() bool {
	for _, msg := range m.session.Messages {
		if msg.Role != provider.RoleSystem {
			return true
		}
	}
	return false
}
