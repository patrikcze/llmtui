package terminaltext

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestByteTruncationPreservesUTF8(t *testing.T) {
	input := "abc€🙂"
	for _, maxBytes := range []int{1, 4, 5, 7, 8} {
		t.Run(fmt.Sprintf("max_%d", maxBytes), func(t *testing.T) {
			head, _ := TruncateBytes(input, maxBytes)
			tail, _ := TailBytes(input, maxBytes)
			if !utf8.ValidString(head) || len(head) > maxBytes {
				t.Fatalf("head = %q (%d bytes)", head, len(head))
			}
			if !utf8.ValidString(tail) || len(tail) > maxBytes {
				t.Fatalf("tail = %q (%d bytes)", tail, len(tail))
			}
		})
	}
}
