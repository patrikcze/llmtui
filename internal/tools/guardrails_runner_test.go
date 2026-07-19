package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// writeThrough runs a write_file call through the runner and returns its error.
func writeThrough(r *Runner, path, body string) error {
	_, _, err := r.writeFile(path, body)
	return err
}

func TestRunnerBlocksGitWrites(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	for _, path := range []string{".git/hooks/pre-commit", " .git/hooks/pre-commit", ".GIT/hooks/pre-commit "} {
		if err := writeThrough(r, path, "#!/bin/sh\n"); err == nil {
			t.Errorf("write into %q allowed, want blocked", path)
		}
	}
}

func TestRunnerBlocksShellStartupWrites(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	for _, p := range []string{".zshrc", ".bashrc", "config.fish", " .zshrc ", " .ZSHRC"} {
		if err := writeThrough(r, p, "evil\n"); err == nil {
			t.Errorf("write to %s allowed, want blocked", p)
		}
	}
}

func TestRunnerBlocksSecretDirWrites(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	for _, path := range []string{".ssh/authorized_keys", " .ssh/authorized_keys", ".GNUPG/private.key "} {
		if err := writeThrough(r, path, "secret\n"); err == nil {
			t.Errorf("write into %q allowed, want blocked", path)
		}
	}
}

func TestRunnerAllowsNormalWrites(t *testing.T) {
	root := t.TempDir()
	r := NewRunner(root, 64)
	if err := writeThrough(r, "src/main.go", "package main\n"); err != nil {
		t.Fatalf("normal write rejected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "src/main.go")); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

func TestRunnerWriteBlocksRespectPolicyOff(t *testing.T) {
	root := t.TempDir()
	r := NewRunner(root, 64)
	r.Guardrails.ProtectShellStartupFiles = false
	if err := writeThrough(r, ".zshrc", "# ok now\n"); err != nil {
		t.Fatalf("write to .zshrc rejected with protection off: %v", err)
	}
}

func TestRunnerNeedsApprovalForSecretRead(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	if !r.NeedsApproval(Call{Tool: ToolReadFile, Path: ".env"}) {
		t.Error("read_file .env did not require approval")
	}
	if !r.NeedsApproval(Call{Tool: ToolReadFile, Path: "secrets/id_rsa"}) {
		t.Error("read_file id_rsa did not require approval")
	}
	if r.NeedsApproval(Call{Tool: ToolReadFile, Path: "main.go"}) {
		t.Error("read_file main.go should not require approval")
	}
}

func TestRunnerSecretReadPolicyOff(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	r.Guardrails.RequireApprovalForSecretReads = false
	if r.NeedsApproval(Call{Tool: ToolReadFile, Path: ".env"}) {
		t.Error("read_file .env required approval with policy off")
	}
}

func TestRunnerCommandApprovalUsesClassifier(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	if r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "go test ./..."}) {
		t.Error("go test should be auto-approved")
	}
	if !r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "rm -rf ."}) {
		t.Error("rm -rf should require approval")
	}
}

func TestRunnerSymlinkEscapeTogglable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	// The escape check fires on existing paths (EvalSymlinks must resolve),
	// so place a real file behind the link.
	if err := os.WriteFile(filepath.Join(outside, "x.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	// Default: symlink escape is blocked.
	r := NewRunner(root, 64)
	if _, err := r.resolve("escape/x.txt"); err == nil {
		t.Error("symlink escape resolved with protection on")
	}
	// Off: escape is permitted (path still joins under root lexically).
	r.Guardrails.BlockSymlinkEscape = false
	if _, err := r.resolve("escape/x.txt"); err != nil {
		t.Errorf("resolve failed with symlink protection off: %v", err)
	}
}
