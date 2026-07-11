package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/tools"
)

// newConnectedMCPRegistry builds a registry with one server already
// connected via a MockClient advertising the given tools.
func newConnectedMCPRegistry(t *testing.T, serverName string, mockTools []mcp.Tool, callFunc func(name string, input json.RawMessage) (mcp.Result, error)) *mcp.Registry {
	t.Helper()
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, CannedTools: mockTools, CallFunc: callFunc}, nil
	}
	reg := mcp.NewRegistry([]mcp.ServerConfig{{
		Name: serverName, Transport: mcp.TransportStdio, Command: "mock", Enabled: true, Timeout: 5 * time.Second,
	}}, factory)
	if err := reg.Connect(context.Background(), serverName); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return reg
}

func TestMcpToolSpecsOnlyIncludesConnectedEnabledServers(t *testing.T) {
	connected := newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Description: "start a session", Schema: json.RawMessage(`{"type":"object"}`)},
	}, nil)

	// A second server that's configured but never connected must contribute nothing.
	factory := func(c mcp.ServerConfig) (mcp.Client, error) { return &mcp.MockClient{ServerName: c.Name}, nil }
	notConnected := mcp.NewRegistry([]mcp.ServerConfig{{Name: "other", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: time.Second}}, factory)

	specs := mcpToolSpecs(connected)
	if len(specs) != 1 || specs[0].Name != "mcp__jiraWorklog__session_start" {
		t.Fatalf("specs = %+v, want one spec named mcp__jiraWorklog__session_start", specs)
	}
	if specs[0].Description != "start a session" {
		t.Errorf("description = %q", specs[0].Description)
	}

	if specs := mcpToolSpecs(notConnected); len(specs) != 0 {
		t.Errorf("unconnected server contributed specs: %+v", specs)
	}

	if specs := mcpToolSpecs(nil); specs != nil {
		t.Errorf("nil registry should produce nil specs, got %+v", specs)
	}
}

func TestExecuteMCPCallSuccess(t *testing.T) {
	reg := newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Schema: json.RawMessage(`{"type":"object"}`)},
	}, func(name string, input json.RawMessage) (mcp.Result, error) {
		return mcp.Result{Content: `{"session":{"id":"ses_1"}}`}, nil
	})
	c := tools.Call{ID: "call_1", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{"issue_key":"AIPO-82"}`}
	res := executeMCPCall(context.Background(), reg, c)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Output != `{"session":{"id":"ses_1"}}` {
		t.Errorf("output = %q", res.Output)
	}
}

func TestExecuteMCPCallServerReportsIsError(t *testing.T) {
	reg := newConnectedMCPRegistry(t, "jiraWorklog", nil, func(name string, input json.RawMessage) (mcp.Result, error) {
		return mcp.Result{Content: "issue key not found", IsError: true}, nil
	})
	c := tools.Call{MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{}`}
	res := executeMCPCall(context.Background(), reg, c)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "issue key not found") {
		t.Errorf("err = %v, want it to surface the server's error content", res.Err)
	}
}

func TestExecuteMCPCallUnknownServer(t *testing.T) {
	reg := mcp.NewRegistry(nil, nil)
	res := executeMCPCall(context.Background(), reg, tools.Call{MCPServer: "ghost", MCPTool: "x", MCPArgs: `{}`})
	if res.Err == nil {
		t.Fatal("expected an error for an unknown server")
	}
}

func TestExecuteMCPCallDisconnectedServer(t *testing.T) {
	// Configured but never connected: Get succeeds, CallTool must still fail.
	reg := mcp.NewRegistry([]mcp.ServerConfig{{Name: "srv", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: time.Second}}, nil)
	res := executeMCPCall(context.Background(), reg, tools.Call{MCPServer: "srv", MCPTool: "x", MCPArgs: `{}`})
	if res.Err == nil {
		t.Fatal("expected an error for a disconnected server")
	}
}

func TestExecuteMCPCallTimeout(t *testing.T) {
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, Delay: 200 * time.Millisecond}, nil
	}
	reg := mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "slow", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: 10 * time.Millisecond,
	}}, factory)
	if err := reg.Connect(context.Background(), "slow"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	start := time.Now()
	res := executeMCPCall(context.Background(), reg, tools.Call{MCPServer: "slow", MCPTool: "x", MCPArgs: `{}`})
	elapsed := time.Since(start)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "timed out") {
		t.Errorf("err = %v, want a timeout error", res.Err)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("executeMCPCall took %s, want it bounded by the 10ms server timeout, not the 200ms Delay", elapsed)
	}
}

func TestContainsMCPCall(t *testing.T) {
	if containsMCPCall([]tools.Call{{Tool: "read_file"}}) {
		t.Error("pure-native batch reported as containing an MCP call")
	}
	if !containsMCPCall([]tools.Call{{Tool: "read_file"}, {MCPServer: "s", MCPTool: "t"}}) {
		t.Error("mixed batch not detected as containing an MCP call")
	}
}

func TestRunMixedToolBatchPreservesOrderAndRunsNativeToo(t *testing.T) {
	reg := newConnectedMCPRegistry(t, "jiraWorklog", nil, func(name string, input json.RawMessage) (mcp.Result, error) {
		return mcp.Result{Content: "mcp-ok"}, nil
	})
	runner := tools.NewRunner(t.TempDir(), 64)
	calls := []tools.Call{
		{ID: "c1", Tool: tools.ToolListDir},
		{ID: "c2", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{}`},
	}
	cmd := runMixedToolBatch(context.Background(), runner, reg, calls)
	msg, ok := cmd().(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want mcpToolResultsMsg", cmd())
	}
	if len(msg.results) != 2 {
		t.Fatalf("results = %d, want 2", len(msg.results))
	}
	if msg.results[0].Call.ID != "c1" || msg.results[0].Err != nil {
		t.Errorf("native result[0] = %+v", msg.results[0])
	}
	if msg.results[1].Call.ID != "c2" || msg.results[1].Output != "mcp-ok" {
		t.Errorf("mcp result[1] = %+v", msg.results[1])
	}
}

func TestRegisterMCPCapabilities(t *testing.T) {
	reg := newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Description: "start a session", Schema: json.RawMessage(`{"type":"object"}`)},
	}, nil)
	capReg := tools.NewRegistry()
	registerMCPCapabilities(capReg, reg)
	info, ok := capReg.Get("mcp__jiraWorklog__session_start")
	if !ok {
		t.Fatal("connected server's tool was not registered")
	}
	if info.Source != "mcp:jiraWorklog" || info.Safety != tools.SafetyExternalMCP {
		t.Errorf("capability = %+v", info)
	}
}

func TestToolsListShowsConnectedMCPServer(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.mcpRegistry = newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Description: "start a session", Schema: json.RawMessage(`{"type":"object"}`)},
	}, nil)

	out := m.toolsListOverlay("")
	if !strings.Contains(out, "mcp__jiraWorklog__session_start") {
		t.Errorf("connected MCP server's tool missing from /tools list:\n%s", out)
	}
	if !strings.Contains(out, "mcp:jiraWorklog") {
		t.Errorf("mcp:jiraWorklog source column missing:\n%s", out)
	}
}

func TestToolsListOmitsUnconnectedMCPServer(t *testing.T) {
	m := newTestModel(t)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) { return &mcp.MockClient{ServerName: c.Name}, nil }
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: time.Second,
	}}, factory) // configured, never connected

	out := m.toolsListOverlay("")
	if strings.Contains(out, "jiraWorklog") {
		t.Errorf("unconnected MCP server should not appear in /tools list:\n%s", out)
	}
}

func TestToolsInspectShowsMCPToolParameters(t *testing.T) {
	m := newTestModel(t)
	m.mcpRegistry = newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Description: "start a session", Schema: json.RawMessage(`{"type":"object","properties":{"issue_key":{"type":"string"}}}`)},
	}, nil)

	out := m.toolsInspectOverlay("mcp__jiraWorklog__session_start")
	if !strings.Contains(out, "start a session") || !strings.Contains(out, "issue_key") {
		t.Errorf("/tools inspect missing MCP tool detail:\n%s", out)
	}
}
