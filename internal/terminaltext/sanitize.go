// Package terminaltext neutralizes untrusted text before it is rendered in a
// terminal. It deliberately lives below the TUI so provider, MCP, RAG, and
// web boundaries can share exactly one policy.
package terminaltext

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Sanitize removes ANSI/VT escape sequences (including CSI, OSC, and DCS)
// and remaining C0/C1/Unicode control characters. Newlines and tabs are kept
// because they express ordinary document layout rather than terminal state.
func Sanitize(s string) string {
	stripped := ansi.Strip(s)
	if stripped == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, stripped)
}
