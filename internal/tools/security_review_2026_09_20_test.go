package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Regression test for a 2026-09-20 finding: local_context.workspace shelled
// out to "git status" directly, adding only GIT_OPTIONAL_LOCKS=0, instead of
// going through the same gitHardenedEnv overrides run_command's read-only
// git commands use (see TestRunCommandGitStatusDoesNotRunConfiguredFSMonitorHelper
// above). Unlike run_command, local_context.workspace is never approval-gated
// (Runner.NeedsApproval only asks for kind=clipboard), so a repository-local
// core.fsmonitor helper could run with no human in the loop at all merely
// because the model requested workspace context.

func TestLocalContextWorkspaceDoesNotRunConfiguredFSMonitorHelper(t *testing.T) {
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

	// core.fsmonitor is configured only now, after the setup commands above,
	// for the same reason TestRunCommandGitStatusDoesNotRunConfiguredFSMonitorHelper
	// waits: "git add"/"git commit" would themselves trigger a
	// configured hook and make the marker misleading.
	marker := filepath.Join(root, "pwned")
	helper := filepath.Join(root, "evil-fsmonitor.sh")
	gitMarkerHelper(t, helper, marker)
	mustGit(t, root, "config", "core.fsmonitor", helper)

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(root, 64)
	if r.NeedsApproval(Call{Tool: ToolLocalContext, ContextKind: LocalContextWorkspace}) {
		t.Fatal("local_context kind=workspace should stay auto-approved (that is exactly what makes the helper dangerous)")
	}

	collector := &defaultLocalContextCollector{root: root}
	data, err := collector.Collect(context.Background(), LocalContextWorkspace, 10)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("core.fsmonitor helper ran under an auto-approved local_context workspace call")
	}

	var result workspaceContext
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Git || !result.Dirty || result.Modified != 1 {
		t.Fatalf("workspace = %+v (%s)", result, data)
	}
}
