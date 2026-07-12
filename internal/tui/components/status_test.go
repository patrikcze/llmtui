package components

import (
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

func TestSpinnerFrame(t *testing.T) {
	if got := SpinnerFrame(0, false, true); got != "⠋" {
		t.Errorf("frame 0 = %q, want ⠋", got)
	}
	if SpinnerFrame(0, false, true) == SpinnerFrame(1, false, true) {
		t.Error("consecutive frames should differ while animated")
	}
	if got := SpinnerFrame(3, true, true); got != "/" {
		t.Errorf("ascii frame 3 = %q, want /", got)
	}
	// Static glyphs when animation is off, regardless of the frame value.
	if got := SpinnerFrame(7, false, false); got != "•" {
		t.Errorf("static = %q, want •", got)
	}
	if got := SpinnerFrame(7, true, false); got != "*" {
		t.Errorf("static ascii = %q, want *", got)
	}
}

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{0, "0s"},
		{42 * time.Second, "42s"},
		{time.Minute, "1m 00s"},
		{191 * time.Second, "3m 11s"},
		{time.Hour + 2*time.Minute + 30*time.Second, "1h 02m"},
	}
	for _, c := range cases {
		if got := FormatElapsed(c.d); got != c.want {
			t.Errorf("FormatElapsed(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestWorkingLineComposition(t *testing.T) {
	th := styles.ByName("mono")
	out := WorkingLine(th, 2, "Ideating", "3m 11s", "↓ 9.1k tokens", false, true)
	for _, want := range []string{"Ideating…", "3m 11s", "↓ 9.1k tokens", "esc to interrupt"} {
		if !strings.Contains(out, want) {
			t.Errorf("animated line missing %q in %q", want, out)
		}
	}

	static := WorkingLine(th, 2, "Ideating", "42s", "", false, false)
	if strings.Contains(static, "…") {
		t.Errorf("static line should drop the ellipsis: %q", static)
	}
	if strings.Contains(static, "tokens") {
		t.Errorf("empty tokens must not render a tokens segment: %q", static)
	}
	if !strings.Contains(static, "•") {
		t.Errorf("static line should use the bullet glyph: %q", static)
	}
}
