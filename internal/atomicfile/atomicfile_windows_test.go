//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// MoveFileEx must replace an existing target, which plain MoveFile refuses.
func TestReplaceFileOverwritesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "from")
	to := filepath.Join(dir, "to")
	if err := os.WriteFile(from, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(from, to); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(to)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
	if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists after replace: %v", err)
	}
}

func TestReplaceFileFailureNamesBothPaths(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "missing")
	to := filepath.Join(dir, "to")
	err := replaceFile(from, to)
	if !errors.Is(err, ErrReplace) {
		t.Fatalf("error = %v, want ErrReplace", err)
	}
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) || linkErr.Old != from || linkErr.New != to {
		t.Fatalf("error = %v, want an *os.LinkError naming both paths", err)
	}
}
