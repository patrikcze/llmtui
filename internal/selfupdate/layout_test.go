package selfupdate

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestTargetForScopeDestOverride(t *testing.T) {
	dir := t.TempDir()
	target, err := TargetForScope(ScopeSystem, dir)
	if err != nil {
		t.Fatal(err)
	}
	if target.Scope != ScopeCustom {
		t.Errorf("scope = %q, want custom", target.Scope)
	}
	if !filepath.IsAbs(target.BinPath) {
		t.Errorf("BinPath not absolute: %s", target.BinPath)
	}
	if filepath.Dir(filepath.Dir(target.BinPath)) != filepath.Clean(dir) && filepath.Dir(target.BinPath) != filepath.Clean(dir) {
		t.Errorf("BinPath %s not under dest %s", target.BinPath, dir)
	}
}

func TestTargetForScopeStandard(t *testing.T) {
	for _, scope := range []Scope{ScopeUser, ScopeSystem} {
		target, err := TargetForScope(scope, "")
		if err != nil {
			t.Fatalf("%s: %v", scope, err)
		}
		if target.Scope != scope {
			t.Errorf("scope = %q, want %q", target.Scope, scope)
		}
		if target.RuntimeDir == "" || target.BinPath == "" || target.ManifestPath == "" {
			t.Errorf("%s: incomplete target %+v", scope, target)
		}
		// The runtime dir must be reachable from the binary via the
		// resolver's <exe>/lib or <exe>/../lib candidates.
		binDir := filepath.Dir(target.BinPath)
		c1 := filepath.Join(binDir, "lib", "llmtui", "runtime")
		c2 := filepath.Join(binDir, "..", "lib", "llmtui", "runtime")
		if filepath.Clean(target.RuntimeDir) != filepath.Clean(c1) && filepath.Clean(target.RuntimeDir) != filepath.Clean(c2) {
			t.Errorf("%s: runtime %s not resolver-reachable from %s", scope, target.RuntimeDir, binDir)
		}
	}
}

func TestDetectScope(t *testing.T) {
	sys, err := systemPrefix()
	if err != nil {
		t.Skip("no system prefix on this platform")
	}
	sysBin := layoutForPrefix(sys, ScopeSystem).BinPath
	if got := DetectScope(sysBin, "v1.0.0"); got != ScopeSystem {
		t.Errorf("system bin detected as %q", got)
	}

	usr, _ := userPrefix()
	usrBin := layoutForPrefix(usr, ScopeUser).BinPath
	if got := DetectScope(usrBin, "v1.0.0"); got != ScopeUser {
		t.Errorf("user bin detected as %q", got)
	}

	if got := DetectScope("/opt/random/llmtui", "dev"); got != ScopeDev {
		t.Errorf("dev build detected as %q", got)
	}
	if got := DetectScope("/opt/random/llmtui", "v1.0.0"); got != ScopeCustom {
		t.Errorf("random path detected as %q, want custom", got)
	}
}

func TestBundleLayout(t *testing.T) {
	root := t.TempDir()
	target := bundleLayoutForRoot(root, ScopeBundle)
	want := filepath.Join(root, binaryName(runtime.GOOS))
	if target.BinPath != want {
		t.Errorf("BinPath = %s, want %s", target.BinPath, want)
	}
	if target.RuntimeDir != filepath.Join(root, "lib", "llmtui", "runtime") {
		t.Errorf("RuntimeDir = %s", target.RuntimeDir)
	}
}
