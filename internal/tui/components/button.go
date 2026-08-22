package components

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// pulse palettes cycle once per spinner tick to give buttons a soft glow.
var (
	stopPulse = resolvePulse([][2]string{
		{"#B03A30", "#E07870"},
		{"#C4544A", "#EE9089"},
		{"#D96E64", "#F7ABA4"},
		{"#C4544A", "#EE9089"},
	})
	workPulse = resolvePulse([][2]string{
		{"#B4551F", "#E58E54"},
		{"#C96830", "#EFA470"},
		{"#DE7C42", "#F8BA8E"},
		{"#C96830", "#EFA470"},
	})
)

// resolvePulse is the local stand-in for lipgloss v1's removed
// AdaptiveColor: each {Light, Dark} pair is resolved once via
// styles.IsDark instead of per-render.
func resolvePulse(pairs [][2]string) []color.Color {
	pick := lipgloss.LightDark(styles.IsDark())
	colors := make([]color.Color, len(pairs))
	for i, p := range pairs {
		colors[i] = pick(lipgloss.Color(p[0]), lipgloss.Color(p[1]))
	}
	return colors
}

// PulseButton renders a small glowing action chip, e.g. "▣ stop · esc".
// frame advances the pulse animation; pass a monotonically increasing tick.
func PulseButton(t styles.Theme, icon, label string, palette []color.Color, frame int) string {
	c := palette[frame%len(palette)]
	edge := lipgloss.NewStyle().Foreground(c)
	body := lipgloss.NewStyle().Foreground(c).Bold(true)
	return edge.Render("⟨") + body.Render(icon+" "+label) + edge.Render("⟩")
}

// StopButton renders the pulsing stop control shown while generating.
func StopButton(t styles.Theme, frame int) string {
	return PulseButton(t, "▣", "stop · esc", stopPulse, frame)
}
