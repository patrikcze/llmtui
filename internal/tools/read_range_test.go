package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/patrikcze/llmtui/internal/provider"
)

// numberedLines builds "line 1\nline 2\n…\nline n\n".
func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func writeTemp(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileLegacyWholeFileUnchanged(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", numberedLines(20))
	got, err := NewRunner(root, 64).readFile("f.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != numberedLines(20) {
		t.Fatalf("legacy read changed output:\n%q", got)
	}
	if strings.Contains(got, "[read_file:") {
		t.Fatal("legacy read must not add a range header")
	}
}

func TestReadFileRange(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", numberedLines(500))
	r := NewRunner(root, 64)

	tests := []struct {
		name           string
		offset, limit  int
		wantFirst      string
		wantLast       string
		wantHeaderPart string
	}{
		{"first lines", 1, 100, "line 1\n", "line 100\n", "lines 1-100 of 500, next_offset=101"},
		{"middle range", 301, 200, "line 301\n", "line 500\n", "lines 301-500 of 500, end of file"},
		{"offset only defaults limit", 51, 0, "line 51\n", "line 250\n", "lines 51-250 of 500, next_offset=251"},
		{"limit only starts at 1", 0, 5, "line 1\n", "line 5\n", "lines 1-5 of 500, next_offset=6"},
		{"single final line", 500, 1, "line 500\n", "line 500\n", "lines 500-500 of 500, end of file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.readFile("f.txt", tc.offset, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			header, body, ok := strings.Cut(got, "\n\n")
			if !ok {
				t.Fatalf("no header/body split in %q", got)
			}
			if !strings.Contains(header, tc.wantHeaderPart) {
				t.Fatalf("header = %q, want it to contain %q", header, tc.wantHeaderPart)
			}
			if !strings.HasPrefix(body, tc.wantFirst) {
				t.Fatalf("body starts %q, want prefix %q", body[:min(len(body), 20)], tc.wantFirst)
			}
			if !strings.HasSuffix(body, tc.wantLast) {
				t.Fatalf("body ends %q, want suffix %q", body[max(0, len(body)-20):], tc.wantLast)
			}
			// Content is verbatim: no artificial "N: " line-number prefixes.
			for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
				if !strings.HasPrefix(line, "line ") {
					t.Fatalf("line %q is not verbatim file content", line)
				}
			}
		})
	}
}

func TestReadFileRangeEndingExactlyAtEOF(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", numberedLines(10))
	got, err := NewRunner(root, 64).readFile("f.txt", 6, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "lines 6-10 of 10, end of file") {
		t.Fatalf("header = %q", got)
	}
	if strings.Contains(got, "next_offset") {
		t.Fatal("no next_offset at EOF")
	}
}

func TestReadFileRangeNoTrailingNewline(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", "alpha\nbeta\ngamma") // no trailing \n
	got, err := NewRunner(root, 64).readFile("f.txt", 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(got, "\n\n")
	if body != "beta\ngamma" {
		t.Fatalf("body = %q, want verbatim tail without an invented newline", body)
	}
	if !strings.Contains(got, "lines 2-3 of 3") {
		t.Fatalf("header = %q", got)
	}
}

func TestReadFileRangeOffsetBeyondEOF(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", numberedLines(12))
	_, err := NewRunner(root, 64).readFile("f.txt", 900, 10)
	if err == nil {
		t.Fatal("want an error for an offset past EOF")
	}
	if !strings.Contains(err.Error(), "900") || !strings.Contains(err.Error(), "12 lines") {
		t.Fatalf("error = %v, want it to name the offset and the line count", err)
	}
}

func TestReadFileRangeInvalidBounds(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.txt", numberedLines(5))
	r := NewRunner(root, 64)
	for _, tc := range []struct {
		name          string
		offset, limit int
	}{
		{"negative offset", -1, 10},
		{"negative limit", 1, -3},
		{"limit over hard cap", 1, MaxReadLimit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.readFile("f.txt", tc.offset, tc.limit); err == nil {
				t.Fatal("want a validation error")
			}
		})
	}
}

func TestReadFileRangeEmptyFile(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "empty.txt", "")
	got, err := NewRunner(root, 64).readFile("empty.txt", 1, 10)
	if err == nil {
		t.Fatalf("want offset-past-EOF error for an empty file, got %q", got)
	}
}

func TestReadFileRangeDirectoryAndEscapeRejected(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root, 64)
	if _, err := r.readFile("d", 1, 10); err == nil {
		t.Fatal("directory should be rejected in range mode")
	}
	if _, err := r.readFile("../outside.txt", 1, 10); err == nil {
		t.Fatal("workspace escape should be rejected in range mode")
	}
}

func TestReadFileRangeUTF8AndByteCap(t *testing.T) {
	root := t.TempDir()
	// One very long line that exceeds the 1 KB cap, with multibyte runes.
	long := "prefix " + strings.Repeat("€", 2000) + "\nsecond line\n"
	writeTemp(t, root, "wide.txt", long)
	got, err := NewRunner(root, 1).readFile("wide.txt", 1, 5)
	if err != nil {
		t.Fatal(err)
	}
	_, body, _ := strings.Cut(got, "\n\n")
	if !utf8.ValidString(body) {
		t.Fatal("range output is not valid UTF-8")
	}
	if len(body) > 1200 {
		t.Fatalf("range output is not byte-bounded: %d bytes", len(body))
	}
	if !strings.Contains(got, "read limit") {
		t.Fatalf("expected a read-limit note in header %q", got)
	}
}

// A line range must not become a way to read a secret file without the
// approval that a whole-file read of it would trigger.
func TestReadFileRangeStillTriggersSecretReadApproval(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	r.Guardrails.RequireApprovalForSecretReads = true
	plain := Call{Tool: ToolReadFile, Path: "notes.txt", Offset: 1, Limit: 50}
	secret := Call{Tool: ToolReadFile, Path: ".env", Offset: 1, Limit: 50}
	if r.NeedsApproval(plain) {
		t.Fatal("a ranged read of an ordinary file should not need approval")
	}
	if !r.NeedsApproval(secret) {
		t.Fatal("a ranged read of a secret file must still need approval")
	}
}

func TestReadFileRangeNativeDecoding(t *testing.T) {
	got := CallsFromNative([]provider.ToolCall{
		{ID: "c1", Name: ToolReadFile, Arguments: `{"path":"a.go","offset":10,"limit":40}`},
	})
	if len(got) != 1 || got[0].Offset != 10 || got[0].Limit != 40 {
		t.Fatalf("call = %+v", got[0])
	}

	bad := CallsFromNative([]provider.ToolCall{
		{ID: "c2", Name: ToolReadFile, Arguments: `{"path":"a.go","limit":9000}`},
	})
	if bad[0].InputErr == "" {
		t.Fatal("limit over the cap should produce InputErr")
	}
}

func TestReadFileRangeFencedDecoding(t *testing.T) {
	calls := Parse("```tool read_file internal/x.go\n{\"offset\":201,\"limit\":150}\n```")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if calls[0].Offset != 201 || calls[0].Limit != 150 || calls[0].Body != "" {
		t.Fatalf("call = %+v", calls[0])
	}

	// Legacy fenced form — no body — still means a whole-file read.
	legacy := Parse("```tool read_file internal/x.go\n```")
	if len(legacy) != 1 || legacy[0].Offset != 0 || legacy[0].Limit != 0 {
		t.Fatalf("legacy call = %+v", legacy[0])
	}

	// A non-JSON body is ignored, not turned into a hard error.
	loose := Parse("```tool read_file internal/x.go\nnot json at all\n```")
	if len(loose) != 1 || loose[0].InputErr != "" {
		t.Fatalf("loose call = %+v", loose[0])
	}
}
