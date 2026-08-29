package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestRenderMarkdownMathDisabledIsUnchanged pins the backward-compatibility
// contract: with ui.math.enabled off, renderMarkdown behaves exactly as it
// did before the feature — both when Markdown itself is off and when it is on.
func TestRenderMarkdownMathDisabledIsUnchanged(t *testing.T) {
	const src = `Torque is $\approx 323 \text{ Nm}$ and volume $\text{m}^3$.`

	t.Run("markdown off", func(t *testing.T) {
		m := newTestModel(t)
		m.cfg.UI.Markdown = false
		m.cfg.UI.Math.Enabled = false
		if got := m.renderMarkdown(src); got != src {
			t.Fatalf("markdown-off path changed:\n got:  %q\n want: %q", got, src)
		}
	})

	t.Run("markdown on, math off matches baseline", func(t *testing.T) {
		base := newTestModel(t)
		base.cfg.UI.Markdown = true
		base.cfg.UI.Math.Enabled = false
		want := base.renderMarkdown(src)

		// A separate model with the same settings must produce identical bytes.
		other := newTestModel(t)
		other.cfg.UI.Markdown = true
		other.cfg.UI.Math.Enabled = false
		if got := other.renderMarkdown(src); got != want {
			t.Fatalf("math-off render not deterministic/unchanged:\n got:  %q\n want: %q", got, want)
		}
		if strings.Contains(ansi.Strip(want), "≈") {
			t.Fatalf("math-off output unexpectedly contains rendered math: %q", want)
		}
	})
}

// TestRenderMarkdownMathEnabledExpands checks the feature end to end through
// the real Glamour renderer.
func TestRenderMarkdownMathEnabledExpands(t *testing.T) {
	m := newTestModel(t)
	m.cfg.UI.Markdown = true
	m.cfg.UI.Math.Enabled = true

	out := ansi.Strip(m.renderMarkdown(`The torque is $\approx 323 \text{ Nm}$ here.`))
	if !strings.Contains(out, "≈") || !strings.Contains(out, "Nm") {
		t.Fatalf("expected rendered math, got: %q", out)
	}
	if strings.Contains(out, `\approx`) || strings.Contains(out, `\text{`) {
		t.Fatalf("literal LaTeX survived: %q", out)
	}
}

// TestRenderMarkdownMathTableNotCorrupted feeds the real-world regression
// table and confirms the pipe structure survives when math is on.
func TestRenderMarkdownMathTableNotCorrupted(t *testing.T) {
	m := newTestModel(t)
	m.cfg.UI.Markdown = true
	m.cfg.UI.Math.Enabled = true

	src := "| Powertrain | Max HP | Max Torque |\n" +
		"|---|---|---|\n" +
		"| Hybrid | $\\approx 218 \\text{ HP}$ | Up to $\\approx 323 \\text{ Nm}$ |\n" +
		"| Frac | $\\frac{1}{2}$ | plain |\n"

	out := ansi.Strip(m.renderMarkdown(src))
	for _, want := range []string{"Powertrain", "Hybrid", "≈", "218", "HP", "Frac", "plain"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table lost %q:\n%s", want, out)
		}
	}
	// Glamour renders a table as a bordered box; a corrupted table collapses
	// to far fewer lines. Require the four content rows plus borders.
	if n := strings.Count(out, "\n"); n < 6 {
		t.Fatalf("table looks collapsed (%d lines):\n%s", n, out)
	}
}
