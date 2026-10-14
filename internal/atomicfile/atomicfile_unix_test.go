//go:build !windows

package atomicfile

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func fileMode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// A fixed "<path>.tmp" used to be reused when a crash left one behind, and its
// old, looser mode survived the rename. The helper must neither reuse nor
// depend on it.
func TestWriteIgnoresStaleFixedTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	stale := path + ".tmp"
	if err := os.WriteFile(stale, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stale, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("fresh"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileMode(t, path); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	got, err := os.ReadFile(stale)
	if err != nil {
		t.Fatalf("stale temp file was touched: %v", err)
	}
	if string(got) != "stale" {
		t.Fatalf("stale temp file content = %q, want it untouched", got)
	}
	assertOnly(t, dir, "state.json", "state.json.tmp")
}

func TestWritePermIsExactUnderUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := t.TempDir()
	for _, perm := range []fs.FileMode{0o600, 0o640, 0o644, 0o666} {
		path := filepath.Join(dir, "file")
		if err := WriteFile(path, []byte("x"), perm); err != nil {
			t.Fatalf("WriteFile(%o): %v", perm, err)
		}
		if got := fileMode(t, path); got != perm {
			t.Fatalf("mode = %o, want exactly %o", got, perm)
		}
	}
}

func TestWriteOverwriteTightensExistingMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileMode(t, path); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}
