package components

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

var (
	spinnerFrames      = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")
	spinnerFramesASCII = []rune(`-\|/`)
)

// SpinnerFrame returns the animation frame glyph for a running item. With
// animation off it returns a static bullet so state is still visible.
func SpinnerFrame(frame int, ascii, animated bool) string {
	if !animated {
		if ascii {
			return "*"
		}
		return "•"
	}
	frames := spinnerFrames
	if ascii {
		frames = spinnerFramesASCII
	}
	return string(frames[frame%len(frames)])
}

// FormatElapsed renders a duration the way a human reads a wait: "42s",
// "3m 11s", "1h 02m".
func FormatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// WorkingLine renders the live footer while a request or tool batch runs:
//
//	⠹ Ideating… (3m 11s · ↓ 9.1k tokens · esc to interrupt)
//
// tokens may be empty. When animated the verb pulses through the work
// palette and trails an ellipsis; otherwise it renders statically in the
// spinner style.
func WorkingLine(t styles.Theme, frame int, verb, elapsed, tokens string, ascii, animated bool) string {
	verbStyle := t.Spinner.Bold(true)
	if animated {
		verbStyle = lipgloss.NewStyle().Foreground(workPulse[frame%len(workPulse)]).Bold(true)
		verb += "…"
	}
	parts := []string{elapsed}
	if tokens != "" {
		parts = append(parts, tokens)
	}
	parts = append(parts, "esc to interrupt")
	detail := " (" + strings.Join(parts, " · ") + ")"
	return t.Spinner.Render(SpinnerFrame(frame, ascii, animated)) + " " +
		verbStyle.Render(verb) + t.HelpFooter.Render(detail)
}
