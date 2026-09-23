package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file characterizes CURRENT edit_file behavior (Phase 0 of the
// next-generation-tool-runtime plan, evidence row L4). It adds a new test
// beside edit_file_test.go — including TestEditFileStaleContentGuard, which
// this file does not touch and which must keep passing unchanged — from a
// fresh fixture, exercising a distinct scenario: an external mutation of an
// unrelated line between a model's read and its later edit_file call.

// TestEditFileSucceedsDespiteUnrelatedModificationSinceRead documents that
// edit_file carries no full-file version binding to whatever the model
// originally read. editFile (tools.go) always reads the file fresh, inside
// its own call, immediately before writing (editFile ->
// writeFileChecked(rel, updated, &current)) — that freshness check only ever
// protects against a change happening during the edit_file call itself. It
// has no memory of, and never compares against, what an earlier separate
// read_file call actually returned. So an external actor (another process, a
// second agent, a human editor) that changes an unrelated line in the window
// between the model's read and its edit_file call is invisible to this
// guard, and the edit still succeeds.
func TestEditFileSucceedsDespiteUnrelatedModificationSinceRead(t *testing.T) {
	root := t.TempDir()
	writeTemp(t, root, "f.go", "package x\n\nconst A = 1\nconst B = 2\n")
	r := NewRunner(root, 64)

	// The model's earlier read_file call observes B = 2.
	original, err := r.readFile("f.go", 0, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(original, "const B = 2") {
		t.Fatalf("original read = %q, want it to contain the original B value", original)
	}

	// An external actor changes the unrelated B line before the model's
	// edit_file call arrives.
	writeTemp(t, root, "f.go", "package x\n\nconst A = 1\nconst B = 99\n")

	// PHASE 0: current gap, closed in Phase 5 (§L4) — this edit succeeds even
	// though the file has changed since the model's read, because edit_file
	// never bound itself to that earlier read's content.
	res := r.Execute(Call{
		Tool: ToolEditFile, Path: "f.go",
		OldText: "const A = 1", NewText: "const A = 42",
	})
	if res.Err != nil {
		t.Fatalf("edit failed: %v (documents today's behavior, not a desired one)", res.Err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "f.go"))
	want := "package x\n\nconst A = 42\nconst B = 99\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}
