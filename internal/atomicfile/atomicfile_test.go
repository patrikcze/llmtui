package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// assertOnly fails unless dir holds exactly the named entries, so a leaked
// temp file shows up by name.
func assertOnly(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	for _, entry := range entries {
		if !want[entry.Name()] {
			t.Errorf("unexpected entry %q left in %s", entry.Name(), dir)
		}
		delete(want, entry.Name())
	}
	for name := range want {
		t.Errorf("missing entry %q in %s", name, dir)
	}
}

func TestWriteFileCreatesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for _, content := range []string{"first", "second, longer than the first", "3"} {
		if err := WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q): %v", content, err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != content {
			t.Fatalf("content = %q, want %q", got, content)
		}
	}
	assertOnly(t, dir, "state.json")
}

func TestWriteStreamsFill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.bin")
	err := Write(path, 0o600, func(w io.Writer) error {
		for _, part := range []string{"a", "b", "c"} {
			if _, err := io.WriteString(w, part); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc" {
		t.Fatalf("content = %q, want %q", got, "abc")
	}
}

func TestWriteFillErrorLeavesTargetAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("encode failed")
	err := Write(path, 0o600, func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Write error = %v, want the fill error", err)
	}
	if errors.Is(err, ErrReplace) {
		t.Fatalf("fill error must not be reported as ErrReplace: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Fatalf("target = %q after failed write, want it unchanged", got)
	}
	assertOnly(t, dir, "state.json")
}

func TestWriteReplaceFailureIsErrReplaceAndRemovesTemp(t *testing.T) {
	dir := t.TempDir()
	// A non-empty directory cannot be replaced by a file on any platform.
	path := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(path, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := WriteFile(path, []byte("data"), 0o600)
	if !errors.Is(err, ErrReplace) {
		t.Fatalf("WriteFile error = %v, want ErrReplace", err)
	}
	assertOnly(t, dir, "target")
}

func TestWriteMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.json")
	err := WriteFile(path, []byte("data"), 0o600)
	if err == nil {
		t.Fatal("WriteFile into a missing directory succeeded")
	}
	if errors.Is(err, ErrReplace) {
		t.Fatalf("create failure must not be reported as ErrReplace: %v", err)
	}
}
