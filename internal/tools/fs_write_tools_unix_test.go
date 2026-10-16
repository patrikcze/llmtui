//go:build !windows

package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// Workspace Tool Safety Invariant: confinement is enforced on the resolved,
// symlink-evaluated path — including a not-yet-existing destination inside a
// symlinked directory, for both create_directory and both ends of move_file.
func TestStructuralWriteToolsRefuseSymlinkEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSearchFixture(t, outside, "victim.txt", "outside")
	writeSearchFixture(t, root, "a.txt", "a")
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim.txt"), filepath.Join(root, "victim-link")); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(root, 64)

	for name, c := range map[string]Call{
		"mkdir through escaping link":     {Tool: ToolCreateDirectory, Path: "out/new/dir"},
		"move into escaping link":         {Tool: ToolMoveFile, Path: "a.txt", Destination: "out/new.txt"},
		"move out of escaping link":       {Tool: ToolMoveFile, Path: "out/victim.txt", Destination: "stolen.txt"},
		"move a symlink pointing outside": {Tool: ToolMoveFile, Path: "victim-link", Destination: "renamed-link"},
	} {
		t.Run(name, func(t *testing.T) {
			if res := r.Execute(c); res.Err == nil {
				t.Fatalf("%s was not refused", c.Describe())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !os.IsNotExist(err) {
		t.Fatal("create_directory wrote outside the workspace")
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("move_file wrote outside the workspace")
	}
	if readFixture(t, outside, "victim.txt") != "outside" {
		t.Fatal("an outside file was moved")
	}
}

// Even a symlink that stays inside the workspace is not written through:
// write admission rejects any symlink component, as for write_file.
func TestStructuralWriteToolsRejectInternalSymlinks(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "real/a.txt", "a")
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlink: %v", err)
	}
	r := NewRunner(root, 64)
	for _, c := range []Call{
		{Tool: ToolCreateDirectory, Path: "alias/sub"},
		{Tool: ToolMoveFile, Path: "alias/a.txt", Destination: "b.txt"},
		{Tool: ToolMoveFile, Path: "real/a.txt", Destination: "alias/b.txt"},
	} {
		if res := r.Execute(c); res.Err == nil {
			t.Errorf("%s was not refused", c.Describe())
		}
	}
}
