package components

import (
	"image/color"
	"testing"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

func TestWorkPulseFollowsTheme(t *testing.T) {
	claude := WorkPulse(styles.ClaudeInspired())
	midnight := WorkPulse(styles.Midnight())
	if len(claude) != 4 || len(midnight) != 4 {
		t.Fatalf("WorkPulse should return a 4-frame gradient, got %d and %d frames", len(claude), len(midnight))
	}
	if sameColor(claude[0], midnight[0]) {
		t.Error("WorkPulse base frame should differ between themes with different Accent colors")
	}
	if sameColor(claude[0], claude[2]) {
		t.Error("WorkPulse should vary across frames to produce a breathing effect")
	}
}

func TestStopButtonFollowsThemeBad(t *testing.T) {
	a := StopButton(styles.ClaudeInspired(), 0)
	b := StopButton(styles.Midnight(), 0)
	if a == "" || b == "" {
		t.Fatal("StopButton rendered empty output")
	}
	// Different themes have different Bad colors, so the rendered ANSI
	// (which embeds the foreground color) should differ even though the
	// glyph/label text is identical.
	if a == b {
		t.Error("StopButton should render differently across themes with different Bad colors")
	}
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
