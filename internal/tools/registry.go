package tools

import (
	"encoding/json"
	"fmt"

	"github.com/patrikcze/llmtui/internal/personalapps"
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
	// SafetyPersonalApps is its own class rather than SafetyNetwork or
	// SafetyWorkspaceWrite: one tool call can be a bounded metadata read or
	// an exact-plan-bound mutation of personal mail/calendar data depending
	// on its operation, and neither existing class fits either shape.
	SafetyPersonalApps SafetyClass = "personal_apps"
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
	ToolListDir:      SafetyReadOnly,
	ToolReadFile:     SafetyReadOnly,
	ToolGlob:         SafetyReadOnly,
	ToolGrep:         SafetyReadOnly,
	ToolWriteFile:    SafetyWorkspaceWrite,
	ToolEditFile:     SafetyWorkspaceWrite,
	ToolRunCommand:   SafetyCommand,
	ToolAskUser:      SafetyReadOnly,
	ToolLocalContext: SafetyReadOnly,
	ToolSearch:       SafetyReadOnly,
	// skill_load only changes prompt state inside the app: no file, command,
	// or network effect, and no permission grant.
	ToolSkillLoad: SafetyReadOnly,
	// personal_apps's twelve native tools (one per operation, see
	// PersonalAppsSpecs) are registered directly with SafetyPersonalApps in
	// DefaultRegistry below, not through this map — their Name is the
	// operation string (e.g. "mail_search"), not ToolPersonalApps.
}

// approvalForTool is the static approval policy per tool.
var approvalForTool = map[string]string{
	ToolListDir:      "no",
	ToolReadFile:     "ask for secret files",
	ToolGlob:         "no",
	ToolGrep:         "ask for an explicit secret file",
	ToolWriteFile:    "ask",
	ToolEditFile:     "ask",
	ToolRunCommand:   "ask unless read-only",
	ToolWebSearch:    "no for ordinary queries; ask for bulk or opaque ones",
	ToolWebFetch:     "ask",
	ToolSkillLoad:    "no",
	ToolAskUser:      "no (never authorizes another tool)",
	ToolLocalContext: "ask for clipboard; otherwise no",
	ToolSearch:       "no (discovery only)",
	// personal_apps: see personalAppsApprovalText below, used directly in
	// DefaultRegistry — this map is keyed by native tool Name, and
	// personal_apps now registers one entry per operation.
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
	for _, s := range PersonalAppsSpecs() {
		_ = r.Register(CapabilityInfo{
			Name:        s.Name,
			Description: s.Description,
			Source:      "personal_apps",
			Safety:      SafetyPersonalApps,
			Approval:    personalAppsApprovalText(personalapps.Operation(s.Name)),
			Parameters:  s.Parameters,
		})
	}
	return r
}

// personalAppsApprovalText gives each personal_apps native tool its own
// approval-policy description for the registry/tool_search display. Actual
// enforcement is Runner.NeedsApproval and internal/tui's
// callNeedsApproval, both keyed on Call.Tool == ToolPersonalApps regardless
// of which of the twelve native names a call dispatched through — this only
// affects what a human sees listed for each one.
func personalAppsApprovalText(op personalapps.Operation) string {
	if op == personalapps.OpChangeApply {
		return "always ask, bound to one exact approved plan — never covered by /tools auto"
	}
	return "no (read-only or preview-only; change_apply is the only personal_apps operation that mutates anything)"
}
