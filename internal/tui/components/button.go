package components

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// WorkPulse returns the current theme's four-frame "in progress" breathing
// gradient, built from the theme's own Accent so the animation follows
// whichever theme is active instead of one hardcoded hue.
func WorkPulse(t styles.Theme) []color.Color {
	return pulseFrom(t.Accent)
}

// pulseFrom derives a four-frame breathing gradient (base, brighter,
// brightest, brighter) from a single theme color.
func pulseFrom(base color.Color) []color.Color {
	return []color.Color{
		base,
		lipgloss.Lighten(base, 0.18),
		lipgloss.Lighten(base, 0.35),
		lipgloss.Lighten(base, 0.18),
	}
}

// PulseButton renders a small glowing action chip, e.g. "▣ stop · esc".
// frame advances the pulse animation; pass a monotonically increasing tick.
func PulseButton(t styles.Theme, icon, label string, palette []color.Color, frame int) string {
	c := palette[frame%len(palette)]
	edge := lipgloss.NewStyle().Foreground(c)
	body := lipgloss.NewStyle().Foreground(c).Bold(true)
	return edge.Render("⟨") + body.Render(icon+" "+label) + edge.Render("⟩")
}

// StopButton renders the pulsing stop control shown while generating, using
// the theme's Bad color so the "stop" glow follows the active theme.
func StopButton(t styles.Theme, frame int) string {
	return PulseButton(t, "▣", "stop · esc", pulseFrom(t.Bad), frame)
}
