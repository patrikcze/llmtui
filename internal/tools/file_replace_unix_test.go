//go:build !windows

package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncParentDirSucceedsOnRealDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("sync root dir: %v", err)
	}
	if err := syncParentDir(r, "sub"); err != nil {
		t.Fatalf("sync subdirectory: %v", err)
	}
}

func TestSyncParentDirFailsOnMissingDirectory(t *testing.T) {
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

	if err := syncParentDir(r, "does-not-exist"); err == nil {
		t.Fatal("want an error syncing a directory that does not exist")
	}
}

func TestCheckNotHardLinkedRejectsMultiplyLinkedFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(root, "b.txt")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNotHardLinked(info); err == nil {
		t.Fatal("want a hard-linked target rejected")
	}
}

func TestCheckNotHardLinkedAllowsSingleLinkFile(t *testing.T) {
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
		t.Fatalf("single-link file rejected: %v", err)
	}
}

func TestCheckNotHardLinkedIgnoresUnavailableMetadata(t *testing.T) {
	// A FileInfo whose Sys() is not *syscall.Stat_t (the shape a fake or a
	// future platform variant might produce) must be treated as "nothing to
	// check", never as a false rejection.
	if err := checkNotHardLinked(fakeFileInfo{}); err != nil {
		t.Fatalf("want nil when platform metadata is unavailable, got %v", err)
	}
}

type fakeFileInfo struct{ os.FileInfo }

func (fakeFileInfo) Sys() any { return nil }
