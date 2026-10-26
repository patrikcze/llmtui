package styles

import (
	"charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
)

// hudPalette is the subset of colors the hud Markdown style needs, in the
// hex form glamour expects.
type hudPalette struct {
	text, accent, dim, faint, warn, err, good, info string
	onAccent, codeBg, blockBg                       string
}

var (
	hudDarkPalette = hudPalette{
		text: "#CFE7E0", accent: "#8FE3CF", dim: "#6E928B", faint: "#3C5752",
		warn: "#E9C46A", err: "#FF7A6B", good: "#A6E3A1", info: "#7FC8E8",
		onAccent: "#0B1212", codeBg: "#142221", blockBg: "#101B1A",
	}
	hudLightPalette = hudPalette{
		text: "#16302B", accent: "#1F7A68", dim: "#5E7A74", faint: "#9DB5AF",
		warn: "#8A6A12", err: "#B8432F", good: "#2E7D4F", info: "#1F6E94",
		onAccent: "#FFFFFF", codeBg: "#E3EFEB", blockBg: "#EEF5F3",
	}
)

// hudMarkdown builds the glamour style for the hud theme. It starts from
// glamour's standard style for the same background so spacing, list
// indents and prefixes stay what every other theme renders, and recolors
// it after the llm-js answer CSS: uppercase accent headings, accent links,
// dim block quotes and tinted code.
//
// The standard configs are shared package variables holding pointers, so
// every override assigns a fresh pointer and the Chroma struct is copied
// before it is changed; nothing here may write through a pointer it got
// from the base.
func hudMarkdown(dark bool) ansi.StyleConfig {
	p, cfg := hudLightPalette, glamourstyles.LightStyleConfig
	if dark {
		p, cfg = hudDarkPalette, glamourstyles.DarkStyleConfig
	}

	cfg.Document.Color = &p.text
	cfg.BlockQuote.Color = &p.dim
	cfg.BlockQuote.IndentToken = ptr("│ ")

	cfg.Heading.Color = &p.accent
	cfg.Heading.Bold = ptr(true)
	cfg.Heading.Upper = ptr(true)
	cfg.H1.Color = &p.onAccent
	cfg.H1.BackgroundColor = &p.accent
	cfg.H6.Color = &p.dim

	cfg.HorizontalRule.Color = &p.faint
	cfg.Item.BlockPrefix = "› "
	cfg.Enumeration.Color = &p.dim

	cfg.Link.Color = &p.dim
	cfg.LinkText.Color = &p.accent
	cfg.Image.Color = &p.accent
	cfg.ImageText.Color = &p.dim

	cfg.Code.Color = &p.accent
	cfg.Code.BackgroundColor = &p.codeBg
	cfg.CodeBlock.Color = &p.text
	cfg.Table.Color = &p.text

	chroma := *cfg.CodeBlock.Chroma
	chroma.Text = ansi.StylePrimitive{Color: &p.text}
	chroma.Error = ansi.StylePrimitive{Color: &p.onAccent, BackgroundColor: &p.err}
	chroma.Comment = ansi.StylePrimitive{Color: &p.dim, Italic: ptr(true)}
	chroma.CommentPreproc = ansi.StylePrimitive{Color: &p.warn}
	chroma.Keyword = ansi.StylePrimitive{Color: &p.accent}
	chroma.KeywordReserved = ansi.StylePrimitive{Color: &p.accent, Bold: ptr(true)}
	chroma.KeywordNamespace = ansi.StylePrimitive{Color: &p.accent}
	chroma.KeywordType = ansi.StylePrimitive{Color: &p.info}
	chroma.Operator = ansi.StylePrimitive{Color: &p.dim}
	chroma.Punctuation = ansi.StylePrimitive{Color: &p.dim}
	chroma.Name = ansi.StylePrimitive{Color: &p.text}
	chroma.NameBuiltin = ansi.StylePrimitive{Color: &p.info}
	chroma.NameTag = ansi.StylePrimitive{Color: &p.accent}
	chroma.NameAttribute = ansi.StylePrimitive{Color: &p.info}
	chroma.NameClass = ansi.StylePrimitive{Color: &p.text, Bold: ptr(true)}
	chroma.NameDecorator = ansi.StylePrimitive{Color: &p.warn}
	chroma.NameFunction = ansi.StylePrimitive{Color: &p.good}
	chroma.LiteralNumber = ansi.StylePrimitive{Color: &p.good}
	chroma.LiteralString = ansi.StylePrimitive{Color: &p.warn}
	chroma.LiteralStringEscape = ansi.StylePrimitive{Color: &p.accent}
	chroma.GenericDeleted = ansi.StylePrimitive{Color: &p.err}
	chroma.GenericInserted = ansi.StylePrimitive{Color: &p.good}
	chroma.GenericSubheading = ansi.StylePrimitive{Color: &p.dim}
	chroma.Background = ansi.StylePrimitive{BackgroundColor: &p.blockBg}
	cfg.CodeBlock.Chroma = &chroma

	return cfg
}

func ptr[T any](v T) *T { return &v }
