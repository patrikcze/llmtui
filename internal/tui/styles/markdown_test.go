package styles

import (
	"strings"
	"testing"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
	xansi "github.com/charmbracelet/x/ansi"
)

// Only hud carries its own Markdown style; the other themes must keep
// rendering answers with glamour's standard style exactly as before.
func TestOnlyHUDOverridesMarkdownStyle(t *testing.T) {
	for _, theme := range builtInThemes() {
		got := theme.Markdown != nil
		if want := theme.Name == "hud"; got != want {
			t.Errorf("%s: Markdown set = %v, want %v", theme.Name, got, want)
		}
	}
}

// hudMarkdown starts from glamour's shared package-level configs. Writing
// through one of their pointers would recolor every other theme's answers
// for the rest of the process, so building both variants must leave them
// untouched.
func TestHUDMarkdownLeavesGlamourStandardStylesUntouched(t *testing.T) {
	read := func(s *string) string {
		if s == nil {
			return "<nil>"
		}
		return *s
	}
	colors := func(c *ansi.StyleConfig) [4]string {
		return [4]string{read(c.Document.Color), read(c.Heading.Color), read(c.Code.Color), read(c.CodeBlock.Chroma.Keyword.Color)}
	}
	standard := map[string]*ansi.StyleConfig{
		"dark":  &glamourstyles.DarkStyleConfig,
		"light": &glamourstyles.LightStyleConfig,
	}
	before := map[string][4]string{}
	for name, cfg := range standard {
		before[name] = colors(cfg)
	}

	hudMarkdown(true)
	hudMarkdown(false)

	for name, cfg := range standard {
		if got := colors(cfg); got != before[name] {
			t.Errorf("glamour %s style changed after building hud: got %v, want %v", name, got, before[name])
		}
	}
}

// The hud style follows the llm-js answer CSS: headings render uppercase
// and the rest of the text keeps its case.
func TestHUDMarkdownRendersUppercaseHeadings(t *testing.T) {
	for _, dark := range []bool{true, false} {
		r, err := glamour.NewTermRenderer(glamour.WithStyles(hudMarkdown(dark)), glamour.WithWordWrap(80))
		if err != nil {
			t.Fatalf("dark=%v: NewTermRenderer: %v", dark, err)
		}
		out, err := r.Render("## Status report\n\nAll systems nominal.\n")
		if err != nil {
			t.Fatalf("dark=%v: Render: %v", dark, err)
		}
		plain := xansi.Strip(out)
		if !strings.Contains(plain, "STATUS REPORT") {
			t.Errorf("dark=%v: heading not uppercased: %q", dark, plain)
		}
		if !strings.Contains(plain, "All systems nominal.") {
			t.Errorf("dark=%v: body text changed case: %q", dark, plain)
		}
	}
}
