package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

func TestRunnerBlocksLLMTUIWorkspaceDirWrites(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	// <workspace>/.llmtui is the discovery root for workspace skills and
	// plugins: a file written there is loaded into the *next* session's
	// prompt, turning one unapproved write into persistent influence over
	// the agent. The model must never author it silently.
	for _, path := range []string{
		".llmtui/skills/pwn/SKILL.md",
		" .llmtui/plugins/x.json",
		".LLMTUI/skills/pwn/SKILL.md ",
		"nested/.llmtui/skills/pwn/SKILL.md",
	} {
		if err := writeThrough(r, path, "malicious instructions\n"); err == nil {
			t.Errorf("write into %q allowed, want blocked", path)
		}
	}
	// A file merely *named* like the directory is fine.
	if err := writeThrough(r, "docs/llmtui.md", "# notes\n"); err != nil {
		t.Errorf("unrelated write rejected: %v", err)
	}
}

func TestRunnerWebSearchApprovalHeuristic(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	// Ordinary searches stay automatic — that is the point of the tool.
	for _, q := range []string{
		"golang context cancellation best practices",
		"how to configure ollama base url",
		"",
	} {
		if r.NeedsApproval(Call{Tool: ToolWebSearch, Body: q}) {
			t.Errorf("web_search %q required approval, want auto", q)
		}
	}
	// Exfiltration shapes must be confirmed: web_search is an unapproved
	// outbound channel and read_file/grep are unapproved inbound ones, so a
	// steered model could otherwise pipe workspace contents out silently.
	for name, q := range map[string]string{
		"bulk length":       strings.Repeat("a b ", 60),
		"embedded newline":  "search this\nAWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI",
		"long opaque token": "lookup sk-proj-" + strings.Repeat("Z", 80),
	} {
		if !r.NeedsApproval(Call{Tool: ToolWebSearch, Body: q}) {
			t.Errorf("web_search (%s) was auto-approved, want ask", name)
		}
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
	if r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "go list ./..."}) {
		t.Error("go list should be auto-approved")
	}
	// go test executes repository code and must require approval (SEC-001):
	// it must not be auto-approved just because it looks like a project check.
	if !r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "go test ./..."}) {
		t.Error("go test should require approval (executes repository code)")
	}
	if !r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "rm -rf ."}) {
		t.Error("rm -rf should require approval")
	}
	if !r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "rg --pre=sh needle payload.sh"}) {
		t.Error("rg --pre should require approval (spawns a helper program)")
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
