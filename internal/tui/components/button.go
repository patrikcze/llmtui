package components

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// pulse palettes cycle once per spinner tick to give buttons a soft glow.
var (
	stopPulse = []lipgloss.AdaptiveColor{
		{Light: "#B03A30", Dark: "#E07870"},
		{Light: "#C4544A", Dark: "#EE9089"},
		{Light: "#D96E64", Dark: "#F7ABA4"},
		{Light: "#C4544A", Dark: "#EE9089"},
	}
	workPulse = []lipgloss.AdaptiveColor{
		{Light: "#B4551F", Dark: "#E58E54"},
		{Light: "#C96830", Dark: "#EFA470"},
		{Light: "#DE7C42", Dark: "#F8BA8E"},
		{Light: "#C96830", Dark: "#EFA470"},
	}
)

// PulseButton renders a small glowing action chip, e.g. "▣ stop · esc".
// frame advances the pulse animation; pass a monotonically increasing tick.
func PulseButton(t styles.Theme, icon, label string, palette []lipgloss.AdaptiveColor, frame int) string {
	color := palette[frame%len(palette)]
	edge := lipgloss.NewStyle().Foreground(color)
	body := lipgloss.NewStyle().Foreground(color).Bold(true)
	return edge.Render("⟨") + body.Render(icon+" "+label) + edge.Render("⟩")
}

// StopButton renders the pulsing stop control shown while generating.
func StopButton(t styles.Theme, frame int) string {
	return PulseButton(t, "▣", "stop · esc", stopPulse, frame)
}

// WorkingButton renders the pulsing progress chip with animated dots.
func WorkingButton(t styles.Theme, frame int, elapsed string) string {
	dots := []string{"·  ", "·· ", "···", " ··", "  ·", "   "}
	label := "working" + dots[frame%len(dots)]
	if elapsed != "" {
		label += " " + elapsed
	}
	return PulseButton(t, "◈", label, workPulse, frame)
}
