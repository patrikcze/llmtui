package tools

import (
	"fmt"
	"strings"
)

// Diff rendering for write_file actions. The result is display-only text
// the TUI colorizes by line prefix; it is never sent to the model. Format:
//
//	Update(path) — added 2 line(s), removed 1 line(s)
//	    1  unchanged context (new line number)
//	-   2  removed line (old line number)
//	+   2  added line (new line number)
//	  …
const (
	// diffMaxLines bounds the LCS table (diffMaxLines² cells); larger files
	// get a summary header instead of a line diff.
	diffMaxLines = 1500
	// diffContext is how many unchanged lines to keep around each change.
	diffContext = 2
)

// RenderWriteDiff builds the display diff for one write_file execution.
func RenderWriteDiff(path, oldContent, newContent string, existed bool) string {
	newLines := splitLines(newContent)
	if !existed {
		var b strings.Builder
		fmt.Fprintf(&b, "Create(%s) — %d line(s)\n", path, len(newLines))
		for i, l := range newLines {
			fmt.Fprintf(&b, "+ %4d  %s\n", i+1, l)
		}
		return strings.TrimRight(b.String(), "\n")
	}

	oldLines := splitLines(oldContent)
	if len(oldLines) > diffMaxLines || len(newLines) > diffMaxLines {
		return fmt.Sprintf("Update(%s) — file replaced (%d → %d lines, too large to diff)",
			path, len(oldLines), len(newLines))
	}

	ops := diffOps(oldLines, newLines)
	added, removed := 0, 0
	for _, op := range ops {
		switch op.kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return fmt.Sprintf("Update(%s) — no changes", path)
	}

	// Keep only changed lines plus a little context; elide the rest.
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind == ' ' {
			continue
		}
		for j := max(0, i-diffContext); j <= min(len(ops)-1, i+diffContext); j++ {
			keep[j] = true
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Update(%s) — added %d line(s), removed %d line(s)\n", path, added, removed)
	last := -1
	for i, op := range ops {
		if !keep[i] {
			continue
		}
		if i != last+1 {
			b.WriteString("  …\n")
		}
		last = i
		switch op.kind {
		case '+':
			fmt.Fprintf(&b, "+ %4d  %s\n", op.newNum, op.text)
		case '-':
			fmt.Fprintf(&b, "- %4d  %s\n", op.oldNum, op.text)
		default:
			fmt.Fprintf(&b, "  %4d  %s\n", op.newNum, op.text)
		}
	}
	if last != len(ops)-1 {
		b.WriteString("  …\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

type diffOp struct {
	kind   byte // ' ' context, '+' added, '-' removed
	oldNum int  // 1-based old line number ('-' and context)
	newNum int  // 1-based new line number ('+' and context)
	text   string
}

// diffOps computes a line diff via the classic LCS dynamic program.
func diffOps(a, b []string) []diffOp {
	n, m := len(a), len(b)
	// lcs[i][j] = length of the LCS of a[i:] and b[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{kind: ' ', oldNum: i + 1, newNum: j + 1, text: b[j]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{kind: '-', oldNum: i + 1, text: a[i]})
			i++
		default:
			ops = append(ops, diffOp{kind: '+', newNum: j + 1, text: b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{kind: '-', oldNum: i + 1, text: a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{kind: '+', newNum: j + 1, text: b[j]})
	}
	return ops
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// CollectDiffs joins the display diffs of a batch (for the fenced-protocol
// results message, which carries all calls in one message).
func CollectDiffs(results []Result) string {
	var parts []string
	for _, res := range results {
		if res.Diff != "" {
			parts = append(parts, res.Diff)
		}
	}
	return strings.Join(parts, "\n")
}
