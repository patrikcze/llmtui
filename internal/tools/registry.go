package tools

import (
	"encoding/json"
	"fmt"
)

// SafetyClass groups capabilities by what they can affect; the TUI and the
// approval flow key their policy off it.
type SafetyClass string

const (
	SafetyReadOnly       SafetyClass = "read_only"
	SafetyWorkspaceWrite SafetyClass = "workspace_write"
	SafetyCommand        SafetyClass = "command"
	SafetyNetwork        SafetyClass = "network"
	SafetyExternalMCP    SafetyClass = "external_mcp"
)

// CapabilityInfo describes one agent capability: today the built-in and web
// tools, later MCP and RAG tools. It is metadata only — execution stays with
// the Runner (or a future MCP client).
type CapabilityInfo struct {
	Name        string
	Description string
	// Source is where the capability comes from: builtin | web | mcp | rag.
	Source string
	Safety SafetyClass
	// Approval is the static policy: "no", "ask", or a qualified form like
	// "ask unless read-only". Runtime auto-approve mode overrides it.
	Approval string
	// Parameters is the JSON Schema of the capability's arguments, when known.
	Parameters json.RawMessage
}

// Registry is the single catalog of agent capabilities. /tools reads it now;
// /mcp and /rag will register into it later so every surface (native
// function calling, fenced protocol, debug UI) lists tools from one place.
type Registry struct {
	byName map[string]CapabilityInfo
	order  []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]CapabilityInfo{}}
}

// Register adds one capability; empty and duplicate names are rejected.
func (r *Registry) Register(info CapabilityInfo) error {
	if info.Name == "" {
		return fmt.Errorf("capability needs a name")
	}
	if _, exists := r.byName[info.Name]; exists {
		return fmt.Errorf("capability %q is already registered", info.Name)
	}
	r.byName[info.Name] = info
	r.order = append(r.order, info.Name)
	return nil
}

// Get returns one capability by name.
func (r *Registry) Get(name string) (CapabilityInfo, bool) {
	info, ok := r.byName[name]
	return info, ok
}

// List returns all capabilities in registration order.
func (r *Registry) List() []CapabilityInfo {
	out := make([]CapabilityInfo, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.byName[name])
	}
	return out
}

// EnabledList filters capabilities to those whose source is enabled
// (e.g. {"builtin": toolsOn, "web": toolsOn && webOn}).
func (r *Registry) EnabledList(sources map[string]bool) []CapabilityInfo {
	var out []CapabilityInfo
	for _, info := range r.List() {
		if sources[info.Source] {
			out = append(out, info)
		}
	}
	return out
}

// safetyForBuiltin maps the built-in tools to their safety class.
var safetyForBuiltin = map[string]SafetyClass{
	ToolListDir:    SafetyReadOnly,
	ToolReadFile:   SafetyReadOnly,
	ToolWriteFile:  SafetyWorkspaceWrite,
	ToolRunCommand: SafetyCommand,
	// skill_load only changes prompt state inside the app: no file, command,
	// or network effect, and no permission grant.
	ToolSkillLoad: SafetyReadOnly,
}

// approvalForTool is the static approval policy per tool.
var approvalForTool = map[string]string{
	ToolListDir:    "no",
	ToolReadFile:   "ask for secret files",
	ToolWriteFile:  "ask",
	ToolRunCommand: "ask unless read-only",
	ToolWebSearch:  "no",
	ToolWebFetch:   "ask",
	ToolSkillLoad:  "no",
}

// DefaultRegistry catalogs the built-in workspace tools and the web tools,
// reusing the native function-calling specs as the source of truth for
// names, descriptions, and schemas.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	for _, s := range Specs() {
		// Registering a fixed spec list cannot collide.
		_ = r.Register(CapabilityInfo{
			Name:        s.Name,
			Description: s.Description,
			Source:      "builtin",
			Safety:      safetyForBuiltin[s.Name],
			Approval:    approvalForTool[s.Name],
			Parameters:  s.Parameters,
		})
	}
	for _, s := range WebSpecs() {
		_ = r.Register(CapabilityInfo{
			Name:        s.Name,
			Description: s.Description,
			Source:      "web",
			Safety:      SafetyNetwork,
			Approval:    approvalForTool[s.Name],
			Parameters:  s.Parameters,
		})
	}
	for _, s := range SkillSpecs() {
		_ = r.Register(CapabilityInfo{
			Name:        s.Name,
			Description: s.Description,
			Source:      "skills",
			Safety:      safetyForBuiltin[s.Name],
			Approval:    approvalForTool[s.Name],
			Parameters:  s.Parameters,
		})
	}
	return r
}
