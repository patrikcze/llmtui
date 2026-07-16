package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// PluginManifestName is the canonical plugin manifest file name.
const PluginManifestName = "plugin.yaml"

// maxManifestBytes caps a plugin manifest; manifests are small by design.
const maxManifestBytes = 16 * 1024

// Manifest is the parsed plugin.yaml. Schema v1 contributes skills only;
// templates and MCP server references are documented future extension points
// and unknown manifest fields are rejected so a v1 build never silently
// ignores a contribution it does not understand.
type Manifest struct {
	SchemaVersion int                `yaml:"schema_version"`
	ID            string             `yaml:"id"`
	Name          string             `yaml:"name"`
	Version       string             `yaml:"version"`
	Description   string             `yaml:"description"`
	Skills        []ManifestSkillRef `yaml:"skills"`
}

// ManifestSkillRef points at one SKILL.md inside the plugin directory.
type ManifestSkillRef struct {
	Path string `yaml:"path"`
}

// Plugin is one discovered plugin: its manifest plus provenance and state.
type Plugin struct {
	Manifest Manifest
	// Root is the canonical absolute plugin directory.
	Root string
	// Source is where the plugin directory was found (user | workspace | extra).
	Source Source
	// Enabled reports whether the user enabled this plugin (config list or
	// /plugins enable). Disabled plugins stay visible but contribute nothing.
	Enabled bool
	// Err records a manifest validation failure; a broken plugin stays
	// listed so the user can see why it is unusable.
	Err error
}

// ParseManifest parses and validates one plugin.yaml document.
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) == 0 {
		return m, fmt.Errorf("plugin manifest is empty")
	}
	if len(raw) > maxManifestBytes {
		return m, fmt.Errorf("plugin manifest is %d bytes, over the %d byte limit", len(raw), maxManifestBytes)
	}
	if !utf8.Valid(raw) {
		return m, fmt.Errorf("plugin manifest is not valid UTF-8")
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("parse plugin manifest: %w", err)
	}
	if m.SchemaVersion != SchemaVersion {
		return m, fmt.Errorf("unsupported schema_version %d (this build understands %d)", m.SchemaVersion, SchemaVersion)
	}
	if err := ValidateID(m.ID); err != nil {
		return m, err
	}
	if strings.TrimSpace(m.Name) == "" {
		return m, fmt.Errorf("plugin %q needs a name", m.ID)
	}
	if strings.TrimSpace(m.Version) == "" {
		return m, fmt.Errorf("plugin %q needs a version", m.ID)
	}
	if strings.TrimSpace(m.Description) == "" {
		return m, fmt.Errorf("plugin %q needs a description", m.ID)
	}
	for _, ref := range m.Skills {
		if strings.TrimSpace(ref.Path) == "" {
			return m, fmt.Errorf("plugin %q declares a skill with an empty path", m.ID)
		}
	}
	return m, nil
}

// resolveInsideRoot resolves rel against root and guarantees the resolved,
// symlink-evaluated path stays inside root. This is the containment check for
// manifest-declared paths: a plugin must never reach outside its own
// directory, whether via "..", an absolute path, or a symlink.
func resolveInsideRoot(root, rel string) (string, error) {
	rel = filepath.Clean(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(rel) || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("path %q escapes the plugin directory", rel)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve plugin root: %w", err)
	}
	abs := filepath.Join(rootResolved, rel)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the plugin directory", rel)
	}
	return resolved, nil
}

// loadPluginSkills parses the skills a plugin manifest declares. Individual
// bad skills become warnings; they never take the whole plugin down.
func loadPluginSkills(p Plugin, maxBytes int) (skills []Skill, warns []Warning) {
	for _, ref := range p.Manifest.Skills {
		path, err := resolveInsideRoot(p.Root, ref.Path)
		if err != nil {
			warns = append(warns, Warning{Path: filepath.Join(p.Root, ref.Path), Message: err.Error()})
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			warns = append(warns, Warning{Path: path, Message: err.Error()})
			continue
		}
		s, err := Parse(raw, maxBytes)
		if err != nil {
			warns = append(warns, Warning{Path: path, Message: err.Error()})
			continue
		}
		s.Source = SourcePlugin
		s.Path = path
		s.PluginID = p.Manifest.ID
		skills = append(skills, s)
	}
	return skills, warns
}
