package components

import (
	"strings"
	"time"
)

var (
	heatRunes = []rune("·░▒▓█")
	heatASCII = []rune(".-=+#")
)

// HeatmapData feeds Heatmap.
type HeatmapData struct {
	// Values maps YYYY-MM-DD (local time) to activity (e.g. tokens).
	Values map[string]int
	Weeks  int // number of week columns, ending at Today's week
	Today  time.Time
	ASCII  bool
}

// Heatmap renders a GitHub-style activity calendar: one column per week,
// Monday–Sunday rows, month labels on top and a Less→More legend below.
// Lines are returned unstyled.
func Heatmap(d HeatmapData) []string {
	if d.Weeks < 1 {
		d.Weeks = 1
	}
	runes := heatRunes
	if d.ASCII {
		runes = heatASCII
	}

	// Normalize to the Monday of the current week, then step back.
	today := d.Today
	weekday := (int(today.Weekday()) + 6) % 7 // Monday = 0
	thisMonday := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, today.Location()).
		AddDate(0, 0, -weekday)
	firstMonday := thisMonday.AddDate(0, 0, -7*(d.Weeks-1))

	maxVal := 0
	for _, v := range d.Values {
		if v > maxVal {
			maxVal = v
		}
	}

	const gutter = 4 // "Mon "
	// Month labels: printed at the week column where the month changes.
	months := []rune(strings.Repeat(" ", gutter+d.Weeks))
	lastMonth := time.Month(0)
	for w := 0; w < d.Weeks; w++ {
		day := firstMonday.AddDate(0, 0, 7*w)
		if day.Month() != lastMonth {
			lastMonth = day.Month()
			label := day.Format("Jan")
			for i, r := range label {
				if gutter+w+i < len(months) {
					months[gutter+w+i] = r
				}
			}
		}
	}

	lines := []string{strings.TrimRight(string(months), " ")}
	dayNames := []string{"Mon", "", "Wed", "", "Fri", "", ""}
	for row := 0; row < 7; row++ {
		var b strings.Builder
		b.WriteString(padRight(dayNames[row], gutter))
		for w := 0; w < d.Weeks; w++ {
			day := firstMonday.AddDate(0, 0, 7*w+row)
			if day.After(today) {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(runes[heatLevel(d.Values[day.Format("2006-01-02")], maxVal)])
		}
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}

	legend := padRight("", gutter) + "Less " + string(runes) + " More"
	lines = append(lines, "", legend)
	return lines
}

// heatLevel buckets a value into 0..4 relative to the max.
func heatLevel(value, maxVal int) int {
	if value == 0 || maxVal == 0 {
		return 0
	}
	level := 1 + value*4/(maxVal+1)
	if level > 4 {
		level = 4
	}
	return level
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}
