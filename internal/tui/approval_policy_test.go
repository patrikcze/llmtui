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
