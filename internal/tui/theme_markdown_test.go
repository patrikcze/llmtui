package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// The answer renderer must use the theme's own Markdown style when it has
// one (hud uppercases headings) and glamour's standard style otherwise.
func TestRendererFollowsThemeMarkdownStyle(t *testing.T) {
	const src = "## Status report\n\nAll systems nominal.\n"
	cases := []struct {
		theme string
		want  string
	}{
		{theme: "claude_inspired", want: "Status report"},
		{theme: "hud", want: "STATUS REPORT"},
	}
	for _, tc := range cases {
		t.Run(tc.theme, func(t *testing.T) {
			m := newTestModel(t)
			m.cfg.UI.Markdown = true
			m.theme = styles.ByName(tc.theme)
			m.renderWidth = 0 // force resize to rebuild the renderer
			m.resize(100, 40)

			out := ansi.Strip(m.renderMarkdown(src))
			if !strings.Contains(out, tc.want) {
				t.Fatalf("rendered heading = %q, want it to contain %q", out, tc.want)
			}
		})
	}
}
