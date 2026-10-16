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

// noChangeDiffMarker is the trailing text RenderWriteDiff produces when a
// write replaced a file with byte-identical content. Kept next to the
// renderer so the two never drift.
const noChangeDiffMarker = "— no changes"

// IsNoChangeDiff reports whether a write_file/edit_file result diff describes
// a write that changed nothing (the model wrote a file's current content
// back). Callers that score real progress — the agent evidence ledger — use
// it so a no-op write is not counted as a changed file.
func IsNoChangeDiff(diff string) bool {
	return strings.HasSuffix(strings.TrimSpace(diff), noChangeDiffMarker)
}

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
		return fmt.Sprintf("Update(%s) %s", path, noChangeDiffMarker)
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

// unifiedContext is the number of unchanged lines around each hunk in
// RenderUnifiedDiff, matching git's default.
const unifiedContext = 3

// RenderUnifiedDiff renders a git-style unified diff (---/+++ headers and
// @@ hunks). Unlike RenderWriteDiff it is model-facing: edit_file's dry_run
// returns it so a model can check a planned change before applying it.
func RenderUnifiedDiff(path, oldContent, newContent string) string {
	oldLines, newLines := splitLines(oldContent), splitLines(newContent)
	header := fmt.Sprintf("--- a/%s\n+++ b/%s\n", path, path)
	if len(oldLines) > diffMaxLines || len(newLines) > diffMaxLines {
		return header + fmt.Sprintf("(file too large to diff: %d → %d lines)", len(oldLines), len(newLines))
	}
	ops := diffOps(oldLines, newLines)
	// oldPos/newPos[i] are the 1-based old/new line numbers op i starts at.
	oldPos := make([]int, len(ops)+1)
	newPos := make([]int, len(ops)+1)
	oldPos[0], newPos[0] = 1, 1
	for i, op := range ops {
		oldPos[i+1], newPos[i+1] = oldPos[i], newPos[i]
		if op.kind != '+' {
			oldPos[i+1]++
		}
		if op.kind != '-' {
			newPos[i+1]++
		}
	}
	var b strings.Builder
	b.WriteString(header)
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		start := max(0, i-unifiedContext)
		end := i
		// Extend the hunk while the next change is within 2×context lines.
		for j := i; j < len(ops); j++ {
			if ops[j].kind != ' ' {
				end = j
				continue
			}
			if j-end > 2*unifiedContext {
				break
			}
		}
		end = min(len(ops)-1, end+unifiedContext)
		oldCount, newCount := oldPos[end+1]-oldPos[start], newPos[end+1]-newPos[start]
		oldStart, newStart := oldPos[start], newPos[start]
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[start : end+1] {
			fmt.Fprintf(&b, "%c%s\n", op.kind, op.text)
		}
		i = end + 1
	}
	return strings.TrimRight(b.String(), "\n")
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
