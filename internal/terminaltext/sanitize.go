// Package terminaltext neutralizes untrusted text before it is rendered in a
// terminal. It deliberately lives below the TUI so provider, MCP, RAG, and
// web boundaries can share exactly one policy.
package terminaltext

import (
	"strings"
	"unicode"
	"unicode/utf8"

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

// TruncateBytes returns valid UTF-8 containing at most maxBytes. Invalid
// input bytes become replacement runes, and neither a replacement nor a
// multibyte rune is split at the boundary.
func TruncateBytes(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", s != ""
	}
	valid := strings.ToValidUTF8(s, "�")
	if len(valid) <= maxBytes {
		return valid, valid != s
	}
	cut := maxBytes
	for cut > 0 && !utf8.ValidString(valid[:cut]) {
		cut--
	}
	return valid[:cut], true
}

// TailBytes returns the largest valid UTF-8 suffix no larger than maxBytes.
func TailBytes(s string, maxBytes int) (string, bool) {
	valid := strings.ToValidUTF8(s, "�")
	if maxBytes <= 0 {
		return "", valid != ""
	}
	if len(valid) <= maxBytes {
		return valid, valid != s
	}
	start := len(valid) - maxBytes
	for start < len(valid) && !utf8.RuneStart(valid[start]) {
		start++
	}
	return valid[start:], true
}
