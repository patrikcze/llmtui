package styles

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestByNameResolvesEveryBuiltInTheme(t *testing.T) {
	cases := map[string]string{
		"claude_inspired": "claude_inspired",
		"midnight":        "midnight",
		"forest":          "forest",
	}
	for input, wantName := range cases {
		if got := ByName(input).Name; got != wantName {
			t.Errorf("ByName(%q).Name = %q, want %q", input, got, wantName)
		}
	}
}

func TestByNameFallsBackToDefaultForUnknownNames(t *testing.T) {
	for _, input := range []string{"", "not-a-real-theme", "Midnight", "CLAUDE_INSPIRED"} {
		if got := ByName(input).Name; got != "claude_inspired" {
			t.Errorf("ByName(%q).Name = %q, want fallback %q", input, got, "claude_inspired")
		}
	}
}

// Every theme must fully populate its palette and derived styles — an empty
// AdaptiveColor would silently render as the terminal's default foreground,
// which is easy to miss visually but easy to catch here.
func TestBuiltInThemesHaveNoEmptyColors(t *testing.T) {
	for _, theme := range []Theme{ClaudeInspired(), Midnight(), Forest()} {
		colors := map[string]lipgloss.AdaptiveColor{
			"Accent": theme.Accent, "Subtle": theme.Subtle, "Text": theme.Text,
			"Faint": theme.Faint, "Good": theme.Good, "Bad": theme.Bad,
			"PanelEdge": theme.PanelEdge, "UserEdge": theme.UserEdge,
		}
		for field, color := range colors {
			if color.Light == "" || color.Dark == "" {
				t.Errorf("%s.%s has an empty AdaptiveColor variant: %+v", theme.Name, field, color)
			}
		}
	}
}
