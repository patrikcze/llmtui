//go:build unix

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestReadFileRejectsFIFOWithoutOpeningIt(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "input.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	_, err := NewRunner(root, 1).readFile("input.fifo")
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want non-regular-file rejection", err)
	}
}
