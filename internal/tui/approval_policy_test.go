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

func TestCapabilityPolicyScopesVersionSelector(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	reviewed := tools.Call{Tool: tools.ToolWriteFile, Path: "cfg.go", Body: "package p\n", ExpectedResourceID: "ent_aaaaaaaaaaaaaaaaaaaaaaaaaa"}
	policy.GrantCall(reviewed, now, time.Hour)
	if !policy.Allows(reviewed, now) {
		t.Fatal("the reviewed versioned write was not allowed")
	}
	other := reviewed
	other.ExpectedResourceID = "ent_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	if policy.Allows(other, now) {
		t.Fatal("approval grant ignored expected_resource_id")
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

func TestCapabilityPolicyScopesWebResearchRequest(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	var policy capabilityPolicy
	granted := tools.Call{Tool: tools.ToolWebResearch, Body: "latest go release", Max: 4,
		WebQueries: [tools.MaxWebResearchQueries]string{"go 1.27"}, WebQueryCount: 1}
	if got := policy.GrantCall(granted, now, time.Minute); got != "this exact web_research request" {
		t.Fatalf("grant description = %q", got)
	}
	if !policy.Allows(granted, now) {
		t.Fatal("the approved research request was not allowed")
	}
	for name, call := range map[string]tools.Call{
		"other question": {Tool: tools.ToolWebResearch, Body: "something else", Max: 4,
			WebQueries: [tools.MaxWebResearchQueries]string{"go 1.27"}, WebQueryCount: 1},
		"extra query": {Tool: tools.ToolWebResearch, Body: "latest go release", Max: 4,
			WebQueries: [tools.MaxWebResearchQueries]string{"go 1.27", "secret token"}, WebQueryCount: 2},
		"more pages": {Tool: tools.ToolWebResearch, Body: "latest go release", Max: 6,
			WebQueries: [tools.MaxWebResearchQueries]string{"go 1.27"}, WebQueryCount: 1},
		"web_fetch": {Tool: tools.ToolWebFetch, Path: "https://go.dev/"},
	} {
		if policy.Allows(call, now) {
			t.Errorf("%s: grant escaped the exact research request", name)
		}
	}
}

// An edit_file grant covers the exact edit list and dry-run flag: approving
// a dry run (asked only for a secret file) never authorises the real write,
// and approving one replacement never covers an extra one.
func TestCapabilityPolicyScopesMultiEditAndDryRun(t *testing.T) {
	now := time.Now()
	edit := func(dry bool, pairs ...tools.EditPair) tools.Call {
		c := tools.Call{Tool: tools.ToolEditFile, Path: ".env", DryRun: dry, EditCount: len(pairs)}
		copy(c.Edits[:], pairs)
		c.OldText, c.NewText = pairs[0].OldText, pairs[0].NewText
		return c
	}
	var policy capabilityPolicy
	reviewed := edit(true, tools.EditPair{OldText: "A", NewText: "B"})
	policy.GrantCall(reviewed, now, time.Hour)
	if !policy.Allows(reviewed, now) {
		t.Fatal("the reviewed dry run was not allowed")
	}
	for name, other := range map[string]tools.Call{
		"real write":  edit(false, tools.EditPair{OldText: "A", NewText: "B"}),
		"extra edit":  edit(true, tools.EditPair{OldText: "A", NewText: "B"}, tools.EditPair{OldText: "C", NewText: "D"}),
		"legacy pair": {Tool: tools.ToolEditFile, Path: ".env", OldText: "A", NewText: "B"},
	} {
		if policy.Allows(other, now) {
			t.Errorf("%s: dry-run grant leaked", name)
		}
	}
}

// A move grant is pinned to its exact source and destination, and neither a
// move nor a mkdir can be blanket-approved by a path pattern.
func TestCapabilityPolicyScopesStructuralWrites(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	move := tools.Call{Tool: tools.ToolMoveFile, Path: "a.go", Destination: "b.go"}
	policy.GrantCall(move, now, time.Hour)
	if !policy.Allows(tools.Call{Tool: tools.ToolMoveFile, Path: "./a.go", Destination: "b.go"}, now) {
		t.Fatal("the reviewed move was not allowed")
	}
	for _, other := range []tools.Call{
		{Tool: tools.ToolMoveFile, Path: "a.go", Destination: "c.go"},
		{Tool: tools.ToolMoveFile, Path: "x.go", Destination: "b.go"},
		{Tool: tools.ToolCreateDirectory, Path: "a.go"},
	} {
		if policy.Allows(other, now) {
			t.Fatalf("move grant leaked to %+v", other)
		}
	}
	var patterns capabilityPolicy
	patterns.GrantPath(tools.ToolMoveFile, "*", now, time.Hour)
	patterns.GrantPath(tools.ToolCreateDirectory, "*", now, time.Hour)
	if patterns.Allows(move, now) || patterns.Allows(tools.Call{Tool: tools.ToolCreateDirectory, Path: "build"}, now) {
		t.Fatal("a path pattern blanket-approved a structural write")
	}
}

func multiRead(paths ...string) tools.Call {
	c := tools.Call{Tool: tools.ToolReadMultipleFiles, PathCount: len(paths)}
	copy(c.Paths[:], paths)
	return c
}

// A read_multiple_files grant (it asks only when a secret path is named) is
// pinned to the exact path set, and a path-pattern grant never covers a batch.
func TestCapabilityPolicyScopesReadMultipleFilesPaths(t *testing.T) {
	now := time.Now()
	var policy capabilityPolicy
	policy.GrantCall(multiRead("a.go", ".env"), now, time.Hour)
	if !policy.Allows(multiRead("a.go", "./.env"), now) {
		t.Fatal("the reviewed batch was not allowed")
	}
	for _, other := range []tools.Call{
		multiRead("a.go", ".env.production"),
		multiRead(".env"),
		{Tool: tools.ToolReadFile, Path: ".env"},
	} {
		if policy.Allows(other, now) {
			t.Fatalf("read_multiple_files grant leaked to %+v", other)
		}
	}
	var patterns capabilityPolicy
	patterns.GrantPath(tools.ToolReadMultipleFiles, "*", now, time.Hour)
	if patterns.Allows(multiRead("a.go", ".env"), now) {
		t.Fatal("a path pattern blanket-approved a read_multiple_files batch")
	}
}
