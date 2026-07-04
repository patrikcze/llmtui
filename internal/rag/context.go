package rag

import (
	"fmt"
	"strings"
)

// FormatContext renders retrieval results as a labeled reference block for
// prompt composition. Each snippet carries its source path, line range, and
// the query terms it matched, so the model (and the user, via /prompt
// preview) can see exactly what was retrieved and why. maxChars caps the
// total size; snippets past the cap are dropped rather than truncated
// mid-line. Returns "" when there is nothing to include.
func FormatContext(results []Result, maxChars int) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range results {
		entry := formatEntry(r)
		if maxChars > 0 && b.Len()+len(entry) > maxChars && b.Len() > 0 {
			break
		}
		b.WriteString(entry)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatEntry(r Result) string {
	reason := "keyword match"
	if len(r.MatchedTerms) > 0 {
		reason = fmt.Sprintf("matched %q", strings.Join(r.MatchedTerms, ", "))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- file: %s lines %d-%d\n", r.Chunk.Path, r.Chunk.StartLine, r.Chunk.EndLine)
	fmt.Fprintf(&b, "  reason: %s\n", reason)
	b.WriteString("  content:\n")
	for _, line := range strings.Split(r.Chunk.Text, "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}
