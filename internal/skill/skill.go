// Package skill implements the provider-neutral Skills and Plugins
// subsystem: declarative Markdown instruction packages (SKILL.md files with
// YAML front matter) discovered from local directories, validated, and
// activated per run or per session so the prompt composer can include them.
//
// Skills are instructions, not code. Discovering, registering, or activating
// a skill never executes anything, never grants tool permissions, never
// starts an MCP server, and never changes provider or model settings. Plugins
// are declarative local packages that contribute skills once explicitly
// enabled; enabling a plugin registers its skills but activates none of them.
package skill

import (
	"fmt"
	"regexp"
	"strings"
)

// Source identifies where a skill (or plugin) was discovered.
type Source string

const (
	SourceBuiltin   Source = "builtin"
	SourceUser      Source = "user"
	SourceWorkspace Source = "workspace"
	SourcePlugin    Source = "plugin"
	// SourceExtra marks skills found under a user-configured extra search
	// path (skills.paths).
	SourceExtra Source = "extra"
)

// SkillFileName is the canonical file name of a skill definition.
const SkillFileName = "SKILL.md"

// SchemaVersion is the only skill/plugin schema this build understands.
const SchemaVersion = 1

// idPattern validates skill and plugin identifiers: stable, lowercase,
// safe for command lines, logs, and path-independent lookup.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// maxIDLen bounds identifiers so they stay usable in tables and logs.
const maxIDLen = 64

// ValidateID reports whether id is an acceptable skill or plugin identifier.
func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("id is required")
	}
	if len(id) > maxIDLen {
		return fmt.Errorf("id %q is longer than %d characters", id, maxIDLen)
	}
	if !idPattern.MatchString(id) {
		return fmt.Errorf("id %q is invalid (want %s)", id, idPattern.String())
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("id %q is invalid (consecutive dots)", id)
	}
	return nil
}

// Meta is the YAML front matter of a SKILL.md file. Only SchemaVersion, ID,
// Name, and Description are required; everything else is optional guidance.
type Meta struct {
	SchemaVersion    int      `yaml:"schema_version"`
	ID               string   `yaml:"id"`
	Name             string   `yaml:"name"`
	Description      string   `yaml:"description"`
	Version          string   `yaml:"version"`
	Tags             []string `yaml:"tags"`
	Triggers         []string `yaml:"triggers"`
	RecommendedTools []string `yaml:"recommended_tools"`
	// Capabilities declares requirements such as tool_calling: optional |
	// required. Purely informational: activation warns, it never grants.
	Capabilities map[string]string `yaml:"capabilities"`
}

// Skill is one parsed and validated skill definition.
type Skill struct {
	Meta Meta
	// Body is the Markdown instruction text below the front matter.
	Body string
	// Hash is the hex sha256 of the complete raw file content, so identical
	// content always produces the identical fingerprint (cache keys, session
	// restore checks, reload diffing).
	Hash string
	// Size is the raw file size in bytes.
	Size int
	// Source and Path are provenance: where the skill was found. PluginID is
	// set when Source == SourcePlugin.
	Source   Source
	Path     string
	PluginID string
}

// QualifiedID returns the unambiguous identifier for this skill:
// "user:go-review", "workspace:go-review", "plugin:jira-tools/worklog".
func (s Skill) QualifiedID() string {
	if s.Source == SourcePlugin && s.PluginID != "" {
		return string(SourcePlugin) + ":" + s.PluginID + "/" + s.Meta.ID
	}
	return string(s.Source) + ":" + s.Meta.ID
}

// Warning is a non-fatal problem found during discovery (duplicate IDs,
// unparsable files, oversized skills). Kept so the UI can show what was
// skipped and why instead of failing silently.
type Warning struct {
	Path    string
	Message string
}

func (w Warning) String() string {
	if w.Path == "" {
		return w.Message
	}
	return w.Path + ": " + w.Message
}
