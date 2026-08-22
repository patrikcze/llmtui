// Package styles defines the visual theme for the TUI. Colors are adaptive
// so the UI stays readable on light and dark terminals, and lipgloss
// automatically degrades TrueColor values on limited terminals.
package styles

import (
	"image/color"
	"os"
	"sync"

	"charm.land/lipgloss/v2"
)

// adaptiveColor is a {Light, Dark} hex pair, the local stand-in for lipgloss
// v1's removed AdaptiveColor type. newTheme resolves each pair once (not
// per-render) via HasDarkBackground, matching v1's effective per-session
// behavior.
type adaptiveColor struct {
	Light, Dark string
}

var (
	isDarkOnce   sync.Once
	isDarkCached bool
)

// IsDark reports whether the terminal has a dark background, queried once
// per process and cached: HasDarkBackground does a live terminal round-trip
// on a real tty, and every AdaptiveColor-equivalent resolution site (themes,
// the textarea input box, dot/pulse palettes) needs the same answer, not a
// fresh query each.
func IsDark() bool {
	isDarkOnce.Do(func() {
		isDarkCached = lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
	})
	return isDarkCached
}

// Theme groups every style the TUI needs.
type Theme struct {
	Name string

	Accent    color.Color
	Subtle    color.Color
	Text      color.Color
	Faint     color.Color
	Good      color.Color
	Bad       color.Color
	PanelEdge color.Color
	UserEdge  color.Color

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
func newTheme(name string, accent, subtle, text, faint, good, bad, panelEdge, userEdge adaptiveColor) Theme {
	pick := lipgloss.LightDark(IsDark())
	resolve := func(c adaptiveColor) color.Color {
		return pick(lipgloss.Color(c.Light), lipgloss.Color(c.Dark))
	}

	t := Theme{
		Name:      name,
		Accent:    resolve(accent),
		Subtle:    resolve(subtle),
		Text:      resolve(text),
		Faint:     resolve(faint),
		Good:      resolve(good),
		Bad:       resolve(bad),
		PanelEdge: resolve(panelEdge),
		UserEdge:  resolve(userEdge),
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
		adaptiveColor{Light: "#B4551F", Dark: "#E58E54"}, // Accent: burnt orange / warm peach
		adaptiveColor{Light: "#8A8580", Dark: "#6E6A65"}, // Subtle
		adaptiveColor{Light: "#2A2622", Dark: "#DDD8D2"}, // Text
		adaptiveColor{Light: "#A8A29B", Dark: "#57534E"}, // Faint
		adaptiveColor{Light: "#3D7A45", Dark: "#7CBF85"}, // Good
		adaptiveColor{Light: "#B03A30", Dark: "#E07870"}, // Bad
		adaptiveColor{Light: "#D6D0C8", Dark: "#3F3B37"}, // PanelEdge
		adaptiveColor{Light: "#2563B8", Dark: "#58A6FF"}, // UserEdge: blue, pops against the warm accent
	)
}

// Midnight returns a cool, sleek theme: indigo accent, cyan user rail, dark-native.
func Midnight() Theme {
	return newTheme("midnight",
		adaptiveColor{Light: "#4B4FB8", Dark: "#8C85FF"}, // Accent: indigo / electric periwinkle
		adaptiveColor{Light: "#7B8394", Dark: "#5B6270"}, // Subtle: cool slate
		adaptiveColor{Light: "#1E2230", Dark: "#D8DBE8"}, // Text: cool near-black / cool off-white
		adaptiveColor{Light: "#A0A6B5", Dark: "#4A4F5E"}, // Faint
		adaptiveColor{Light: "#2F7A52", Dark: "#6FCB93"}, // Good: clean green, kept distinct from indigo
		adaptiveColor{Light: "#C13B3B", Dark: "#F0827A"}, // Bad: warm red, unambiguous against the cool palette
		adaptiveColor{Light: "#D2D5DE", Dark: "#363B4A"}, // PanelEdge: dark slate border
		adaptiveColor{Light: "#0E7C86", Dark: "#4FD1D9"}, // UserEdge: cyan, pops against the indigo accent
	)
}

// Forest returns a calm, natural theme: mossy-olive accent, warm amber user rail.
func Forest() Theme {
	return newTheme("forest",
		adaptiveColor{Light: "#5C6B2F", Dark: "#9BB26A"}, // Accent: deep moss / olive green
		adaptiveColor{Light: "#8B8A78", Dark: "#6B6A58"}, // Subtle: warm sage
		adaptiveColor{Light: "#24261E", Dark: "#DEDCC8"}, // Text: warm near-black / warm off-white
		adaptiveColor{Light: "#A6A38C", Dark: "#54523F"}, // Faint
		adaptiveColor{Light: "#2E7D4F", Dark: "#7ED9A0"}, // Good: brighter forest green, distinct from the mossy accent
		adaptiveColor{Light: "#B33A2E", Dark: "#E58579"}, // Bad: terracotta red
		adaptiveColor{Light: "#D6D2BE", Dark: "#43412F"}, // PanelEdge: dark moss border
		adaptiveColor{Light: "#A6740A", Dark: "#E8B84B"}, // UserEdge: warm amber/gold, pops against the olive accent
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
