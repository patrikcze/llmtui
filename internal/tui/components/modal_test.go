package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

func TestModalInnerSize(t *testing.T) {
	for _, tc := range []struct {
		w, h         int
		wantW, wantH int
		wantOK       bool
	}{
		{w: 200, h: 40, wantW: 136, wantH: 36, wantOK: true},
		{w: 120, h: 30, wantW: 104, wantH: 26, wantOK: true},
		{w: 60, h: 15, wantW: 54, wantH: 11, wantOK: true},
		{w: 49, h: 30, wantOK: false},
		{w: 120, h: 7, wantOK: false},
	} {
		w, h, ok := ModalInnerSize(tc.w, tc.h)
		if ok != tc.wantOK || (ok && (w != tc.wantW || h != tc.wantH)) {
			t.Errorf("ModalInnerSize(%d, %d) = %d, %d, %t; want %d, %d, %t", tc.w, tc.h, w, h, ok, tc.wantW, tc.wantH, tc.wantOK)
		}
	}
}

func TestModalFrameHasExactSize(t *testing.T) {
	theme := styles.ByName("forest")
	box := Modal(theme, "Providers", "esc close", "row one\nrow two that is far too long to fit in the dialog body width", 20, 4)
	lines := strings.Split(box, "\n")
	if len(lines) != 6 {
		t.Fatalf("lines = %d, want body 4 + 2 border rows", len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != 24 {
			t.Errorf("line %d width = %d, want 24: %q", i, w, ansi.Strip(line))
		}
	}
	if !strings.Contains(ansi.Strip(lines[0]), "Providers") || !strings.Contains(ansi.Strip(lines[5]), "esc close") {
		t.Fatal("title or hint missing from the border")
	}
}

func TestOverlayCentersBoxAndKeepsMarkers(t *testing.T) {
	theme := styles.ByName("forest")
	base := strings.Repeat(strings.Repeat("x", 30)+"\n", 10)
	marker := "\x1b[1001z"
	box := "+--+\n|" + marker + "ab" + marker + "|\n+--+"
	out := Overlay(theme, base, 30, 10, box)
	lines := strings.Split(out, "\n")
	if len(lines) != 10 {
		t.Fatalf("lines = %d, want 10", len(lines))
	}
	for i, line := range lines {
		if w := ansi.StringWidth(line); w != 30 {
			t.Errorf("line %d width = %d, want 30", i, w)
		}
	}
	if !strings.Contains(lines[4], marker+"ab"+marker) {
		t.Fatalf("zone markers inside the box were not preserved: %q", lines[4])
	}
	if got := ansi.Strip(lines[4]); got != strings.Repeat("x", 13)+"|ab|"+strings.Repeat("x", 13) {
		t.Fatalf("box not centered: %q", got)
	}
}

func TestTableRowAlignsColumns(t *testing.T) {
	row := TableRow([]Column{{Width: 4}, {Width: 0}, {Width: 5, Right: true}}, []string{"ab", "name that is long", "42"}, 30)
	if got := ansi.Strip(row); ansi.StringWidth(got) != 30 || !strings.HasSuffix(got, "   42") || !strings.HasPrefix(got, "ab  ") {
		t.Fatalf("row = %q", got)
	}
}
