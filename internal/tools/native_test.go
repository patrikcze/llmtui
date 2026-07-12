package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestSpecsAreValidJSONSchemas(t *testing.T) {
	specs := Specs()
	if len(specs) != 4 {
		t.Fatalf("specs = %d, want 4", len(specs))
	}
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
		var schema map[string]any
		if err := json.Unmarshal(s.Parameters, &schema); err != nil {
			t.Errorf("%s: parameters are not valid JSON: %v", s.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s: schema type = %v, want object", s.Name, schema["type"])
		}
		if s.Description == "" {
			t.Errorf("%s: missing description", s.Name)
		}
	}
	for _, want := range []string{ToolListDir, ToolReadFile, ToolWriteFile, ToolRunCommand} {
		if !names[want] {
			t.Errorf("missing spec for %s", want)
		}
	}
}

func TestCallsFromNative(t *testing.T) {
	tests := []struct {
		name string
		in   provider.ToolCall
		want Call
	}{
		{
			name: "read_file",
			in:   provider.ToolCall{ID: "c1", Name: ToolReadFile, Arguments: `{"path":"a.txt"}`},
			want: Call{ID: "c1", Tool: ToolReadFile, Path: "a.txt"},
		},
		{
			name: "write_file",
			in:   provider.ToolCall{ID: "c2", Name: ToolWriteFile, Arguments: `{"path":"b.txt","content":"data"}`},
			want: Call{ID: "c2", Tool: ToolWriteFile, Path: "b.txt", Body: "data"},
		},
		{
			name: "run_command",
			in:   provider.ToolCall{ID: "c3", Name: ToolRunCommand, Arguments: `{"command":"ls"}`},
			want: Call{ID: "c3", Tool: ToolRunCommand, Body: "ls"},
		},
		{
			name: "missing id is filled",
			in:   provider.ToolCall{Name: ToolListDir, Arguments: `{}`},
			want: Call{ID: "call_0", Tool: ToolListDir},
		},
		{
			name: "malformed arguments still produce a call",
			in:   provider.ToolCall{ID: "c4", Name: ToolReadFile, Arguments: `{not json`},
			want: Call{ID: "c4", Tool: ToolReadFile},
		},
		{
			name: "unknown tool passes through for the runner to report",
			in:   provider.ToolCall{ID: "c5", Name: "delete_everything", Arguments: `{}`},
			want: Call{ID: "c5", Tool: "delete_everything"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CallsFromNative([]provider.ToolCall{tc.in})
			if len(got) != 1 {
				t.Fatalf("calls = %d, want 1", len(got))
			}
			if got[0] != tc.want {
				t.Errorf("call = %+v, want %+v", got[0], tc.want)
			}
		})
	}
}

func TestNativeResults(t *testing.T) {
	results := []Result{
		{Call: Call{ID: "c1", Tool: ToolListDir}, Output: "a.txt\nb.txt"},
		{Call: Call{ID: "c2", Tool: ToolReadFile, Path: "x"}, Err: ErrDenied},
	}
	msgs := NativeResults(results)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if msgs[0].Role != provider.RoleTool || msgs[0].ToolCallID != "c1" ||
		msgs[0].ToolName != ToolListDir || msgs[0].Content != "a.txt\nb.txt" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].ToolCallID != "c2" || !strings.Contains(msgs[1].Content, "denied by the user") {
		t.Errorf("msg[1] = %+v", msgs[1])
	}
}

func TestLimitResults(t *testing.T) {
	calls := []Call{{ID: "c1", Tool: ToolListDir}, {ID: "c2", Tool: ToolReadFile}}
	results := LimitResults(calls, 10)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, res := range results {
		if res.Err == nil || !strings.Contains(res.Err.Error(), "iteration limit") {
			t.Errorf("result err = %v, want iteration-limit explanation", res.Err)
		}
		if !strings.Contains(res.Err.Error(), "final answer") {
			t.Errorf("result err = %v, want wrap-up instruction", res.Err)
		}
	}
}

func TestSummarizeOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "(no output)"},
		{"single short line", "wrote 46 bytes to test.ps1", "wrote 46 bytes to test.ps1"},
		{"multi-line counts", "a\nb\nc", "3 lines of output"},
		{"error kept visible", "error: no such file", "error: no such file"},
		{"error with extra lines", "error: boom\ndetail\nmore", "error: boom (+2 lines)"},
		{"long single line truncated", strings.Repeat("x", 150), strings.Repeat("x", 100) + "…"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SummarizeOutput(tc.in); got != tc.want {
				t.Errorf("SummarizeOutput(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCollapseResults(t *testing.T) {
	content := FormatResults([]Result{
		{Call: Call{Tool: ToolListDir}, Output: "a.txt\nb.txt\nc.txt"},
		{Call: Call{Tool: ToolReadFile, Path: "x.go"}, Err: ErrDenied},
	})
	got := CollapseResults(content)
	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("collapsed to %d lines, want 2: %q", len(lines), got)
	}
	if lines[0] != "  ⎿ list_dir → 3 lines of output" {
		t.Errorf("line 0 = %q", lines[0])
	}
	if !strings.Contains(lines[1], "read_file x.go") || !strings.Contains(lines[1], "denied by the user") {
		t.Errorf("line 1 = %q", lines[1])
	}
}

func TestCollapseBlocksReplacesToolBlocks(t *testing.T) {
	reply := "Saving the script:\n```tool write_file a.sh\n#!/bin/sh\necho hi\n```\nDone."
	got := CollapseBlocks(reply)
	if strings.Contains(got, "echo hi") {
		t.Errorf("body leaked into collapsed view: %q", got)
	}
	if !strings.Contains(got, "⚒ write a.sh (18 bytes)") {
		t.Errorf("missing collapsed description: %q", got)
	}
	if !strings.Contains(got, "Saving the script:") || !strings.Contains(got, "Done.") {
		t.Errorf("surrounding prose lost: %q", got)
	}

	// Unterminated blocks and plain code fences stay untouched.
	plain := "```go\nfmt.Println(1)\n```"
	if CollapseBlocks(plain) != plain {
		t.Error("plain code fence modified")
	}
}

func TestNativeInstructionsMentionRootAndRules(t *testing.T) {
	s := NativeInstructions("/work/proj", false)
	for _, want := range []string{"/work/proj", "relative", "approval", "final answer"} {
		if !strings.Contains(s, want) {
			t.Errorf("native instructions missing %q", want)
		}
	}
	if strings.Contains(s, "```") {
		t.Error("native instructions must not teach the fenced protocol")
	}
}

func TestSplitMCPToolName(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantServer string
		wantTool   string
		wantOK     bool
	}{
		{"valid", "mcp__jiraWorklog__session_start", "jiraWorklog", "session_start", true},
		{"tool name itself contains underscores", "mcp__jiraWorklog__jira_get_issue", "jiraWorklog", "jira_get_issue", true},
		{"no prefix", "session_start", "", "", false},
		{"empty tool part", "mcp__jiraWorklog__", "", "", false},
		{"empty server part", "mcp____session_start", "", "", false},
		{"native tool name", "write_file", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, tool, ok := SplitMCPToolName(tc.in)
			if server != tc.wantServer || tool != tc.wantTool || ok != tc.wantOK {
				t.Errorf("SplitMCPToolName(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.in, server, tool, ok, tc.wantServer, tc.wantTool, tc.wantOK)
			}
		})
	}
}

func TestJoinMCPToolName(t *testing.T) {
	got := JoinMCPToolName("jiraWorklog", "session_start")
	want := "mcp__jiraWorklog__session_start"
	if got != want {
		t.Errorf("JoinMCPToolName = %q, want %q", got, want)
	}
	server, tool, ok := SplitMCPToolName(got)
	if !ok || server != "jiraWorklog" || tool != "session_start" {
		t.Errorf("round-trip failed: SplitMCPToolName(JoinMCPToolName(...)) = (%q,%q,%v)", server, tool, ok)
	}
}

func TestCallsFromNativeMCP(t *testing.T) {
	tests := []struct {
		name string
		in   provider.ToolCall
		want Call
	}{
		{
			name: "mcp call splits server and tool, keeps raw arguments",
			in:   provider.ToolCall{ID: "c1", Name: "mcp__jiraWorklog__session_start", Arguments: `{"issue_key":"DEMO-1"}`},
			want: Call{ID: "c1", Tool: "mcp__jiraWorklog__session_start", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{"issue_key":"DEMO-1"}`},
		},
		{
			name: "empty arguments default to an empty object",
			in:   provider.ToolCall{ID: "c2", Name: "mcp__jiraWorklog__session_list", Arguments: ""},
			want: Call{ID: "c2", Tool: "mcp__jiraWorklog__session_list", MCPServer: "jiraWorklog", MCPTool: "session_list", MCPArgs: "{}"},
		},
		{
			name: "malformed prefix (no tool part) is not treated as an MCP call",
			in:   provider.ToolCall{ID: "c3", Name: "mcp__jiraWorklog__", Arguments: `{}`},
			want: Call{ID: "c3", Tool: "mcp__jiraWorklog__"},
		},
		{
			name: "name without the mcp__ prefix is not treated as an MCP call",
			in:   provider.ToolCall{ID: "c4", Name: "session_start", Arguments: `{}`},
			want: Call{ID: "c4", Tool: "session_start"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CallsFromNative([]provider.ToolCall{tc.in})
			if len(got) != 1 {
				t.Fatalf("calls = %d, want 1", len(got))
			}
			if got[0] != tc.want {
				t.Errorf("call = %+v, want %+v", got[0], tc.want)
			}
		})
	}
}
