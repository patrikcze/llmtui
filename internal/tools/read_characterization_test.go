package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file characterizes CURRENT read_file behavior (Phase 0 of the
// next-generation-tool-runtime plan, evidence row L3). It adds new tests
// beside read_range_test.go without changing any existing assertion there.

// TestReadFileRangeCannotReachLinesPastByteLimitedPrefix documents the L3
// gap: a ranged read is sliced out of the same byte-limited prefix as a
// whole-file read (readFile -> io.LimitReader(file, byteLimit+1)), so a line
// that genuinely exists later in the file is unreachable no matter what
// offset is requested, and the error message itself only ever names the
// lines that fit within the KB cap, never the file's real total line count.
func TestReadFileRangeCannotReachLinesPastByteLimitedPrefix(t *testing.T) {
	root := t.TempDir()
	// 500 lines of 7-9 bytes each (~4 KB) — well past this runner's 1 KB cap.
	writeTemp(t, root, "big.txt", numberedLines(500))
	r := NewRunner(root, 1) // 1 KB read cap

	// A line comfortably inside the byte-limited prefix reads fine.
	got, err := r.readFile("big.txt", 1, 5)
	if err != nil {
		t.Fatalf("read within the prefix failed: %v", err)
	}
	if !strings.Contains(got, "line 1\n") {
		t.Fatalf("got = %q", got)
	}

	// PHASE 0: current gap, closed in Phase 3 (§L3). Line 400 is a real line
	// in the 500-line file, but it falls after however many lines fit in the
	// 1 KB byte-limited prefix, so it can never be read — and the error
	// reports only the KB-limited prefix's line count, not the true 500.
	_, err = r.readFile("big.txt", 400, 5)
	if err == nil {
		t.Fatal("want an error reading a line past the byte-limited prefix")
	}
	if !strings.Contains(err.Error(), "fit within") {
		t.Fatalf("err = %v, want it to name the KB-limited prefix, not a true past-EOF condition", err)
	}
	if strings.Contains(err.Error(), "500 lines") {
		t.Fatalf("err = %v, this call must not be able to know the file's real total line count", err)
	}
}

// TestReadFileWholeFileTruncatesOversizedFirstLine documents current
// whole-file (legacy) truncation when a single line alone exceeds the read
// cap: the cap is a hard byte boundary with no line awareness, so the
// visible text is cut mid-line at exactly the cap.
func TestReadFileWholeFileTruncatesOversizedFirstLine(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("a", 5000) // one line, no trailing newline
	writeTemp(t, root, "oneline.txt", long)
	r := NewRunner(root, 1) // 1 KB cap

	got, err := r.readFile("oneline.txt", 0, 0) // whole-file legacy read
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body, rest, ok := strings.Cut(got, "\n… truncated")
	if !ok {
		t.Fatalf("expected a truncation notice, got %q", got)
	}
	if len(body) != 1024 {
		t.Fatalf("visible body = %d bytes, want exactly the 1024 byte cap (no line-boundary awareness)", len(body))
	}
	if !strings.Contains(rest, "1024 of 5000 bytes shown") {
		t.Fatalf("truncation notice = %q, want it to name 1024 of 5000", rest)
	}
}

// TestReadFileWholeFileEmptyFile documents whole-file read_file behavior on
// a zero-byte file. This is already correct: an empty result and no error,
// matching the legacy whole-file contract exactly — no gap to close here.
func TestReadFileWholeFileEmptyFile(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "empty.txt", "")
	got, err := NewRunner(root, 64).readFile("empty.txt", 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "" {
		t.Fatalf("got = %q, want an empty string with no error", got)
	}
}

// BenchmarkReadSmall measures the current (unbounded-prefix) whole-file read
// path at three sizes so its cost and visible-byte count are numbers, not
// just a code-reading claim. It characterizes BASELINE behavior only; it does
// not add any bounded-capture implementation.
func BenchmarkReadSmall(b *testing.B) {
	sizes := []struct {
		name  string
		bytes int
	}{
		{"1KiB", 1 << 10},
		{"16KiB", 16 << 10},
		{"256KiB", 256 << 10},
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			root := b.TempDir()
			content := strings.Repeat("a", sz.bytes-1) + "\n"
			writeTempBench(b, root, "f.txt", content)
			// 1 MiB cap: large enough that none of these sizes truncate, so
			// the benchmark measures the whole-file read path, not the
			// truncation path.
			r := NewRunner(root, 1024)

			b.ReportAllocs()
			b.ResetTimer()
			var visible int
			for i := 0; i < b.N; i++ {
				out, err := r.readFile("f.txt", 0, 0)
				if err != nil {
					b.Fatal(err)
				}
				visible = len(out)
			}
			b.ReportMetric(float64(visible), "visible_bytes")
		})
	}
}

func writeTempBench(b *testing.B, root, name, content string) {
	b.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		b.Fatal(err)
	}
}
