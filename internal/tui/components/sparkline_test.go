package components

import (
	"strings"
	"testing"
)

func TestSparklineScalesToMax(t *testing.T) {
	got := Sparkline([]int{0, 50, 100}, 3, false)
	if got != "▁▄█" {
		t.Errorf("Sparkline = %q, want ▁▄█", got)
	}
}

func TestSparklineKeepsMostRecent(t *testing.T) {
	got := Sparkline([]int{1, 2, 3, 4, 100}, 2, false)
	// Only the last two values fit; 100 is the max.
	if []rune(got)[1] != '█' {
		t.Errorf("Sparkline = %q, last column should be full block", got)
	}
}

func TestSparklinePadsToWidth(t *testing.T) {
	got := Sparkline([]int{5}, 4, false)
	if len([]rune(got)) != 4 {
		t.Errorf("Sparkline width = %d, want 4", len([]rune(got)))
	}
}

func TestSparklineEmpty(t *testing.T) {
	if got := Sparkline(nil, 5, false); got != "     " {
		t.Errorf("empty Sparkline = %q, want spaces", got)
	}
	if got := Sparkline([]int{1}, 0, false); got != "" {
		t.Errorf("zero-width Sparkline = %q, want empty", got)
	}
}

func TestSparklineASCIIFallback(t *testing.T) {
	got := Sparkline([]int{0, 100}, 2, true)
	if strings.ContainsRune(got, '█') {
		t.Errorf("ASCII sparkline %q should not contain Unicode blocks", got)
	}
}
