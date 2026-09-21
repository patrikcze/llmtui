package rag

import (
	"fmt"
	"strings"

	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// FormatContext renders retrieval results as a labeled reference block for
// prompt composition. Each snippet carries its source path, line range, and
// the query terms it matched, so the model (and the user, via /prompt
// preview) can see exactly what was retrieved and why. maxChars is a hard cap
// on the whole block, citation framing included: snippets past the cap are
// dropped, and a first snippet that alone exceeds it is cut at a line
// boundary with a truncation marker rather than exceeding the budget. A
// snippet with no room for even one content line is omitted. Returns ""
// when there is nothing to include.
func FormatContext(results []Result, maxChars int) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range results {
		entry := formatEntry(r)
		if maxChars > 0 && b.Len()+len(entry) > maxChars {
			// Only the top-ranked snippet is cut to fit; lower-ranked ones
			// that do not fit whole are dropped.
			if b.Len() == 0 {
				b.WriteString(truncateEntry(r, maxChars))
			}
			break
		}
		b.WriteString(entry)
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncationMarker replaces the tail of a snippet that had to be cut.
const truncationMarker = "    … (truncated)\n"

func entryHeader(r Result) string {
	path := terminaltext.Sanitize(r.Chunk.Path)
	reason := "keyword match"
	if len(r.MatchedTerms) > 0 {
		reason = fmt.Sprintf("matched %q", terminaltext.Sanitize(strings.Join(r.MatchedTerms, ", ")))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- file: %s lines %d-%d\n", path, r.Chunk.StartLine, r.Chunk.EndLine)
	fmt.Fprintf(&b, "  reason: %s\n", reason)
	b.WriteString("  content:\n")
	return b.String()
}

func formatEntry(r Result) string {
	var b strings.Builder
	b.WriteString(entryHeader(r))
	for _, line := range strings.Split(terminaltext.Sanitize(r.Chunk.Text), "\n") {
		b.WriteString("    " + line + "\n")
	}
	return b.String()
}

// truncateEntry renders r within limit bytes, cutting at a line boundary and
// ending with truncationMarker. It returns "" when not even one content line
// fits beside the citation header: a citation with no content is noise.
func truncateEntry(r Result, limit int) string {
	header := entryHeader(r)
	room := limit - len(header) - len(truncationMarker)
	if room <= 0 {
		return ""
	}
	var cut strings.Builder
	for _, line := range strings.Split(terminaltext.Sanitize(r.Chunk.Text), "\n") {
		l := "    " + line + "\n"
		if cut.Len()+len(l) > room {
			break
		}
		cut.WriteString(l)
	}
	if cut.Len() == 0 {
		return ""
	}
	return header + cut.String() + truncationMarker
}
