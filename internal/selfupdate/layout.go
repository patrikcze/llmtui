package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Scope names how an llmtui installation is managed.
type Scope string

const (
	ScopeUser   Scope = "user"
	ScopeSystem Scope = "system"
	ScopeCustom Scope = "custom/unmanaged"
	ScopeBundle Scope = "unpacked release bundle"
	ScopeDev    Scope = "development/source build"
)

// Target is a fully resolved installation location. Every path is derived
// from an llmtui-controlled prefix; none come from release content.
type Target struct {
	Scope        Scope
	Prefix       string // installation root
	BinPath      string // absolute path to the installed llmtui[.exe]
	RuntimeDir   string // absolute path to the managed lib/llmtui/runtime
	DocDir       string // absolute path for LICENSE / notices
	ManifestPath string // absolute path to the install manifest
}

// TargetForScope resolves the standard Target for a scope. A non-empty
// destOverride replaces the standard prefix and forces ScopeCustom (used for
// testing and unusual layouts).
func TargetForScope(scope Scope, destOverride string) (Target, error) {
	if destOverride != "" {
		abs, err := filepath.Abs(destOverride)
		if err != nil {
			return Target{}, fmt.Errorf("resolve --dest: %w", err)
		}
		return layoutForPrefix(abs, ScopeCustom), nil
	}
	var prefix string
	var err error
	switch scope {
	case ScopeSystem:
		prefix, err = systemPrefix()
	case ScopeUser:
		prefix, err = userPrefix()
	default:
		return Target{}, fmt.Errorf("scope %q has no standard prefix", scope)
	}
	if err != nil {
		return Target{}, err
	}
	return layoutForPrefix(prefix, scope), nil
}

// bundleLayoutForRoot returns the Target for an unpacked release bundle,
// whose internal layout (llmtui and lib/llmtui/runtime directly under the
// root) differs from a managed prefix's bin/ + lib/ split. The runtime
// resolver finds it via <exe>/lib/llmtui/runtime.
func bundleLayoutForRoot(root string, scope Scope) Target {
	libDir := filepath.Join(root, "lib", "llmtui")
	return Target{
		Scope:        scope,
		Prefix:       root,
		BinPath:      filepath.Join(root, binaryName(runtime.GOOS)),
		RuntimeDir:   filepath.Join(libDir, "runtime"),
		DocDir:       root,
		ManifestPath: filepath.Join(libDir, "install.json"),
	}
}

// TargetForRunningExe resolves where `self update` should install, based on
// how the running executable is managed. destOverride wins outright. A
// running binary that is not a managed or bundle installation is refused
// unless allowUnmanaged (i.e. --force) is set, in which case its own
// directory is treated as a bundle root.
func TargetForRunningExe(resolvedExe, version, destOverride string, allowUnmanaged bool) (Target, error) {
	if destOverride != "" {
		return TargetForScope(ScopeCustom, destOverride)
	}
	switch DetectScope(resolvedExe, version) {
	case ScopeSystem:
		return TargetForScope(ScopeSystem, "")
	case ScopeUser:
		return TargetForScope(ScopeUser, "")
	case ScopeBundle:
		return bundleLayoutForRoot(filepath.Dir(resolvedExe), ScopeBundle), nil
	case ScopeDev:
		return Target{}, fmt.Errorf("llmtui is running from a development build (%s); build and install a release instead, or pass --dest", version)
	default:
		if allowUnmanaged {
			return bundleLayoutForRoot(filepath.Dir(resolvedExe), ScopeCustom), nil
		}
		return Target{}, fmt.Errorf(
			"llmtui is running from %s, which is not a managed installation\n\n"+
				"Install it into a managed location first:\n  llmtui self install\n\n"+
				"or update in place explicitly:\n  llmtui self update --dest %s",
			resolvedExe, filepath.Dir(resolvedExe))
	}
}

// DetectScope classifies the installation the resolved executable path
// belongs to. It never guesses: anything it cannot place confidently is
// ScopeCustom (or ScopeDev / ScopeBundle when those are unambiguous).
func DetectScope(resolvedExe, version string) Scope {
	if sys, err := systemPrefix(); err == nil {
		if pathsEqual(layoutForPrefix(sys, ScopeSystem).BinPath, resolvedExe) {
			return ScopeSystem
		}
	}
	if usr, err := userPrefix(); err == nil {
		if pathsEqual(layoutForPrefix(usr, ScopeUser).BinPath, resolvedExe) {
			return ScopeUser
		}
	}
	if looksLikeBundle(resolvedExe) {
		return ScopeBundle
	}
	if IsDevBuild(version) {
		return ScopeDev
	}
	return ScopeCustom
}

// looksLikeBundle reports whether the executable sits inside an unpacked
// release archive: a sibling lib/llmtui/runtime plus a sibling LICENSE.
func looksLikeBundle(exe string) bool {
	dir := filepath.Dir(exe)
	if fi, err := os.Stat(filepath.Join(dir, "lib", "llmtui", "runtime")); err != nil || !fi.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "LICENSE")); err != nil {
		return false
	}
	return true
}

func pathsEqual(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
