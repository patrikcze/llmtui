package tui

import (
	"image/color"
	"testing"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// TestNoticeBadgeClassifiesByLeadingGlyph locks in the convention every
// m.notice assignment already follows, so the footer badge (success, error,
// warning, neutral) actually matches what the notice text says instead of
// always rendering as a success badge.
func TestNoticeBadgeClassifiesByLeadingGlyph(t *testing.T) {
	th := styles.ClaudeInspired()

	cases := []struct {
		notice string
		want   color.Color
	}{
		{"✓ copied last reply (128 chars)", th.BadgeOK.GetForeground()},
		{"✗ denied 1 tool call(s)", th.BadgeErr.GetForeground()},
		{"⚒ approve 2 tool action(s)?", th.BadgeWarn.GetForeground()},
		{"⚠ workspace plugin — review before enabling", th.BadgeWarn.GetForeground()},
		{"resumed session (4 messages, 1.2k/32k)", th.SystemNote.GetForeground()},
		{"", th.SystemNote.GetForeground()},
	}

	for _, c := range cases {
		got := noticeBadge(th, c.notice).GetForeground()
		if !sameColor(got, c.want) {
			t.Errorf("noticeBadge(%q) foreground = %v, want %v", c.notice, got, c.want)
		}
	}
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
