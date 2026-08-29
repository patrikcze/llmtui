package terminalmath

import (
	"strings"
	"testing"
)

// containsControl reports whether s holds any C0/C1 control rune other than
// newline or tab — the exact class terminaltext.Sanitize exists to remove.
func containsControl(s string) bool {
	for _, r := range s {
		if r == '\n' || r == '\t' {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

func TestExpandMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		// want, when non-empty, must equal the output exactly.
		want string
		// contains / notContains are substring assertions applied when want
		// is empty.
		contains    []string
		notContains []string
	}{
		{
			name:        "inline approx and text",
			in:          `The value is $\approx 323 \text{ Nm}$.`,
			contains:    []string{"≈", "323", "Nm", "The value is", "."},
			notContains: []string{`\approx`, `\text{`},
		},
		{
			name:        "superscript",
			in:          `Volume is $\text{m}^3$.`,
			contains:    []string{"m³", "Volume is"},
			notContains: []string{"^3", `\text`},
		},
		{
			name:        "subscript",
			in:          `Coefficient $x_1$ and $x_2$.`,
			contains:    []string{"x₁", "x₂"},
			notContains: []string{"x_1", "x_2"},
		},
		{
			name:        "greek letters",
			in:          `$\alpha + \beta = \gamma$`,
			contains:    []string{"α", "β", "γ"},
			notContains: []string{`\alpha`, `\beta`, `\gamma`},
		},
		{
			name:        "E = mc^2",
			in:          `Einstein: $E = mc^2$`,
			contains:    []string{"E = mc²"},
			notContains: []string{"mc^2"},
		},
		{
			name:        "display math fraction",
			in:          "Before\n\n$$\n\\frac{-b \\pm \\sqrt{b^2 - 4ac}}{2a}\n$$\n\nAfter",
			contains:    []string{"Before", "After", "```", "─", "±", "√"},
			notContains: []string{`\frac`, `\sqrt`, `\pm`},
		},
		{
			name: "malformed fraction preserved",
			in:   `Broken $\frac{ here`,
			want: `Broken $\frac{ here`,
		},
		{
			name: "unmatched dollar preserved",
			in:   `a $ b and more text`,
			want: `a $ b and more text`,
		},
		{
			name:        "escaped dollar not math",
			in:          `The kit is \$100 and \$200 total.`,
			notContains: []string{"─"},
			contains:    []string{`\$100`, `\$200`},
		},
		{
			name: "single currency value",
			in:   `It costs $100.`,
			want: `It costs $100.`,
		},
		{
			name: "multiple currency values in prose",
			in:   `Budget is between $50 and $100, ideally near $75.`,
			want: `Budget is between $50 and $100, ideally near $75.`,
		},
		{
			name: "inline code containing dollar",
			in:   "Set `$HOME` then run `echo '$PATH'` and `$\\frac{1}{2}$`.",
			want: "Set `$HOME` then run `echo '$PATH'` and `$\\frac{1}{2}$`.",
		},
		{
			name: "fenced code containing latex",
			in:   "```python\nprice = \"$100\"\nexpr = r\"$\\frac{x}{y}$\"\n```",
			want: "```python\nprice = \"$100\"\nexpr = r\"$\\frac{x}{y}$\"\n```",
		},
		{
			name: "tilde fenced code containing latex",
			in:   "~~~\nx = $a^2$ + $b^2$\n~~~",
			want: "~~~\nx = $a^2$ + $b^2$\n~~~",
		},
		{
			name:        "tilde fence does not swallow later math",
			in:          "~~~\n$a^2$\n~~~\n\nThen $a^2$ renders.",
			contains:    []string{"~~~\n$a^2$\n~~~", "a²"},
			notContains: []string{"Then $a^2$ renders"},
		},
		{
			name: "ordinary markdown unaffected",
			in:   "# Heading\n\n- one\n- two\n\n> quote\n\n[link](https://example.com) **bold** `code`",
			want: "# Heading\n\n- one\n- two\n\n> quote\n\n[link](https://example.com) **bold** `code`",
		},
		{
			name:        "table with simple inline math",
			in:          "| Feature | Value |\n|---|---|\n| Length | $\\approx 4.51 \\text{ m}$ |\n| Power | $\\approx 218 \\text{ HP}$ |\n| Volume | $\\text{m}^3$ |",
			contains:    []string{"| Feature | Value |", "|---|---|", "≈", "4.51", "m³"},
			notContains: []string{"```", `\approx`, `\text{`},
		},
		{
			name: "table complex expression not corrupted",
			in:   "| A | B |\n|---|---|\n| ok | $\\approx 5$ |\n| frac | $\\frac{1}{2}$ |\n| after | plain |",
			contains: []string{
				"| A | B |",
				"|---|---|",
				"| frac | $\\frac{1}{2}$ |", // kept verbatim
				"| after | plain |",
				"≈",
			},
			notContains: []string{"```"},
		},
		{
			name:        "multiple math expressions in one response",
			in:          `First $\alpha$, then $\beta^2$, finally $\gamma_i$.`,
			contains:    []string{"α", "β²", "γᵢ"},
			notContains: []string{`\alpha`, `\beta`, `\gamma`},
		},
		{
			name:        "repeated identical expression",
			in:          `Here is $\alpha$ and again $\alpha$ and once more $\alpha$.`,
			contains:    []string{"α"},
			notContains: []string{`\alpha`},
		},
		{
			name: "incomplete streamed expression does not panic",
			in:   "The formula $\\frac{-b \\pm \\sqrt{",
			want: "The formula $\\frac{-b \\pm \\sqrt{",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "plain text with no math",
			in:   "Just a sentence with no mathematics at all.",
			want: "Just a sentence with no mathematics at all.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandMarkdown(tc.in)

			if tc.want != "" || (tc.want == "" && len(tc.contains) == 0 && len(tc.notContains) == 0) {
				if got != tc.want {
					t.Fatalf("ExpandMarkdown mismatch\n in:   %q\n got:  %q\n want: %q", tc.in, got, tc.want)
				}
			}
			for _, sub := range tc.contains {
				if !strings.Contains(got, sub) {
					t.Errorf("output missing %q\n in:  %q\n got: %q", sub, tc.in, got)
				}
			}
			for _, sub := range tc.notContains {
				if strings.Contains(got, sub) {
					t.Errorf("output should not contain %q\n in:  %q\n got: %q", sub, tc.in, got)
				}
			}
			if containsControl(got) {
				t.Errorf("output introduced a terminal control character\n in:  %q\n got: %q", tc.in, got)
			}
		})
	}
}

// TestExpandMarkdownDisabledPathIsIdentity is a guard for the caller contract:
// when the feature is off the caller must not invoke ExpandMarkdown at all, so
// here we only assert the cheap fast-path stays byte-identical.
func TestExpandMarkdownFastPathIdentity(t *testing.T) {
	for _, s := range []string{
		"",
		"no dollar here",
		"# Title\n\nParagraph with `code` and **bold**.\n",
		"| a | b |\n|---|---|\n| 1 | 2 |\n",
	} {
		if got := ExpandMarkdown(s); got != s {
			t.Errorf("fast path changed input\n in:  %q\n got: %q", s, got)
		}
	}
}

// TestExpandMarkdownSanitizedControlInputStaysSafe feeds text that already
// went through terminaltext.Sanitize (so no ESC/OSC) and confirms the math
// expansion does not resurrect any control sequence.
func TestExpandMarkdownSanitizedControlInputStaysSafe(t *testing.T) {
	// A payload that, pre-sanitization, tried to smuggle an OSC title set and
	// a CSI clear; post-sanitization only the inert text remains, plus math.
	in := "safe title 0;pwned after 2J and math $\\alpha^2 + \\beta_j$ done"
	got := ExpandMarkdown(in)
	if containsControl(got) {
		t.Fatalf("control sequence present in output: %q", got)
	}
	if !strings.Contains(got, "α") || !strings.Contains(got, "done") {
		t.Fatalf("expected math expansion and trailing text: %q", got)
	}
}

// TestExpandMarkdownRealWorldRegression covers the two transcripts from the
// feature request: the terminal must no longer show literal \approx / \text{
// / }^3 for supported expressions, and the tables must survive.
func TestExpandMarkdownRealWorldRegression(t *testing.T) {
	specTable := "| Feature | 2026 Model Estimate | Units/Notes |\n" +
		"|---|---|---|\n" +
		"| Length | $\\approx 4,51$ meters | Metric |\n" +
		"| Width | $\\approx 1,84$ meters | Metric |\n" +
		"| Cargo Space | $\\approx 1,047 \\text{ Liters}$ | Cubic Meters / Liter |\n" +
		"| Max Cargo Space | $\\approx 1,952 \\text{ Liters}$ | $\\text{m}^3$ / Liter |"

	got := ExpandMarkdown(specTable)
	for _, bad := range []string{`$\approx`, `\text{`, `}^3`} {
		if strings.Contains(got, bad) {
			t.Errorf("literal LaTeX %q still present:\n%s", bad, got)
		}
	}
	for _, good := range []string{"≈", "4,51", "Liters", "m³", "| Feature |", "|---|---|---|"} {
		if !strings.Contains(got, good) {
			t.Errorf("expected %q in rendered table:\n%s", good, got)
		}
	}
	if strings.Contains(got, "```") {
		t.Errorf("table row promoted to fenced block:\n%s", got)
	}
	if lineCount(got) != lineCount(specTable) {
		t.Errorf("table row count changed: got %d want %d\n%s", lineCount(got), lineCount(specTable), got)
	}

	powertrain := "| Powertrain | Max HP | Max Torque |\n" +
		"|---|---|---|\n" +
		"| Hybrid | $\\approx 218 \\text{ HP}$ | Up to $\\approx 323 \\text{ Nm}$ |"
	got = ExpandMarkdown(powertrain)
	for _, bad := range []string{`$\approx`, `\text{`} {
		if strings.Contains(got, bad) {
			t.Errorf("literal LaTeX %q still present:\n%s", bad, got)
		}
	}
	for _, good := range []string{"≈", "218", "HP", "323", "Nm"} {
		if !strings.Contains(got, good) {
			t.Errorf("expected %q in rendered table:\n%s", good, got)
		}
	}
	if lineCount(got) != lineCount(powertrain) {
		t.Errorf("table row count changed: got %d want %d\n%s", lineCount(got), lineCount(powertrain), got)
	}
}

func lineCount(s string) int { return strings.Count(s, "\n") + 1 }
