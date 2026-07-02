package components

import (
	"strconv"
	"strings"
)

// FormatTokens renders token counts compactly: 950, 8.5k, 1.2M.
func FormatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return trimZero(float64(n)/1e6) + "M"
	case n >= 1_000:
		return trimZero(float64(n)/1e3) + "k"
	default:
		return strconv.Itoa(n)
	}
}

func trimZero(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}

// BarChartData feeds BarChart.
type BarChartData struct {
	Values  []int
	XLabels map[int]string // column index → label, rendered under the axis
	Height  int            // chart body rows
	ASCII   bool
}

var barEighths = []rune("▁▂▃▄▅▆▇█")

// BarChart renders a column chart with a labeled y-axis:
//
//	8.5k ┤          █
//	4.2k ┤  ▄   ▂   █
//	   0 ┼──────────────
//	     Jun 05   Jun 20
//
// Lines are returned unstyled so the caller can color the block as a whole.
func BarChart(d BarChartData) []string {
	if d.Height < 1 {
		d.Height = 1
	}
	maxVal := 0
	for _, v := range d.Values {
		if v > maxVal {
			maxVal = v
		}
	}

	// Y-axis labels, right-aligned.
	labels := make([]string, d.Height+1)
	labelWidth := 0
	for row := 0; row <= d.Height; row++ {
		labels[row] = FormatTokens(maxVal * (d.Height - row) / d.Height)
		if len(labels[row]) > labelWidth {
			labelWidth = len(labels[row])
		}
	}

	tick := " ┤"
	if d.ASCII {
		tick = " |"
	}

	lines := make([]string, 0, d.Height+2)
	for row := 0; row < d.Height; row++ {
		var b strings.Builder
		b.WriteString(pad(labels[row], labelWidth))
		b.WriteString(tick)
		rowFromBottom := d.Height - 1 - row
		for _, v := range d.Values {
			b.WriteRune(barCell(v, maxVal, d.Height, rowFromBottom, d.ASCII))
		}
		lines = append(lines, b.String())
	}

	// Axis row.
	axis := pad(labels[d.Height], labelWidth) + " ┼" + strings.Repeat("─", len(d.Values))
	if d.ASCII {
		axis = pad(labels[d.Height], labelWidth) + " +" + strings.Repeat("-", len(d.Values))
	}
	lines = append(lines, axis)

	// X labels row; long labels may extend past the last column.
	if len(d.XLabels) > 0 {
		width := labelWidth + 2 + len(d.Values)
		for col, label := range d.XLabels {
			if end := labelWidth + 2 + col + len(label); end > width {
				width = end
			}
		}
		row := []rune(strings.Repeat(" ", width))
		for col, label := range d.XLabels {
			start := labelWidth + 2 + col
			for i, r := range label {
				row[start+i] = r
			}
		}
		lines = append(lines, strings.TrimRight(string(row), " "))
	}
	return lines
}

// barCell returns the rune for one column at rowFromBottom (0 = bottom row).
func barCell(value, maxVal, height, rowFromBottom int, ascii bool) rune {
	if maxVal == 0 || value == 0 {
		return ' '
	}
	eighths := value * height * 8 / maxVal
	if eighths == 0 {
		eighths = 1 // never render a non-zero value as empty
	}
	filled := eighths - rowFromBottom*8
	switch {
	case filled >= 8:
		if ascii {
			return '#'
		}
		return '█'
	case filled <= 0:
		return ' '
	default:
		if ascii {
			if filled >= 4 {
				return '+'
			}
			return '.'
		}
		return barEighths[filled-1]
	}
}

func pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}
