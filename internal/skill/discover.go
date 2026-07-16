package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Paths carries the deterministic discovery locations. Any of them may be
// empty or missing on disk; a missing directory is never an error.
type Paths struct {
	// UserDir is the per-user skills directory, e.g.
	// <UserConfigDir>/llmtui/skills.
	UserDir string
	// WorkspaceDir is <workspace>/.llmtui/skills.
	WorkspaceDir string
	// Extra are additional user-configured search paths (skills.paths).
	Extra []string
	// UserPluginDir and WorkspacePluginDir hold plugin packages.
	UserPluginDir      string
	WorkspacePluginDir string
	// ExtraPluginDirs are additional plugin search paths (plugins.paths).
	ExtraPluginDirs []string
}

// DefaultPaths derives the standard locations from the platform config dir
// and the workspace root. Errors resolving the user config dir degrade to
// empty entries rather than failing discovery.
func DefaultPaths(workspaceRoot string) Paths {
	var p Paths
	if dir, err := os.UserConfigDir(); err == nil {
		p.UserDir = filepath.Join(dir, "llmtui", "skills")
		p.UserPluginDir = filepath.Join(dir, "llmtui", "plugins")
	}
	if workspaceRoot != "" {
		p.WorkspaceDir = filepath.Join(workspaceRoot, ".llmtui", "skills")
		p.WorkspacePluginDir = filepath.Join(workspaceRoot, ".llmtui", "plugins")
	}
	return p
}

// discoverDir loads every <dir>/<skill-dir>/SKILL.md. Directory names are
// only a browsing convenience — identity comes from the validated front
// matter, and a mismatch between directory name and metadata ID is reported
// as a warning so spoofed names are visible.
func discoverDir(dir string, source Source, maxBytes int) (skills []Skill, warns []Warning) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // missing directory: nothing to discover
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name, SkillFileName)
		raw, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			warns = append(warns, Warning{Path: path, Message: err.Error()})
			continue
		}
		s, err := Parse(raw, maxBytes)
		if err != nil {
			warns = append(warns, Warning{Path: path, Message: err.Error()})
			continue
		}
		if s.Meta.ID != name {
			warns = append(warns, Warning{Path: path,
				Message: fmt.Sprintf("directory %q does not match skill id %q (the id from the file is used)", name, s.Meta.ID)})
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
		s.Source = source
		s.Path = path
		skills = append(skills, s)
	}
	return skills, warns
}

// discoverPlugins loads every <dir>/<plugin-dir>/plugin.yaml. Invalid
// manifests produce a Plugin with Err set so the UI can explain the problem.
func discoverPlugins(dir string, source Source) (plugins []Plugin, warns []Warning) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		root := filepath.Join(dir, name)
		manifestPath := filepath.Join(root, PluginManifestName)
		raw, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			warns = append(warns, Warning{Path: manifestPath, Message: err.Error()})
			continue
		}
		if abs, err := filepath.Abs(root); err == nil {
			root = abs
		}
		p := Plugin{Root: root, Source: source}
		m, err := ParseManifest(raw)
		p.Manifest = m
		if err != nil {
			p.Err = err
		} else if m.ID != name {
			warns = append(warns, Warning{Path: manifestPath,
				Message: fmt.Sprintf("directory %q does not match plugin id %q (the id from the manifest is used)", name, m.ID)})
		}
		plugins = append(plugins, p)
	}
	return plugins, warns
}

// expandExtra normalizes a configured extra path list: blanks dropped,
// relative paths made absolute so provenance is unambiguous.
func expandExtra(paths []string) []string {
	var out []string
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		out = append(out, p)
	}
	return out
}
