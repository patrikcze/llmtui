package styles

import (
	"image/color"
	"testing"
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

// Every theme must fully populate its palette and derived styles — a missing
// adaptiveColor pair would resolve to a nil color.Color and silently render
// as the terminal's default foreground, which is easy to miss visually but
// easy to catch here.
func TestBuiltInThemesHaveNoEmptyColors(t *testing.T) {
	for _, theme := range []Theme{ClaudeInspired(), Midnight(), Forest()} {
		colors := map[string]color.Color{
			"Accent": theme.Accent, "Subtle": theme.Subtle, "Text": theme.Text,
			"Faint": theme.Faint, "Good": theme.Good, "Bad": theme.Bad,
			"PanelEdge": theme.PanelEdge, "UserEdge": theme.UserEdge,
		}
		for field, c := range colors {
			if c == nil {
				t.Errorf("%s.%s resolved to a nil color", theme.Name, field)
			}
		}
	}
}
