package styles

import (
	"image/color"
	"math"
	"testing"

	"charm.land/lipgloss/v2"
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
			"Warning": theme.Warning, "PanelEdge": theme.PanelEdge, "UserEdge": theme.UserEdge,
		}
		for field, c := range colors {
			if c == nil {
				t.Errorf("%s.%s resolved to a nil color", theme.Name, field)
			}
		}
	}
}

// Warning must read as its own state, not a recolored Bad or Accent — it is
// the color behind attention/approval badges (BadgeWarn), and collapsing it
// onto an existing color would silently make approvals look like errors (or
// like ordinary activity) again.
func TestBuiltInThemesWarningIsDistinct(t *testing.T) {
	for _, theme := range []Theme{ClaudeInspired(), Midnight(), Forest()} {
		if sameColor(theme.Warning, theme.Bad) {
			t.Errorf("%s: Warning resolves to the same color as Bad", theme.Name)
		}
		if sameColor(theme.Warning, theme.Accent) {
			t.Errorf("%s: Warning resolves to the same color as Accent", theme.Name)
		}
		if sameColor(theme.Warning, theme.Good) {
			t.Errorf("%s: Warning resolves to the same color as Good", theme.Name)
		}
	}
}

// BadgeWarn and BadgeErr must render distinctly: BadgeWarn marks
// attention/approval states, BadgeErr marks actual failures/denials. Before
// this split both drew on Bad, so a pending approval and a denied tool call
// were visually identical.
func TestBadgeWarnAndBadgeErrAreDistinct(t *testing.T) {
	for _, theme := range []Theme{ClaudeInspired(), Midnight(), Forest()} {
		warnFg := theme.BadgeWarn.GetForeground()
		errFg := theme.BadgeErr.GetForeground()
		if sameColor(warnFg, errFg) {
			t.Errorf("%s: BadgeWarn and BadgeErr render with the same color", theme.Name)
		}
	}
}

func TestRelativeLuminanceBoundaries(t *testing.T) {
	if lum := relativeLuminance(lipgloss.Color("#000000")); lum != 0 {
		t.Errorf("relativeLuminance(black) = %v, want 0", lum)
	}
	if lum := relativeLuminance(lipgloss.Color("#FFFFFF")); math.Abs(lum-1) > 1e-9 {
		t.Errorf("relativeLuminance(white) = %v, want 1", lum)
	}
}

func TestContrastForegroundPicksTheHigherContrastOption(t *testing.T) {
	if got := contrastForeground(lipgloss.Color("#000000")); !sameColor(got, lipgloss.Color("#FFFFFF")) {
		t.Errorf("contrastForeground(black) = %v, want white", got)
	}
	if got := contrastForeground(lipgloss.Color("#FFFFFF")); !sameColor(got, lipgloss.Color("#1A1A1A")) {
		t.Errorf("contrastForeground(white) = %v, want near-black", got)
	}
}

// TestTabActiveMeetsWCAGAAForEveryThemeAccent guards the pill styling behind
// an active tab/range selector (see usage.go's usageTabRow/usageRangeRow):
// TabActive fills its background with the theme's Accent color and must
// pick a foreground that stays readable against it. A naive fixed choice
// (always white, or always black) fails WCAG AA on at least one of these
// three themes' Accent colors — that is the whole reason
// contrastForeground exists instead.
func TestTabActiveMeetsWCAGAAForEveryThemeAccent(t *testing.T) {
	const wcagAA = 4.5
	for _, theme := range []Theme{ClaudeInspired(), Midnight(), Forest()} {
		bg := theme.TabActive.GetBackground()
		fg := theme.TabActive.GetForeground()
		if bg == nil || fg == nil {
			t.Fatalf("%s: TabActive has an unset Background/Foreground", theme.Name)
		}
		ratio := contrastRatio(relativeLuminance(fg), relativeLuminance(bg))
		if ratio < wcagAA {
			t.Errorf("%s: TabActive foreground/background contrast = %.2f, want >= %.1f (WCAG AA)", theme.Name, ratio, wcagAA)
		}
	}
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
