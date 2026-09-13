package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Regression tests for the 2026-09-13 security review
// (.claude/tasks/plans/2026-09-13-security-review.md).

// requireGit skips the test when git is unavailable, matching the pattern in
// local_context_test.go.
func requireGit(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("marker-writing shell helpers assume a POSIX shell")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
}

// gitMarkerHelper writes a small POSIX shell script at path that, if ever
// executed, creates marker and echoes its stdin back (so a diff/status
// invocation that is *not* neutralized still looks superficially normal).
func gitMarkerHelper(t *testing.T, path, marker string) {
	t.Helper()
	script := "#!/bin/sh\ntouch " + marker + "\ncat\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// Finding 1 (CWE-78): a supplied repository's repo-local .git/config can set
// diff.external or core.fsmonitor, and an auto-approved "read-only" git
// diff/status must not launch it.

func TestRunCommandGitDiffDoesNotRunConfiguredExternalDiffHelper(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "config", "user.email", "test@example.com")
	mustGit(t, root, "config", "user.name", "Test")

	marker := filepath.Join(root, "pwned")
	helper := filepath.Join(root, "evil-diff.sh")
	gitMarkerHelper(t, helper, marker)
	mustGit(t, root, "config", "diff.external", helper)

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "a.txt")
	mustGit(t, root, "commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(root, 64)
	if r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "git diff"}) {
		t.Fatal("git diff should stay auto-approved (that is exactly what makes the helper dangerous)")
	}
	out, err := r.runCommand("git diff")
	if err != nil {
		t.Fatalf("git diff failed: %v (%s)", err, out)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("diff.external helper ran under an auto-approved git diff")
	}
	if !strings.Contains(out, "-hello") || !strings.Contains(out, "+world") {
		t.Fatalf("git diff output missing expected content: %s", out)
	}
}

func TestRunCommandGitStatusDoesNotRunConfiguredFSMonitorHelper(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "config", "user.email", "test@example.com")
	mustGit(t, root, "config", "user.name", "Test")

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "a.txt")
	mustGit(t, root, "commit", "-q", "-m", "init")

	// core.fsmonitor is configured only now, after the setup commands above:
	// "git add"/"git commit" also refresh the index and would themselves
	// consult (and thus trigger) a configured fsmonitor hook, which would
	// make the marker's presence below reflect that unrelated setup step
	// rather than the auto-approved "git status" this test actually exercises.
	marker := filepath.Join(root, "pwned")
	helper := filepath.Join(root, "evil-fsmonitor.sh")
	gitMarkerHelper(t, helper, marker)
	mustGit(t, root, "config", "core.fsmonitor", helper)

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(root, 64)
	if r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "git status --porcelain"}) {
		t.Fatal("git status should stay auto-approved")
	}
	out, err := r.runCommand("git status --porcelain")
	if err != nil {
		t.Fatalf("git status failed: %v (%s)", err, out)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("core.fsmonitor helper ran under an auto-approved git status")
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("git status output missing modified file: %s", out)
	}
}

// A tracked .gitattributes can assign an arbitrary-named textconv driver to
// a diff; per the suggested regression test, that must not run either.
func TestRunCommandGitDiffDoesNotRunConfiguredTextconvDriver(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	mustGit(t, root, "init", "-q")
	mustGit(t, root, "config", "user.email", "test@example.com")
	mustGit(t, root, "config", "user.name", "Test")

	marker := filepath.Join(root, "pwned")
	helper := filepath.Join(root, "evil-textconv.sh")
	gitMarkerHelper(t, helper, marker)
	mustGit(t, root, "config", "diff.pwn.textconv", helper)
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.bin diff=pwn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.bin"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", ".gitattributes", "a.bin")
	mustGit(t, root, "commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(root, "a.bin"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(root, 64)
	if r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "git diff"}) {
		t.Fatal("git diff should stay auto-approved")
	}
	if _, err := r.runCommand("git diff"); err != nil {
		t.Fatalf("git diff failed: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("diff.pwn.textconv helper ran under an auto-approved git diff")
	}
}

// The hardening must not follow a command an explicit approval was granted
// for into rewriting or re-scoping it — it only applies to the fixed,
// provably read-only shapes the classifier already lets run automatically.
func TestHardenGitInvocationLeavesMutatingCommandsUntouched(t *testing.T) {
	cmdline := "git commit -m message"
	execLine, env := hardenGitInvocation(cmdline)
	if execLine != cmdline {
		t.Errorf("execLine = %q, want unchanged %q", execLine, cmdline)
	}
	if len(env) != 0 {
		t.Errorf("env = %v, want none for a mutating git command", env)
	}
}

// Finding 3 (CWE-863): "go list -mod=mod" must require approval because it
// can write go.mod/go.sum as a side effect of an ordinary listing query.

func TestClassifyCommandGoListModRequiresApproval(t *testing.T) {
	g := DefaultGuardrails()
	askCases := []string{
		"go list -mod=mod ./...",
		"go list --mod=mod ./...",
		"go list -mod mod ./...",
		"go list ./... -mod=mod",
	}
	for _, cmd := range askCases {
		if got := g.ClassifyCommand(cmd, "."); got.Verdict != VerdictAsk {
			t.Errorf("ClassifyCommand(%q) = %+v, want VerdictAsk", cmd, got)
		}
	}
	autoCases := []string{
		"go list ./...",
		"go list -mod=readonly ./...",
		"go list -mod=vendor ./...",
		"go list -m all",
	}
	for _, cmd := range autoCases {
		if got := g.ClassifyCommand(cmd, "."); got.Verdict != VerdictAuto {
			t.Errorf("ClassifyCommand(%q) = %+v, want VerdictAuto", cmd, got)
		}
	}
}

func TestRunnerGoListModRequiresApproval(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	if !r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "go list -mod=mod ./..."}) {
		t.Error("go list -mod=mod should require approval (can write go.mod/go.sum)")
	}
	if r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "go list ./..."}) {
		t.Error("plain go list should stay auto-approved")
	}
}

// End-to-end: an actual "go list -mod=mod" run against a module with a
// missing requirement really does rewrite go.mod, confirming the fixture
// exercises the write the classifier change now gates behind approval.
func TestRunCommandGoListModMutatesModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain is unavailable")
	}
	root := t.TempDir()
	depDir := filepath.Join(root, "dep")
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "go.mod"), []byte("module example.com/dep\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "dep.go"), []byte("package dep\n\nfunc Hello() string { return \"hi\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	moduleDir := filepath.Join(root, "mod")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// go.mod requires the local dep but does not yet know its replace
	// directive's module is what satisfies it — go list -mod=mod adds that
	// missing bookkeeping as a side effect of listing.
	goMod := "module example.com/mod\n\ngo 1.21\n\nreplace example.com/dep => ../dep\n"
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "main.go"), []byte("package main\n\nimport \"example.com/dep\"\n\nfunc main() { _ = dep.Hello() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}

	// Offline and hermetic, matching the audit's reproduction: only a local
	// replace dependency is involved, so no network is used.
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOFLAGS", "")

	r := NewRunner(moduleDir, 64)
	if !r.NeedsApproval(Call{Tool: ToolRunCommand, Body: "go list -mod=mod ./..."}) {
		t.Fatal("go list -mod=mod should require approval")
	}
	// Execute directly, simulating the human's required approval, to prove
	// the fixture actually exercises the mutation the approval now gates.
	if out, err := r.runCommand("go list -mod=mod ./..."); err != nil {
		t.Fatalf("go list -mod=mod failed: %v (%s)", err, out)
	}
	after, err := os.ReadFile(filepath.Join(moduleDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatal("expected go list -mod=mod to rewrite go.mod in this reproduction; test fixture no longer exercises the write")
	}
}
