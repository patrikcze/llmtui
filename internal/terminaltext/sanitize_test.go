package terminaltext

import (
	"strings"
	"testing"
)

func TestSanitizeStripsTerminalSequencesAndControls(t *testing.T) {
	input := "safe\n" +
		"\x1b]0;spoofed title\x07" +
		"\x1b]52;c;Y2xpcGJvYXJk\x1b\\" +
		"\x1b[2J\x1b[H" +
		"\x1bPmalicious dcs\x1b\\" +
		"after\r\x00\u009b31mred"
	got := Sanitize(input)
	if strings.ContainsAny(got, "\x1b\x07\r\x00") || strings.ContainsRune(got, '\u009b') {
		t.Fatalf("control byte survived: %q", got)
	}
	if strings.Contains(got, "spoofed title") || strings.Contains(got, "Y2xpcGJvYXJk") || strings.Contains(got, "malicious dcs") {
		t.Fatalf("escape payload survived: %q", got)
	}
	if got != "safe\nafter31mred" {
		t.Fatalf("Sanitize = %q", got)
	}
}

func TestSanitizePreservesUnicodeNewlinesAndTabs(t *testing.T) {
	want := "Příliš žluťoučký\n\t🙂"
	if got := Sanitize(want); got != want {
		t.Fatalf("Sanitize = %q, want %q", got, want)
	}
}
