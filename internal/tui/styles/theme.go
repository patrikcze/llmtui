// Package styles defines the visual theme for the TUI. Colors are adaptive
// so the UI stays readable on light and dark terminals, and lipgloss
// automatically degrades TrueColor values on limited terminals.
package styles

import "github.com/charmbracelet/lipgloss"

// Theme groups every style the TUI needs.
type Theme struct {
	Name string

	Accent    lipgloss.AdaptiveColor
	Subtle    lipgloss.AdaptiveColor
	Text      lipgloss.AdaptiveColor
	Faint     lipgloss.AdaptiveColor
	Good      lipgloss.AdaptiveColor
	Bad       lipgloss.AdaptiveColor
	PanelEdge lipgloss.AdaptiveColor
	UserEdge  lipgloss.AdaptiveColor
	Thought   lipgloss.AdaptiveColor
	Tool      lipgloss.AdaptiveColor

	UserLabel      lipgloss.Style
	AssistantLabel lipgloss.Style
	SystemNote     lipgloss.Style
	StatusBar      lipgloss.Style
	StatusKey      lipgloss.Style
	StatusValue    lipgloss.Style
	Badge          lipgloss.Style
	BadgeOK        lipgloss.Style
	BadgeWarn      lipgloss.Style
	Panel          lipgloss.Style
	InputPanel     lipgloss.Style
	UserCard       lipgloss.Style
	AssistantCard  lipgloss.Style
	ReasoningCard  lipgloss.Style
	ToolCard       lipgloss.Style
	SystemCard     lipgloss.Style
	ErrorCard      lipgloss.Style
	HelpFooter     lipgloss.Style
	Spinner        lipgloss.Style
	ErrorText      lipgloss.Style
	ChartBar       lipgloss.Style
	ChartLabel     lipgloss.Style
}

// ClaudeInspired returns the default theme: calm, warm-accented, terminal-native.
func ClaudeInspired() Theme {
	t := Theme{
		Name:      "claude_inspired",
		Accent:    lipgloss.AdaptiveColor{Light: "#B4551F", Dark: "#E58E54"},
		Subtle:    lipgloss.AdaptiveColor{Light: "#8A8580", Dark: "#6E6A65"},
		Text:      lipgloss.AdaptiveColor{Light: "#2A2622", Dark: "#DDD8D2"},
		Faint:     lipgloss.AdaptiveColor{Light: "#A8A29B", Dark: "#57534E"},
		Good:      lipgloss.AdaptiveColor{Light: "#3D7A45", Dark: "#7CBF85"},
		Bad:       lipgloss.AdaptiveColor{Light: "#B03A30", Dark: "#E07870"},
		PanelEdge: lipgloss.AdaptiveColor{Light: "#D6D0C8", Dark: "#3F3B37"},
		UserEdge:  lipgloss.AdaptiveColor{Light: "#2563B8", Dark: "#58A6FF"},
		Thought:   lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#D29922"},
		Tool:      lipgloss.AdaptiveColor{Light: "#6F42C1", Dark: "#A98BFA"},
	}

	t.UserLabel = lipgloss.NewStyle().Bold(true).Foreground(t.Accent)
	t.AssistantLabel = lipgloss.NewStyle().Bold(true).Foreground(t.Good)
	t.SystemNote = lipgloss.NewStyle().Foreground(t.Subtle).Italic(true)
	t.StatusBar = lipgloss.NewStyle().Foreground(t.Subtle)
	t.StatusKey = lipgloss.NewStyle().Foreground(t.Faint)
	t.StatusValue = lipgloss.NewStyle().Foreground(t.Text)
	t.Badge = lipgloss.NewStyle().Foreground(t.Text).Bold(true)
	t.BadgeOK = lipgloss.NewStyle().Foreground(t.Good).Bold(true)
	t.BadgeWarn = lipgloss.NewStyle().Foreground(t.Bad).Bold(true)
	t.Panel = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.PanelEdge).
		Padding(0, 1)
	t.InputPanel = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.Accent).
		Padding(0, 1)
	card := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	t.UserCard = card.Copy().
		BorderForeground(t.UserEdge).
		Background(lipgloss.AdaptiveColor{Light: "#EFF6FF", Dark: "#111A24"})
	t.AssistantCard = card.Copy().
		BorderForeground(t.Good).
		Background(lipgloss.AdaptiveColor{Light: "#F1F7F2", Dark: "#121B15"})
	t.ReasoningCard = card.Copy().
		BorderForeground(t.Thought).
		Background(lipgloss.AdaptiveColor{Light: "#FFF8E7", Dark: "#211B10"})
	t.ToolCard = card.Copy().
		BorderForeground(t.Tool).
		Background(lipgloss.AdaptiveColor{Light: "#F6F3FF", Dark: "#181523"})
	t.SystemCard = card.Copy().
		BorderForeground(t.PanelEdge).
		Background(lipgloss.AdaptiveColor{Light: "#F7F5F2", Dark: "#181614"})
	t.ErrorCard = card.Copy().
		BorderForeground(t.Bad).
		Background(lipgloss.AdaptiveColor{Light: "#FFF1F0", Dark: "#251313"})
	t.HelpFooter = lipgloss.NewStyle().Foreground(t.Faint)
	t.Spinner = lipgloss.NewStyle().Foreground(t.Accent)
	t.ErrorText = lipgloss.NewStyle().Foreground(t.Bad)
	t.ChartBar = lipgloss.NewStyle().Foreground(t.Accent)
	t.ChartLabel = lipgloss.NewStyle().Foreground(t.Faint)

	return t
}

// ByName resolves a theme by config name, falling back to the default.
func ByName(name string) Theme {
	switch name {
	default:
		return ClaudeInspired()
	}
}
