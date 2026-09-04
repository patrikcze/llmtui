package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stagePayload creates a staging directory with a valid fake binary and,
// optionally, a runtime tree, returning a Payload ready for Install.
func stagePayload(t *testing.T, withRuntime bool) *Payload {
	t.Helper()
	stage := t.TempDir()
	bin := filepath.Join(stage, binaryName(runtime.GOOS))
	if err := os.WriteFile(bin, fakeBinary(runtime.GOOS), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Payload{Dir: stage, BinPath: bin}
	if withRuntime {
		rt := filepath.Join(stage, "lib", "llmtui", "runtime")
		if err := os.MkdirAll(rt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rt, "libllama.so"), []byte("NEW-RUNTIME"), 0o755); err != nil {
			t.Fatal(err)
		}
		p.RuntimeDir = rt
	}
	return p
}

func TestInstallFresh(t *testing.T) {
	prefix := t.TempDir()
	target, _ := TargetForScope(ScopeSystem, prefix)
	p := stagePayload(t, true)

	if err := p.Install(target, InstallMeta{Version: "v1.0.24", By: "self update"}, true); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if fi, err := os.Stat(target.BinPath); err != nil {
		t.Fatalf("binary not installed: %v", err)
	} else if runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("binary not executable: %v", fi.Mode())
	}
	if got, _ := os.ReadFile(filepath.Join(target.RuntimeDir, "libllama.so")); string(got) != "NEW-RUNTIME" {
		t.Errorf("runtime content = %q", got)
	}
	m, err := ReadManifest(target.ManifestPath)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.Version != "v1.0.24" || m.InstalledBy != "self update" {
		t.Errorf("manifest = %+v", m)
	}
}

func TestInstallUpgradeReplacesAndCleansBackups(t *testing.T) {
	prefix := t.TempDir()
	target, _ := TargetForScope(ScopeSystem, prefix)

	// Pre-existing installation.
	if err := os.MkdirAll(filepath.Dir(target.BinPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target.BinPath, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target.RuntimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target.RuntimeDir, "libllama.so"), []byte("OLD-RUNTIME"), 0o755); err != nil {
		t.Fatal(err)
	}

	p := stagePayload(t, true)
	if err := p.Install(target, InstallMeta{Version: "v2.0.0", By: "self update"}, true); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if got, _ := os.ReadFile(target.BinPath); string(got) == "OLD-BINARY" {
		t.Error("binary not replaced")
	}
	if got, _ := os.ReadFile(filepath.Join(target.RuntimeDir, "libllama.so")); string(got) != "NEW-RUNTIME" {
		t.Errorf("runtime not replaced: %q", got)
	}
	// No backup leftovers.
	for _, dir := range []string{filepath.Dir(target.BinPath), filepath.Dir(target.RuntimeDir)} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.Contains(e.Name(), ".llmtui-bak-") {
				t.Errorf("backup left behind: %s", e.Name())
			}
		}
	}
}

func TestInstallRefusesSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is privileged on Windows CI")
	}
	prefix := t.TempDir()
	target, _ := TargetForScope(ScopeSystem, prefix)
	if err := os.MkdirAll(filepath.Dir(target.BinPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", target.BinPath); err != nil {
		t.Fatal(err)
	}
	p := stagePayload(t, false)
	err := p.Install(target, InstallMeta{Version: "v1.0.0", By: "self install"}, false)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("got %v, want a symlink refusal", err)
	}
	// The symlink is still there, untouched.
	if fi, _ := os.Lstat(target.BinPath); fi == nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink target was modified")
	}
}

func TestTransactionRollback(t *testing.T) {
	root := t.TempDir()
	liveRuntime := filepath.Join(root, "runtime")
	if err := os.MkdirAll(liveRuntime, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveRuntime, "lib"), []byte("ORIGINAL-RUNTIME"), 0o644); err != nil {
		t.Fatal(err)
	}
	liveBin := filepath.Join(root, "llmtui")
	if err := os.WriteFile(liveBin, []byte("ORIGINAL-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}

	stagedRuntime := t.TempDir()
	if err := os.WriteFile(filepath.Join(stagedRuntime, "lib"), []byte("STAGED-RUNTIME"), 0o644); err != nil {
		t.Fatal(err)
	}

	tx := &transaction{}
	if err := tx.swapPath(stagedRuntime, liveRuntime, liveRuntime+".bak"); err != nil {
		t.Fatalf("runtime swap: %v", err)
	}
	// Second step fails: staged source does not exist.
	err := tx.swapPath(filepath.Join(root, "does-not-exist"), liveBin, liveBin+".bak")
	if err == nil {
		t.Fatal("expected swap failure")
	}
	tx.rollback()

	if got, _ := os.ReadFile(filepath.Join(liveRuntime, "lib")); string(got) != "ORIGINAL-RUNTIME" {
		t.Errorf("runtime not rolled back: %q", got)
	}
	if got, _ := os.ReadFile(liveBin); string(got) != "ORIGINAL-BINARY" {
		t.Errorf("binary changed despite rollback: %q", got)
	}
	for _, e := range mustReadDir(t, root) {
		if strings.HasSuffix(e, ".bak") {
			t.Errorf("backup left after rollback: %s", e)
		}
	}
}

func TestCleanStaleBackups(t *testing.T) {
	prefix := t.TempDir()
	target, _ := TargetForScope(ScopeSystem, prefix)
	binDir := filepath.Dir(target.BinPath)
	libDir := filepath.Dir(target.RuntimeDir)
	for _, d := range []string{binDir, libDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(binDir, "llmtui")
	_ = os.WriteFile(keep, []byte("keep"), 0o755)
	_ = os.WriteFile(filepath.Join(binDir, "llmtui.llmtui-bak-1-2"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(binDir, "llmtui.llmtui-bak-1-2.old"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(libDir, ".llmtui-stage-abc"), 0o755)

	CleanStaleBackups(target)

	if _, err := os.Stat(keep); err != nil {
		t.Errorf("real binary removed: %v", err)
	}
	for _, p := range []string{
		filepath.Join(binDir, "llmtui.llmtui-bak-1-2"),
		filepath.Join(binDir, "llmtui.llmtui-bak-1-2.old"),
		filepath.Join(libDir, ".llmtui-stage-abc"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale artifact not removed: %s", p)
		}
	}
}

func TestEnsureWritablePrefixPermissionHint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks do not apply")
	}
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based read-only dir is not reliable on Windows")
	}
	prefix := t.TempDir()
	if err := os.Chmod(prefix, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(prefix, 0o700) })

	target, _ := TargetForScope(ScopeSystem, filepath.Join(prefix, "root"))
	target.Scope = ScopeSystem
	err := ensureWritablePrefix(target)
	if err == nil || !strings.Contains(err.Error(), "elevated shell") {
		t.Fatalf("got %v, want an elevation hint", err)
	}
}

func mustReadDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
