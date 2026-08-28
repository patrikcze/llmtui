//go:build unix

package tools

import (
	"context"
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

	_, err := NewRunner(root, 1).readFile("input.fifo", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want non-regular-file rejection", err)
	}
}

func TestWorkspaceRootPreventsSymlinkSwapEscape(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(inside, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "swap.txt")
	outsideTarget, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.txt", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			_ = os.Remove(link)
			_ = os.Symlink(outsideTarget, link)
			_ = os.Remove(link)
			_ = os.Symlink("inside.txt", link)
		}
	}()
	runner := NewRunner(root, 1)
	for range 200 {
		if output, err := runner.readFile("swap.txt", 0, 0); err == nil && strings.Contains(output, "outside") {
			cancel()
			<-done
			t.Fatal("descriptor-relative read escaped through a symlink swap")
		}
		_, _, _ = runner.writeFile("swap.txt", "updated")
	}
	cancel()
	<-done
	data, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "outside" {
		t.Fatalf("outside file modified through symlink swap: %q", data)
	}
}
