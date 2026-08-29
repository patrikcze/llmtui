// Package terminalmath expands LaTeX-style mathematics embedded in Markdown
// into terminal-friendly Unicode.
//
// It is a display-only transformation that sits between
// [github.com/patrikcze/llmtui/internal/terminaltext.Sanitize] and the Glamour
// Markdown renderer in the TUI. It never touches provider messages, agent
// messages, history, cache keys, prompt construction, or any conversation
// state — only the string about to be handed to the renderer.
//
// The heavy lifting is done by github.com/doug/termtex, which parses a subset
// of LaTeX math and typesets it on a character grid using Unicode box-drawing
// characters, mathematical symbols, and combining marks. termtex is invoked
// with the zero-value [termtex.Style]: plain Unicode, no ANSI colour, no
// italic letter forms — so the expansion cannot introduce terminal escape
// sequences.
package terminalmath

import (
	"strings"

	"github.com/doug/termtex"
)

// style is the single termtex configuration used everywhere in this package.
// The zero value is deliberate: no Color (no ANSI escapes), no Italic (no
// dependency on exotic font coverage), no ASCII (full Unicode math).
var style = termtex.Style{}

// ExpandMarkdown rewrites inline ($...$) and display ($$...$$) math in a
// Markdown string to pre-rendered Unicode, leaving everything else — prose,
// currency, escaped dollars, inline code spans, fenced code blocks — exactly
// as it was.
//
// It is a pure function: no persistent state, safe to call once per streamed
// update. On malformed or unsupported input termtex preserves the original
// source; ExpandMarkdown never panics and never removes text.
//
// Markdown table rows are treated conservatively. An expression that termtex
// renders across multiple terminal rows (a fraction, a root, display math)
// would shatter the table layout, so inside a table row such an expression is
// left as its original LaTeX. Simple inline math (\approx, \text{...},
// super/subscripts, Greek letters) still renders inside table cells.
func ExpandMarkdown(input string) string {
	// Fast path: no math delimiter anywhere means nothing to do. This also
	// covers the empty-string case.
	if !strings.Contains(input, "$") {
		return input
	}

	lines := strings.Split(input, "\n")
	table := markTableRows(lines)

	var segments []string
	for i, n := 0, len(lines); i < n; {
		if table[i] {
			j := i
			var rows []string
			for j < n && table[j] {
				rows = append(rows, expandTableRow(lines[j]))
				j++
			}
			segments = append(segments, strings.Join(rows, "\n"))
			i = j
			continue
		}
		j := i
		for j < n && !table[j] {
			j++
		}
		// A non-table chunk always contains balanced code fences (a GFM table
		// cannot open inside a fence, and markTableRows never marks lines
		// inside one), so termtex.Expand's own fence tracking is reliable here.
		chunk := strings.Join(lines[i:j], "\n")
		segments = append(segments, termtex.Expand(chunk, style))
		i = j
	}

	// Segments are cut at original line boundaries, so exactly one newline
	// separated each pair; Join restores them.
	return strings.Join(segments, "\n")
}

// expandTableRow expands math in a single Markdown table row, but only when
// the result stays on one line. A multi-row expansion (or one that termtex
// promotes to a fenced block) would corrupt the table, so the original line —
// LaTeX and all — is kept instead.
func expandTableRow(line string) string {
	if !strings.Contains(line, "$") {
		return line
	}
	e := termtex.Expand(line, style)
	if strings.ContainsRune(e, '\n') || strings.Contains(e, "```") {
		return line
	}
	return e
}

// markTableRows returns a per-line flag marking which lines belong to a GFM
// pipe table (header row, delimiter row, and the body rows that follow).
// Lines inside fenced code blocks are never marked.
func markTableRows(lines []string) []bool {
	marks := make([]bool, len(lines))

	inFence := false
	var fenceChar byte
	fenceLen := 0

	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimLeft(lines[i], " ")

		if fc, fl := fenceRun(trimmed); fl > 0 {
			rest := strings.TrimSpace(trimmed[fl:])
			switch {
			case !inFence && fl >= 3:
				inFence, fenceChar, fenceLen = true, fc, fl
			case inFence && fc == fenceChar && fl >= fenceLen && rest == "":
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}

		// A table is a delimiter row ("|---|:-:|") preceded by a non-blank
		// header line that also contains a pipe.
		if i == 0 || !isDelimiterRow(lines[i]) {
			continue
		}
		header := lines[i-1]
		if marks[i-1] || strings.TrimSpace(header) == "" || !strings.Contains(header, "|") {
			continue
		}

		marks[i-1] = true
		marks[i] = true
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" || !strings.Contains(lines[j], "|") {
				break
			}
			if _, fl := fenceRun(strings.TrimLeft(lines[j], " ")); fl >= 3 {
				break
			}
			marks[j] = true
		}
	}

	return marks
}

// fenceRun reports the leading run of backtick or tilde characters at the
// start of s (already left-trimmed of spaces). Returns the fence character
// and the run length, or (0, 0) if s does not start with one.
func fenceRun(s string) (byte, int) {
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return 0, 0
	}
	c := s[0]
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return c, n
}

// isDelimiterRow reports whether line is a GFM table delimiter row: pipe-
// separated cells each matching /^:?-+:?$/, with at least one dash.
func isDelimiterRow(line string) bool {
	s := strings.TrimSpace(line)
	if s == "" || !strings.Contains(s, "-") {
		return false
	}
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	if s == "" {
		return false
	}
	for _, cell := range strings.Split(s, "|") {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		for k := 0; k < len(cell); k++ {
			switch cell[k] {
			case '-':
			case ':':
				if k != 0 && k != len(cell)-1 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}
