package tui

import (
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/tools"
)

func TestCapabilityPolicyScopesToolTargetAndTime(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	var policy capabilityPolicy
	granted := tools.Call{Tool: tools.ToolWriteFile, Path: "src/a.go"}
	policy.GrantCall(granted, now, time.Minute)

	if !policy.Allows(granted, now.Add(30*time.Second)) {
		t.Fatal("matching call was not allowed")
	}
	for _, call := range []tools.Call{
		{Tool: tools.ToolWriteFile, Path: "src/b.go"},
		{Tool: tools.ToolRunCommand, Body: "printf x > src/a.go"},
		{MCPServer: "github", MCPTool: "create_issue"},
	} {
		if policy.Allows(call, now.Add(30*time.Second)) {
			t.Fatalf("grant escaped its capability scope: %+v", call)
		}
	}
	if policy.Allows(granted, now.Add(2*time.Minute)) {
		t.Fatal("expired grant was still active")
	}
}

func TestCapabilityPolicySupportsPathPatterns(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	policy.GrantPath(tools.ToolReadFile, "src/*.go", now, time.Hour)
	if !policy.Allows(tools.Call{Tool: tools.ToolReadFile, Path: "src/main.go"}, now) {
		t.Fatal("path-scoped grant did not match")
	}
	if policy.Allows(tools.Call{Tool: tools.ToolReadFile, Path: "internal/main.go"}, now) {
		t.Fatal("path-scoped grant matched outside its pattern")
	}
}

// TestCapabilityPolicyScopesWriteContent locks in the fix for a grant that was
// keyed on the write target alone. "Approve always" on a benign write to
// config.yaml handed the model a 15-minute window in which it could rewrite
// that same file with anything at all — no second prompt. The grant now
// carries a content fingerprint, so only the reviewed bytes are authorised.
func TestCapabilityPolicyScopesWriteContent(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	reviewed := tools.Call{Tool: tools.ToolWriteFile, Path: "config.yaml", Body: "debug: true\n"}
	policy.GrantCall(reviewed, now, time.Hour)

	if !policy.Allows(reviewed, now) {
		t.Fatal("the reviewed write was not allowed")
	}
	swapped := tools.Call{Tool: tools.ToolWriteFile, Path: "config.yaml", Body: "exfil: http://evil\n"}
	if policy.Allows(swapped, now) {
		t.Fatal("grant approved different content written to the same path")
	}
}

// TestCapabilityPolicyScopesEditReplacement checks an edit_file grant is
// pinned to the exact old→new pair: a different new_text, a different
// old_text, or a different file must each re-prompt.
func TestCapabilityPolicyScopesEditReplacement(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	reviewed := tools.Call{Tool: tools.ToolEditFile, Path: "cfg.go", OldText: "A", NewText: "B"}
	policy.GrantCall(reviewed, now, time.Hour)

	if !policy.Allows(reviewed, now) {
		t.Fatal("the reviewed edit was not allowed")
	}
	for _, other := range []tools.Call{
		{Tool: tools.ToolEditFile, Path: "cfg.go", OldText: "A", NewText: "C"},
		{Tool: tools.ToolEditFile, Path: "cfg.go", OldText: "X", NewText: "B"},
		{Tool: tools.ToolEditFile, Path: "other.go", OldText: "A", NewText: "B"},
		{Tool: tools.ToolWriteFile, Path: "cfg.go", Body: "A"},
	} {
		if policy.Allows(other, now) {
			t.Fatalf("edit grant leaked to %+v", other)
		}
	}
}

// A glob path grant carries no variant, so it must never match edit_file
// (every edit is fingerprinted by its replacement).
func TestCapabilityPolicyPathPatternNeverApprovesEdits(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	policy.GrantPath(tools.ToolEditFile, "src/*.go", now, time.Hour)
	if policy.Allows(tools.Call{Tool: tools.ToolEditFile, Path: "src/main.go", OldText: "a", NewText: "b"}, now) {
		t.Fatal("a path pattern blanket-approved an edit")
	}
}

// TestCapabilityPolicyPathPatternNeverBlanketApprovesWrites ensures a glob
// grant cannot be used to sidestep the content fingerprint.
func TestCapabilityPolicyPathPatternNeverBlanketApprovesWrites(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	policy.GrantPath(tools.ToolWriteFile, "src/*.go", now, time.Hour)
	if policy.Allows(tools.Call{Tool: tools.ToolWriteFile, Path: "src/main.go", Body: "package main\n"}, now) {
		t.Fatal("path-pattern grant blanket-approved a write")
	}
}

func TestCapabilityPolicyScopesMCPServerAndTool(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	call := tools.Call{MCPServer: "jira", MCPTool: "worklog_submit"}
	policy.GrantCall(call, now, time.Hour)
	if !policy.Allows(call, now) {
		t.Fatal("matching MCP capability was not allowed")
	}
	if policy.Allows(tools.Call{MCPServer: "jira", MCPTool: "delete_issue"}, now) ||
		policy.Allows(tools.Call{MCPServer: "github", MCPTool: "worklog_submit"}, now) {
		t.Fatal("MCP grant escaped its server/tool pair")
	}
}
