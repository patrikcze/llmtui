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
	PromptRail     lipgloss.Style
	ReasoningText  lipgloss.Style
	AnswerText     lipgloss.Style
	HelpFooter     lipgloss.Style
	Spinner        lipgloss.Style
	ErrorText      lipgloss.Style
	ChartBar       lipgloss.Style
	ChartLabel     lipgloss.Style
}

// newTheme builds every derived style from one base palette, so each theme
// only has to declare its eight colors — the styling built on top of them
// (borders, weights, italics) stays identical and in one place.
func newTheme(name string, accent, subtle, text, faint, good, bad, panelEdge, userEdge lipgloss.AdaptiveColor) Theme {
	t := Theme{
		Name:      name,
		Accent:    accent,
		Subtle:    subtle,
		Text:      text,
		Faint:     faint,
		Good:      good,
		Bad:       bad,
		PanelEdge: panelEdge,
		UserEdge:  userEdge,
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
	t.PromptRail = lipgloss.NewStyle().
		Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(t.UserEdge).
		Foreground(t.Text).
		PaddingLeft(1)
	t.ReasoningText = lipgloss.NewStyle().Foreground(t.Subtle)
	t.AnswerText = lipgloss.NewStyle().Foreground(t.Text)
	t.HelpFooter = lipgloss.NewStyle().Foreground(t.Faint)
	t.Spinner = lipgloss.NewStyle().Foreground(t.Accent)
	t.ErrorText = lipgloss.NewStyle().Foreground(t.Bad)
	t.ChartBar = lipgloss.NewStyle().Foreground(t.Accent)
	t.ChartLabel = lipgloss.NewStyle().Foreground(t.Faint)

	return t
}

// ClaudeInspired returns the default theme: calm, warm-accented, terminal-native.
func ClaudeInspired() Theme {
	return newTheme("claude_inspired",
		lipgloss.AdaptiveColor{Light: "#B4551F", Dark: "#E58E54"}, // Accent: burnt orange / warm peach
		lipgloss.AdaptiveColor{Light: "#8A8580", Dark: "#6E6A65"}, // Subtle
		lipgloss.AdaptiveColor{Light: "#2A2622", Dark: "#DDD8D2"}, // Text
		lipgloss.AdaptiveColor{Light: "#A8A29B", Dark: "#57534E"}, // Faint
		lipgloss.AdaptiveColor{Light: "#3D7A45", Dark: "#7CBF85"}, // Good
		lipgloss.AdaptiveColor{Light: "#B03A30", Dark: "#E07870"}, // Bad
		lipgloss.AdaptiveColor{Light: "#D6D0C8", Dark: "#3F3B37"}, // PanelEdge
		lipgloss.AdaptiveColor{Light: "#2563B8", Dark: "#58A6FF"}, // UserEdge: blue, pops against the warm accent
	)
}

// Midnight returns a cool, sleek theme: indigo accent, cyan user rail, dark-native.
func Midnight() Theme {
	return newTheme("midnight",
		lipgloss.AdaptiveColor{Light: "#4B4FB8", Dark: "#8C85FF"}, // Accent: indigo / electric periwinkle
		lipgloss.AdaptiveColor{Light: "#7B8394", Dark: "#5B6270"}, // Subtle: cool slate
		lipgloss.AdaptiveColor{Light: "#1E2230", Dark: "#D8DBE8"}, // Text: cool near-black / cool off-white
		lipgloss.AdaptiveColor{Light: "#A0A6B5", Dark: "#4A4F5E"}, // Faint
		lipgloss.AdaptiveColor{Light: "#2F7A52", Dark: "#6FCB93"}, // Good: clean green, kept distinct from indigo
		lipgloss.AdaptiveColor{Light: "#C13B3B", Dark: "#F0827A"}, // Bad: warm red, unambiguous against the cool palette
		lipgloss.AdaptiveColor{Light: "#D2D5DE", Dark: "#363B4A"}, // PanelEdge: dark slate border
		lipgloss.AdaptiveColor{Light: "#0E7C86", Dark: "#4FD1D9"}, // UserEdge: cyan, pops against the indigo accent
	)
}

// Forest returns a calm, natural theme: mossy-olive accent, warm amber user rail.
func Forest() Theme {
	return newTheme("forest",
		lipgloss.AdaptiveColor{Light: "#5C6B2F", Dark: "#9BB26A"}, // Accent: deep moss / olive green
		lipgloss.AdaptiveColor{Light: "#8B8A78", Dark: "#6B6A58"}, // Subtle: warm sage
		lipgloss.AdaptiveColor{Light: "#24261E", Dark: "#DEDCC8"}, // Text: warm near-black / warm off-white
		lipgloss.AdaptiveColor{Light: "#A6A38C", Dark: "#54523F"}, // Faint
		lipgloss.AdaptiveColor{Light: "#2E7D4F", Dark: "#7ED9A0"}, // Good: brighter forest green, distinct from the mossy accent
		lipgloss.AdaptiveColor{Light: "#B33A2E", Dark: "#E58579"}, // Bad: terracotta red
		lipgloss.AdaptiveColor{Light: "#D6D2BE", Dark: "#43412F"}, // PanelEdge: dark moss border
		lipgloss.AdaptiveColor{Light: "#A6740A", Dark: "#E8B84B"}, // UserEdge: warm amber/gold, pops against the olive accent
	)
}

// ByName resolves a theme by config name, falling back to the default.
func ByName(name string) Theme {
	switch name {
	case "midnight":
		return Midnight()
	case "forest":
		return Forest()
	default:
		return ClaudeInspired()
	}
}
