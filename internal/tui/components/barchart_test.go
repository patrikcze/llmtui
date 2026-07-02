package components

import (
	"strings"
	"testing"
	"time"
)

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{950, "950"},
		{1200, "1.2k"},
		{8500, "8.5k"},
		{85000, "85k"},
		{1_200_000, "1.2M"},
		{8_500_000, "8.5M"},
	}
	for _, tt := range tests {
		if got := FormatTokens(tt.in); got != tt.want {
			t.Errorf("FormatTokens(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestBarChartShape(t *testing.T) {
	lines := BarChart(BarChartData{
		Values:  []int{0, 50, 100},
		XLabels: map[int]string{0: "day1"},
		Height:  4,
	})
	// 4 chart rows + axis + x labels.
	if len(lines) != 6 {
		t.Fatalf("lines = %d, want 6", len(lines))
	}
	// Top row shows the max value label and only the tallest bar.
	if !strings.Contains(lines[0], "100") {
		t.Errorf("top row %q should carry the max label", lines[0])
	}
	if !strings.Contains(lines[0], "█") {
		t.Errorf("top row %q should contain the full bar", lines[0])
	}
	if !strings.HasSuffix(lines[4], strings.Repeat("─", 3)) {
		t.Errorf("axis row %q should span all columns", lines[4])
	}
	if !strings.Contains(lines[5], "day1") {
		t.Errorf("x label row %q should contain day1", lines[5])
	}
}

func TestBarChartAllZero(t *testing.T) {
	lines := BarChart(BarChartData{Values: []int{0, 0}, Height: 3})
	for i := 0; i < 3; i++ {
		if strings.ContainsAny(lines[i], "▁▂▃▄▅▆▇█") {
			t.Errorf("row %q should be empty for zero data", lines[i])
		}
	}
}

func TestBarChartNonZeroNeverEmpty(t *testing.T) {
	// A tiny value next to a huge one must still render something.
	lines := BarChart(BarChartData{Values: []int{1, 10000}, Height: 4})
	bottom := lines[3]
	if !strings.ContainsAny(bottom, "▁▂▃▄▅▆▇█") {
		t.Errorf("bottom row %q should show the tiny value", bottom)
	}
}

func TestBarChartASCII(t *testing.T) {
	lines := BarChart(BarChartData{Values: []int{100}, Height: 2, ASCII: true})
	joined := strings.Join(lines, "\n")
	if strings.ContainsAny(joined, "▁▂▃▄▅▆▇█─┤┼") {
		t.Errorf("ASCII chart contains Unicode: %q", joined)
	}
}

func TestHeatmapShape(t *testing.T) {
	today := time.Date(2026, 7, 2, 12, 0, 0, 0, time.Local) // Thursday
	values := map[string]int{
		"2026-07-01": 100,
		"2026-06-15": 50,
	}
	lines := Heatmap(HeatmapData{Values: values, Weeks: 8, Today: today})
	// months + 7 day rows + blank + legend
	if len(lines) != 10 {
		t.Fatalf("lines = %d, want 10", len(lines))
	}
	// Weeks run May 11 … Jun 29, so the columns start in May and June.
	if !strings.Contains(lines[0], "May") || !strings.Contains(lines[0], "Jun") {
		t.Errorf("month row %q should label May and Jun", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Mon") || !strings.HasPrefix(lines[3], "Wed") {
		t.Error("weekday rows should be labeled Mon/Wed/Fri")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "█") {
		t.Error("max-activity day should render at full intensity")
	}
	if !strings.Contains(lines[9], "Less") || !strings.Contains(lines[9], "More") {
		t.Errorf("legend row %q missing Less/More", lines[9])
	}
}

func TestHeatmapFutureDaysBlank(t *testing.T) {
	today := time.Date(2026, 7, 2, 12, 0, 0, 0, time.Local) // Thursday
	lines := Heatmap(HeatmapData{Values: map[string]int{}, Weeks: 2, Today: today})
	// Friday of the current (last) week is Jul 3 — in the future, so the row
	// keeps only last week's Friday cell: "Fri " + "·" with the future cell
	// blank and trimmed.
	if lines[5] != "Fri ·" {
		t.Errorf("Friday row = %q, want %q (future day blank)", lines[5], "Fri ·")
	}
	// Thursday (today) renders both weeks.
	if lines[4] != "    ··" {
		t.Errorf("Thursday row = %q, want two cells", lines[4])
	}
}
