package tools

import (
	"strings"
	"testing"
)

// BenchmarkWriteFileSmall measures writeFileChecked's cost for a small
// (~1 KiB) file, overwriting the same path every iteration. Phase 1b
// (.claude/tasks/plans/next-generation-tool-runtime.md §31 checklist bullet
// 6, "measure small edit and sync costs separately") asks for a rough
// before/after comparison against the O_TRUNC direct-open primitive this
// benchmark's shape was first run against (see phase-1b-report.md for the
// recorded numbers on both sides) rather than a full §29 benchmark suite.
func BenchmarkWriteFileSmall(b *testing.B) {
	root := b.TempDir()
	r := NewRunner(root, 512)
	content := strings.Repeat("line of sample content\n", 45) // ~1 KiB
	// One creating write outside the timed loop so every timed iteration is
	// an overwrite of an existing file (the common edit_file/write_file
	// case), not a one-time create.
	if _, _, err := r.writeFileChecked("bench.txt", content, nil); err != nil {
		b.Fatalf("seed write: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := r.writeFileChecked("bench.txt", content, nil); err != nil {
			b.Fatalf("write: %v", err)
		}
	}
}

// BenchmarkEditFileSmall measures editFile's cost (read + exact replace +
// writeFileChecked with an expectCurrent precondition) for a small file, the
// shape that exercises the new step-5 pre-publish recheck.
func BenchmarkEditFileSmall(b *testing.B) {
	root := b.TempDir()
	r := NewRunner(root, 512)
	base := strings.Repeat("line of sample content\n", 45)
	if _, _, _, err := r.writeFileMeta("bench.txt", base+"MARKER\n"); err != nil {
		b.Fatalf("seed write: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	// editFile refuses an identical old_text/new_text pair before ever
	// touching disk (see the "old == new" case in edit_file_test.go), so
	// alternate the marker each iteration to force a real write every time.
	for i := 0; i < b.N; i++ {
		from, want := "MARKER\n", "MARKER2\n"
		if i%2 == 1 {
			from, want = "MARKER2\n", "MARKER\n"
		}
		if _, _, _, err := r.editFile("bench.txt", from, want, nil); err != nil {
			b.Fatalf("edit: %v", err)
		}
	}
}
