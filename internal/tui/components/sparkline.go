// Package components holds small composable pieces of the TUI.
package components

import "strings"

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders values as a Unicode sparkline of the given width. The
// most recent values are kept when there are more values than columns. When
// ascii is true, plain characters are used for terminals without Unicode.
func Sparkline(values []int, width int, ascii bool) string {
	if width <= 0 || len(values) == 0 {
		return strings.Repeat(" ", max(width, 0))
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}

	maxVal := 0
	for _, v := range values {
		if v > maxVal {
			maxVal = v
		}
	}

	runes := sparkRunes
	if ascii {
		runes = []rune("_.-=+*#%")
	}

	var b strings.Builder
	for _, v := range values {
		idx := 0
		if maxVal > 0 {
			idx = v * (len(runes) - 1) / maxVal
		}
		b.WriteRune(runes[idx])
	}
	// Pad to full width so the panel stays stable.
	b.WriteString(strings.Repeat(" ", width-len(values)))
	return b.String()
}
