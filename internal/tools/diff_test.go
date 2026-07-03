package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderWriteDiffCreate(t *testing.T) {
	got := RenderWriteDiff("a.sh", "", "#!/bin/sh\necho hi\n", false)
	lines := strings.Split(got, "\n")
	if lines[0] != "Create(a.sh) — 2 line(s)" {
		t.Errorf("header = %q", lines[0])
	}
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "+") || !strings.Contains(lines[2], "echo hi") {
		t.Errorf("body = %q", lines[1:])
	}
}

func TestRenderWriteDiffUpdate(t *testing.T) {
	oldC := "line one\nline two\nline three\nline four\nline five\n"
	newC := "line one\nline 2\nline three\nline four\nline five\nline six\n"
	got := RenderWriteDiff("f.txt", oldC, newC, true)

	if !strings.HasPrefix(got, "Update(f.txt) — added 2 line(s), removed 1 line(s)") {
		t.Fatalf("header wrong: %q", got)
	}
	if !strings.Contains(got, "- ") || !strings.Contains(got, "line two") {
		t.Errorf("removed line missing: %q", got)
	}
	if !strings.Contains(got, "+ ") || !strings.Contains(got, "line 2") {
		t.Errorf("added line missing: %q", got)
	}
	// Context lines carry no marker; unchanged middles far from changes are
	// not needed here (the file is small), but line numbers must be present.
	if !strings.Contains(got, "   1  line one") {
		t.Errorf("context line with number missing: %q", got)
	}
}

func TestRenderWriteDiffElidesFarContext(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 1; i <= 30; i++ {
		oldB.WriteString("same line\n")
		newB.WriteString("same line\n")
	}
	newB.WriteString("added at end\n")
	got := RenderWriteDiff("f.txt", oldB.String(), newB.String(), true)
	if !strings.Contains(got, "  …") {
		t.Errorf("far context not elided: %q", got)
	}
	if strings.Count(got, "same line") > diffContext {
		t.Errorf("too much context kept: %q", got)
	}
}

func TestRenderWriteDiffNoChanges(t *testing.T) {
	got := RenderWriteDiff("f.txt", "same\n", "same\n", true)
	if got != "Update(f.txt) — no changes" {
		t.Errorf("got %q", got)
	}
}

func TestWriteFileProducesDiff(t *testing.T) {
	root := t.TempDir()
	r := NewRunner(root, 64)

	// A fresh file yields a Create diff.
	res := r.Execute(Call{Tool: ToolWriteFile, Path: "note.txt", Body: "hello\nworld\n"})
	if res.Err != nil {
		t.Fatalf("write: %v", res.Err)
	}
	if !strings.HasPrefix(res.Diff, "Create(note.txt)") {
		t.Errorf("create diff = %q", res.Diff)
	}

	// Overwriting yields an Update diff against the previous content.
	res = r.Execute(Call{Tool: ToolWriteFile, Path: "note.txt", Body: "hello\nthere\n"})
	if res.Err != nil {
		t.Fatalf("overwrite: %v", res.Err)
	}
	if !strings.HasPrefix(res.Diff, "Update(note.txt) — added 1 line(s), removed 1 line(s)") {
		t.Errorf("update diff header = %q", res.Diff)
	}
	if !strings.Contains(res.Diff, "- ") || !strings.Contains(res.Diff, "world") {
		t.Errorf("old line missing from diff: %q", res.Diff)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "note.txt")); string(data) != "hello\nthere\n" {
		t.Errorf("file content = %q", data)
	}

	// The model-facing output stays the compact confirmation.
	if res.Output != "wrote 12 bytes to note.txt" {
		t.Errorf("output = %q", res.Output)
	}
}

func TestCollectDiffs(t *testing.T) {
	results := []Result{
		{Call: Call{Tool: ToolListDir}, Output: "a\nb"},
		{Call: Call{Tool: ToolWriteFile, Path: "x"}, Diff: "Create(x) — 1 line(s)\n+    1  hi"},
	}
	got := CollectDiffs(results)
	if !strings.HasPrefix(got, "Create(x)") {
		t.Errorf("got %q", got)
	}
	if CollectDiffs(results[:1]) != "" {
		t.Error("non-write results must contribute no diff")
	}
}
