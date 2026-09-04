package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubLaunch(*Root, string, bool) error { return nil }

func executeSelf(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd("v1.0.0", "abc123", "2026-01-01T00:00:00Z", stubLaunch)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestSelfCommandsRegistered(t *testing.T) {
	cmd := newRootCmd("dev", "none", "unknown", stubLaunch)
	self, _, err := cmd.Find([]string{"self"})
	if err != nil || self.Name() != "self" {
		t.Fatalf("self command not registered: %v", err)
	}
	for _, sub := range []string{"check", "update", "install", "path"} {
		c, _, err := cmd.Find([]string{"self", sub})
		if err != nil || c.Name() != sub {
			t.Errorf("self %s not registered: %v", sub, err)
		}
	}
}

func TestSelfPathOfflineAndConfigIndependent(t *testing.T) {
	// A malformed config must not stop `self path`.
	badCfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(badCfg, []byte("this: : : not yaml\n\t- broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := executeSelf(t, "--config", badCfg, "self", "path")
	if err != nil {
		t.Fatalf("self path failed with broken config: %v\n%s", err, out)
	}
	for _, want := range []string{"Executable:", "Resolved:", "Version:", "Scope:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "v1.0.0") {
		t.Errorf("version not shown:\n%s", out)
	}
}

func TestSelfInstallDryRun(t *testing.T) {
	dest := t.TempDir()
	out, err := executeSelf(t, "self", "install", "--dest", dest, "--dry-run", "--yes")
	if err != nil {
		t.Fatalf("dry-run failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No files changed") {
		t.Errorf("missing dry-run notice:\n%s", out)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Errorf("dry-run created files: %v", entries)
	}
}

func TestSelfInstallToDest(t *testing.T) {
	dest := t.TempDir()
	out, err := executeSelf(t, "self", "install", "--dest", dest, "--yes")
	if err != nil {
		t.Fatalf("install --dest failed: %v\n%s", err, out)
	}
	bin := filepath.Join(dest, "bin", "llmtui")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("binary not installed at %s: %v\n%s", bin, err, out)
	}
	if _, err := os.Stat(filepath.Join(dest, "lib", "llmtui", "install.json")); err != nil {
		t.Errorf("install manifest not written: %v", err)
	}
}

func TestSelfInstallRejectsConflictingScopes(t *testing.T) {
	_, err := executeSelf(t, "self", "install", "--system", "--user")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("got %v, want mutual-exclusion error", err)
	}
}
