package selfupdate

import (
	"os"
	"path/filepath"
)

// PathInfo is the offline diagnostic reported by `self path`.
type PathInfo struct {
	Executable string // os.Executable() result
	Resolved   string // symlinks evaluated
	Version    string
	Scope      Scope
	Manifest   *InstallManifest // nil when no install manifest was found
}

// InspectPath gathers `self path` information. It performs no network access.
func InspectPath(build BuildInfo) (PathInfo, error) {
	info := PathInfo{Version: build.Version}

	exe, err := os.Executable()
	if err != nil {
		return info, err
	}
	info.Executable = exe

	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	info.Resolved = resolved

	info.Scope = DetectScope(resolved, build.Version)

	// A manifest, when present next to the resolved binary's managed prefix,
	// makes the scope authoritative rather than heuristic.
	for _, mp := range candidateManifestPaths(resolved) {
		if m, err := ReadManifest(mp); err == nil {
			info.Manifest = m
			if s := Scope(m.Scope); s == ScopeUser || s == ScopeSystem || s == ScopeCustom {
				info.Scope = s
			}
			break
		}
	}
	return info, nil
}

// candidateManifestPaths lists where an install manifest could sit relative
// to the resolved executable, covering both the managed prefix layout
// (bin/ + lib/) and the bundle layout (flat).
func candidateManifestPaths(resolvedExe string) []string {
	dir := filepath.Dir(resolvedExe)
	return []string{
		filepath.Join(dir, "lib", "llmtui", "install.json"),       // bundle / windows managed
		filepath.Join(dir, "..", "lib", "llmtui", "install.json"), // unix managed (bin/ -> ../lib)
	}
}
