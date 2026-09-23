//go:build windows

package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests only run on windows and are therefore unexecuted by this
// phase's implementation environment (macOS); see phase-1b-report.md for
// what was verified from the Go standard library source instead. They
// document, and would catch a regression in, the intentionally-inert
// windows half of the platform split in file_replace_windows.go: no
// directory-fsync primitive and no portable hard-link count exist on this
// platform the way they do on Unix, so both functions are no-ops rather
// than silently claiming coverage they cannot provide.

func TestSyncParentDirIsNoOpOnWindows(t *testing.T) {
	root := t.TempDir()
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close root: %v", err)
		}
	})
	if err := syncParentDir(r, "."); err != nil {
		t.Fatalf("syncParentDir must be a no-op on windows, got %v", err)
	}
	if err := syncParentDir(r, "does-not-exist"); err != nil {
		t.Fatalf("syncParentDir must be a no-op on windows even for a missing directory, got %v", err)
	}
}

func TestCheckNotHardLinkedIsNoOpOnWindows(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNotHardLinked(info); err != nil {
		t.Fatalf("checkNotHardLinked must be a documented no-op on windows, got %v", err)
	}
}
