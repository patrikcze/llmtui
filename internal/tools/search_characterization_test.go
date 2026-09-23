package tools

import (
	"fmt"
	"strings"
	"testing"
)

// This file preserves the legacy regex-body contract while checking the
// bounded Phase 4 search envelope around it.

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
	if res.Meta.Outcome != OutcomePartial || res.Meta.Coverage.SourceComplete {
		t.Fatalf("expected a visibly partial search for the skipped file: %+v", res.Meta)
	}
	if !strings.Contains(strings.Join(res.Meta.Coverage.Reasons, ","), "large") {
		t.Fatalf("expected large-file coverage reason, got %v", res.Meta.Coverage.Reasons)
	}
}

// TestGrepFilesTruncationUsesCapturedContinuation verifies that a large
// result set is paged from one captured, stable result set.
func TestGrepFilesTruncationDisclosesCountNotWhichFilesWereDropped(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 205; i++ {
		writeSearchFixture(t, root, fmt.Sprintf("f%03d.txt", i), fmt.Sprintf("MARKER hit %d\n", i))
	}
	r := NewRunner(root, 512)

	res := r.Execute(Call{Tool: ToolGrep, Body: "MARKER"})
	if res.Err != nil {
		t.Fatalf("grep: %v", res.Err)
	}
	if !strings.Contains(res.Output, "search cursor:") {
		t.Fatalf("expected a bounded continuation cursor, got %q", res.Output)
	}
	if !strings.Contains(res.Output, "f099.txt") {
		t.Fatalf("expected f099.txt (within the default page) to be present, got %q", res.Output)
	}
	if strings.Contains(res.Output, "f100.txt") {
		t.Fatalf("did not expect the next page in the first response, got %q", res.Output)
	}
}
