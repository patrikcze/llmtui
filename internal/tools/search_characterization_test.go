package tools

import (
	"fmt"
	"strings"
	"testing"
)

// This file characterizes CURRENT grepFiles behavior (Phase 0 of the
// next-generation-tool-runtime plan, evidence row L5). It adds new tests
// beside search_test.go without changing any existing assertion there.

// TestGrepFilesOmitsCoverageMetadataForSkippedLargeFiles documents that a
// file skipped for exceeding the read cap leaves no trace at all: not its
// name, not a count, not even a generic "some files were too large" note.
func TestGrepFilesOmitsCoverageMetadataForSkippedLargeFiles(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "small.txt", "MARKER small hit\n")
	// oversized.txt exceeds the 1 KB runner cap; grepFiles's per-file loop
	// (search.go) does "info.Size() > int64(limit): continue" with no output
	// signal that this file was ever seen or skipped.
	writeSearchFixture(t, root, "oversized.txt", "MARKER "+strings.Repeat("x", 2000)+"\n")
	r := NewRunner(root, 1) // 1 KB grep/read cap

	res := r.Execute(Call{Tool: ToolGrep, Body: "MARKER"})
	if res.Err != nil {
		t.Fatalf("grep: %v", res.Err)
	}
	if !strings.Contains(res.Output, "small.txt") {
		t.Fatalf("expected the small file's match, got %q", res.Output)
	}
	// PHASE 0: current gap, closed in Phase 2a (§L5).
	if strings.Contains(res.Output, "oversized.txt") {
		t.Fatalf("did not expect the oversized file to be named anywhere, got %q", res.Output)
	}
	if strings.Contains(res.Output, "too large") || strings.Contains(res.Output, "skipped") {
		t.Fatalf("did not expect any skipped-file disclosure, got %q", res.Output)
	}
}

// TestGrepFilesTruncationDisclosesCountNotWhichFilesWereDropped documents the
// other half of the L5 gap: once the 200-match cap is hit, the output names a
// bare count ("… results limited to N matches …") but never says which
// specific files' matches were the ones dropped.
func TestGrepFilesTruncationDisclosesCountNotWhichFilesWereDropped(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < maxSearchResults+5; i++ {
		writeSearchFixture(t, root, fmt.Sprintf("f%03d.txt", i), fmt.Sprintf("MARKER hit %d\n", i))
	}
	r := NewRunner(root, 512)

	res := r.Execute(Call{Tool: ToolGrep, Body: "MARKER"})
	if res.Err != nil {
		t.Fatalf("grep: %v", res.Err)
	}
	if !strings.Contains(res.Output, fmt.Sprintf("limited to %d matches", maxSearchResults)) {
		t.Fatalf("expected a bare-count truncation note, got %q", res.Output)
	}
	// Files sort lexically, so f000..f199 (the first 200) are kept and
	// f200..f204 are dropped — but nothing in the output says so by name.
	if !strings.Contains(res.Output, "f199.txt") {
		t.Fatalf("expected f199.txt (within the 200-match cap) to be present, got %q", res.Output)
	}
	// PHASE 0: current gap, closed in Phase 2a (§L5) — the dropped files are
	// never named, individually or as a list, only implied by the count.
	if strings.Contains(res.Output, "f200.txt") {
		t.Fatalf("did not expect the first dropped file to be named, got %q", res.Output)
	}
}
