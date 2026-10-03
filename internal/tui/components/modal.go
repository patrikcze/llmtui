package components

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// ModalInnerSize returns the body width and height of a modal dialog drawn
// over an area of areaW×areaH cells, and whether the area is large enough for
// a dialog at all. The dialog takes ~90% of the width (capped at 140 columns)
// and all but one row above and below; the frame costs two rows and four
// columns (border plus one cell of padding on each side).
func ModalInnerSize(areaW, areaH int) (innerW, innerH int, ok bool) {
	if areaW < 50 || areaH < 8 {
		return 0, 0, false
	}
	outerW := min(areaW*9/10, 140)
	outerW = max(outerW, min(areaW-2, 60))
	outerH := areaH - 2
	return outerW - 4, outerH - 2, true
}

// Modal renders body inside a rounded frame of exactly innerW×innerH body
// cells. The title is set into the top border and hint into the bottom
// border, so neither costs a body row. Body lines are padded or truncated to
// innerW; zone markers and styles inside them are kept intact.
func Modal(theme styles.Theme, title, hint, body string, innerW, innerH int) string {
	edge := lipgloss.NewStyle().Foreground(theme.Accent)
	titleStyle := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(theme.Faint)

	rule := func(left, label, right string, labelStyle lipgloss.Style) string {
		span := innerW + 2 // the border corners sit outside the padded body
		label = ansi.Truncate(label, max(span-4, 0), "…")
		if label == "" {
			return edge.Render(left + strings.Repeat("─", span) + right)
		}
		fill := max(span-ansi.StringWidth(label)-3, 0)
		return edge.Render(left+"─ ") + labelStyle.Render(label) + edge.Render(" "+strings.Repeat("─", fill)+right)
	}

	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var b strings.Builder
	b.WriteString(rule("╭", title, "╮", titleStyle))
	for row := range innerH {
		line := ""
		if row < len(lines) {
			line = lines[row]
		}
		b.WriteString("\n" + edge.Render("│") + " " + FitWidth(line, innerW) + " " + edge.Render("│"))
	}
	b.WriteString("\n" + rule("╰", hint, "╯", hintStyle))
	return b.String()
}

// Overlay splices box centered over base, an areaW×areaH frame. The base is
// shown dimmed: its text is kept but re-rendered in the theme's faint colour,
// so the dialog reads as being in front of it. Box lines are inserted
// verbatim, which preserves their styles and click-zone markers.
func Overlay(theme styles.Theme, base string, areaW, areaH int, box string) string {
	dim := lipgloss.NewStyle().Foreground(theme.Faint)
	baseLines := strings.Split(ansi.Strip(base), "\n")
	boxLines := strings.Split(box, "\n")
	boxW := 0
	for _, line := range boxLines {
		boxW = max(boxW, ansi.StringWidth(line))
	}
	x := max((areaW-boxW)/2, 0)
	y := max((areaH-len(boxLines))/2, 0)

	out := make([]string, areaH)
	for row := range areaH {
		plain := ""
		if row < len(baseLines) {
			plain = baseLines[row]
		}
		plain = FitWidth(plain, areaW)
		boxRow := row - y
		if boxRow < 0 || boxRow >= len(boxLines) {
			out[row] = dim.Render(plain)
			continue
		}
		left := ansi.Truncate(plain, x, "")
		right := ansi.TruncateLeft(plain, x+boxW, "")
		out[row] = dim.Render(left) + boxLines[boxRow] + dim.Render(right)
	}
	return strings.Join(out, "\n")
}

// FitWidth pads s with spaces, or truncates it with an ellipsis, to exactly w
// display cells. Escape sequences in s are preserved.
func FitWidth(s string, w int) string {
	if w <= 0 {
		return ""
	}
	width := ansi.StringWidth(s)
	if width > w {
		return ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-width)
}

// Column describes one column of a Table row.
type Column struct {
	// Width is the column width in cells; 0 lets the column take the space
	// left over once every fixed column is placed.
	Width int
	// Right aligns the cell to the right edge of its column.
	Right bool
}

// TableRow lays cells out in columns within total cells, separated by two
// spaces. Cells are padded or truncated with an ellipsis to their column
// width; escape sequences inside cells are preserved.
func TableRow(columns []Column, cells []string, total int) string {
	widths := make([]int, len(columns))
	fixed, flexible := 0, 0
	for i, col := range columns {
		widths[i] = col.Width
		fixed += col.Width
		if col.Width == 0 {
			flexible++
		}
	}
	gaps := 2 * max(len(columns)-1, 0)
	if flexible > 0 {
		share := max((total-fixed-gaps)/flexible, 1)
		for i := range widths {
			if widths[i] == 0 {
				widths[i] = share
			}
		}
	}
	parts := make([]string, len(columns))
	for i, col := range columns {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		if col.Right {
			if pad := widths[i] - ansi.StringWidth(cell); pad > 0 {
				cell = strings.Repeat(" ", pad) + cell
			}
		}
		parts[i] = FitWidth(cell, widths[i])
	}
	return strings.Join(parts, "  ")
}

// SectionTitle renders a dialog section heading with a thin rule filling the
// remaining width.
func SectionTitle(theme styles.Theme, title string, width int) string {
	label := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(title)
	fill := max(width-ansi.StringWidth(title)-1, 0)
	return label + " " + lipgloss.NewStyle().Foreground(theme.PanelEdge).Render(strings.Repeat("─", fill))
}
