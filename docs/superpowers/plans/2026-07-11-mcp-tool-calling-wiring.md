# MCP Tool-Calling Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `internal/mcp`'s already-built `Registry.CallTool` into the model's native function-calling loop, so a connected MCP server's tools (e.g. `jiraWorklog`) are actually callable by the model, not just inspectable via `/mcp`.

**Architecture:** A new `internal/tui/mcp_tools.go` is the only file that imports both `internal/tools` and `internal/mcp`. `internal/tools.Call` gains three opaque fields (`MCPServer`, `MCPTool`, `MCPArgs`) that `internal/tools` threads through without interpreting. Pure-native tool batches keep today's exact synchronous path; a batch containing any MCP call runs asynchronously (bounded by `ServerConfig.Timeout`, cancellable) via a new `tea.Cmd`/`Msg` pair. MCP calls get an approval state (`mcpAutoApprove`) independent of the native-tools one.

**Tech Stack:** Go, Bubble Tea (`tea.Cmd`/`tea.Msg` async pattern), existing `internal/mcp` stdio JSON-RPC client, existing `internal/cache` SHA-256 content-hash cache key.

## Global Constraints

- Native function-calling only — no fenced-block/text-protocol support for MCP tools this round (see spec's Non-goals).
- No changes to `internal/mcp`'s registry/stdio transport/config schema beyond the one `MockClient` test-double addition in Task 2.
- Every new failure mode (unknown server, disconnected server, malformed args, timeout, cancellation) must produce a `tools.Result{Err: ...}` fed back to the model — never a panic, never a silently dropped call.
- `go fmt ./...`, `go vet ./...`, and `go test ./...` must all pass after every task.
- Full design context: `docs/superpowers/specs/2026-07-11-mcp-tool-calling-wiring-design.md`.

---

### Task 1: `tools.Call` MCP fields, naming helpers, and `Describe()`

**Files:**
- Modify: `internal/tools/tools.go:46-53` (the `Call` struct), `internal/tools/tools.go:576-593` (`Describe()`)
- Modify: `internal/tools/native.go:74-106` (`CallsFromNative`)
- Test: `internal/tools/native_test.go`, `internal/tools/tools_test.go`

**Interfaces:**
- Produces: `Call.MCPServer, Call.MCPTool, Call.MCPArgs string` (all empty for non-MCP calls); `SplitMCPToolName(name string) (server, tool string, ok bool)`; `JoinMCPToolName(server, tool string) string` — both exported from package `tools`, used by `internal/tui/mcp_tools.go` in Task 3.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tools/native_test.go` (same file already containing `TestCallsFromNative`):

```go
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
```

Add to `internal/tools/tools_test.go`:

```go
func TestDescribeMCPCall(t *testing.T) {
	c := Call{MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{"issue_key":"DEMO-1"}`}
	got := c.Describe()
	want := `jiraWorklog: session_start({"issue_key":"DEMO-1"})`
	if got != want {
		t.Errorf("Describe = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tools/... -run 'TestSplitMCPToolName|TestJoinMCPToolName|TestCallsFromNativeMCP|TestDescribeMCPCall' -v`
Expected: FAIL — `SplitMCPToolName`, `JoinMCPToolName` undefined; `Call` has no field `MCPServer`.

- [ ] **Step 3: Extend the `Call` struct**

In `internal/tools/tools.go`, replace the `Call` struct (lines 46-53):

```go
// Call is one tool invocation: parsed from a fenced block in an assistant
// reply, or converted from a native function call (in which case ID is set
// and the results must go back as role:"tool" messages).
type Call struct {
	ID   string
	Tool string
	Path string
	Body string
	// Max caps web_search results (native max_results argument).
	Max int

	// MCPServer, when non-empty, marks this as a call to an MCP server's
	// tool rather than a built-in one. MCPTool is the tool's name on that
	// server, and MCPArgs is the raw JSON arguments to pass through
	// unparsed — MCP tool schemas are arbitrary and unknown to this
	// package, unlike the built-in tools' hand-mapped Path/Body/Max.
	MCPServer string
	MCPTool   string
	MCPArgs   string
}
```

- [ ] **Step 4: Extend `Describe()`**

In `internal/tools/tools.go`, replace `Describe()` (lines 576-593):

```go
// Describe renders one call for the approval prompt.
func (c Call) Describe() string {
	if c.MCPServer != "" {
		return fmt.Sprintf("%s: %s(%s)", c.MCPServer, c.MCPTool, truncateLine(c.MCPArgs, 80))
	}
	switch c.Tool {
	case ToolRunCommand:
		return "run: " + strings.TrimSpace(c.Body)
	case ToolWriteFile:
		return fmt.Sprintf("write %s (%d bytes)", c.Path, len(c.Body))
	case ToolWebSearch:
		return fmt.Sprintf("web_search(%q)", strings.TrimSpace(c.Body))
	case ToolWebFetch:
		return "fetch " + c.Path
	default:
		if c.Path == "" {
			return c.Tool
		}
		return c.Tool + " " + c.Path
	}
}
```

- [ ] **Step 5: Add the naming helpers and extend `CallsFromNative`**

In `internal/tools/native.go`, add above `CallsFromNative` (after the `nativeArgs` type, before line 74):

```go
// mcpToolPrefix marks a native tool name as routing to an MCP server's tool:
// "mcp__<server>__<tool>". internal/tui builds names in this shape when
// assembling tool specs for a connected server; SplitMCPToolName splits them
// back out on the way in.
const mcpToolPrefix = "mcp__"

// JoinMCPToolName builds the native tool name that exposes one MCP server's
// tool to the model, matching SplitMCPToolName.
func JoinMCPToolName(server, tool string) string {
	return mcpToolPrefix + server + "__" + tool
}

// SplitMCPToolName splits a native tool name of the form
// "mcp__<server>__<tool>" into its server and tool parts. ok is false if the
// name doesn't have the prefix, or either part would be empty — the caller
// falls back to treating it as an ordinary tool name.
func SplitMCPToolName(name string) (server, tool string, ok bool) {
	rest, found := strings.CutPrefix(name, mcpToolPrefix)
	if !found {
		return "", "", false
	}
	server, tool, found = strings.Cut(rest, "__")
	if !found || server == "" || tool == "" {
		return "", "", false
	}
	return server, tool, true
}
```

Then replace `CallsFromNative` (lines 78-106) with:

```go
func CallsFromNative(tcs []provider.ToolCall) []Call {
	out := make([]Call, 0, len(tcs))
	for i, tc := range tcs {
		c := Call{ID: tc.ID, Tool: tc.Name}
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		if server, tool, ok := SplitMCPToolName(tc.Name); ok {
			c.MCPServer, c.MCPTool = server, tool
			c.MCPArgs = tc.Arguments
			if strings.TrimSpace(c.MCPArgs) == "" {
				c.MCPArgs = "{}"
			}
			out = append(out, c)
			continue
		}
		var args nativeArgs
		if strings.TrimSpace(tc.Arguments) != "" {
			// A decode error leaves the fields empty; Execute reports what is
			// missing (e.g. "read_file needs a path") and the model retries.
			_ = json.Unmarshal([]byte(tc.Arguments), &args)
		}
		c.Path = args.Path
		switch tc.Name {
		case ToolWriteFile:
			c.Body = args.Content
		case ToolRunCommand:
			c.Body = args.Command
		case ToolWebSearch:
			c.Body = args.Query
			c.Max = args.MaxResults
		case ToolWebFetch:
			c.Path = args.URL
		}
		out = append(out, c)
	}
	return out
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/tools/... -v`
Expected: PASS — every test in the package, including the four new functions and the pre-existing `TestCallsFromNative` (whose `got[0] != tc.want` struct comparisons still compile: all `Call` fields, including the three new ones, remain `string`/`int` — comparable).

- [ ] **Step 7: Commit**

```bash
git add internal/tools/tools.go internal/tools/native.go internal/tools/native_test.go internal/tools/tools_test.go
git commit -m "$(cat <<'EOF'
feat(tools): add MCP call fields and mcp__server__tool naming helpers

Extends tools.Call with MCPServer/MCPTool/MCPArgs (kept as plain strings,
not json.RawMessage, so Call stays comparable with == — existing tests rely
on that). CallsFromNative now recognizes the mcp__<server>__<tool> native
tool-name convention and threads raw arguments through unparsed, since MCP
tool schemas are arbitrary and unknown to this package.
EOF
)"
```

---

### Task 2: `mcp.MockClient.CallTool` respects context

**Files:**
- Modify: `internal/mcp/mock.go`
- Test: `internal/mcp/mcp_test.go`

**Interfaces:**
- Consumes: none new.
- Produces: `MockClient.Delay time.Duration` field — Task 3's tests use it to simulate a hung MCP call.

- [ ] **Step 1: Write the failing test**

Add to `internal/mcp/mcp_test.go`:

```go
func TestMockClientCallToolRespectsContextTimeout(t *testing.T) {
	c := &MockClient{ServerName: "slow", Delay: 50 * time.Millisecond}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.CallTool(ctx, "slow_echo", json.RawMessage(`{}`))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error from a call that outlives its context")
	}
	if elapsed > 40*time.Millisecond {
		t.Errorf("CallTool took %s, want it to return promptly once ctx expires (well under the 50ms Delay)", elapsed)
	}
}
```

Check `internal/mcp/mcp_test.go`'s existing imports first (`context`, `encoding/json`, `time` are all already needed elsewhere in that file per its current tests — if any of the three is missing, add it to the `import` block).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/mcp/... -run TestMockClientCallToolRespectsContextTimeout -v`
Expected: FAIL — the call blocks for the full 50ms `Delay` (or `MockClient` has no `Delay` field, a compile error) because `CallTool` currently ignores `ctx` entirely.

- [ ] **Step 3: Add the `Delay` field and make `CallTool` respect `ctx`**

In `internal/mcp/mock.go`, replace the `MockClient` struct:

```go
// MockClient is an in-memory Client for tests and for exercising the registry
// without a real subprocess. It records lifecycle calls and returns canned
// tools and results.
type MockClient struct {
	ServerName  string
	CannedTools []Tool
	// CallFunc, if set, produces the result for CallTool; otherwise a simple
	// echo result is returned.
	CallFunc func(name string, input json.RawMessage) (Result, error)
	// ConnectErr, if set, makes Connect fail (to test error paths).
	ConnectErr error
	// Delay, if set, makes CallTool block for this long (or until ctx is
	// done, whichever comes first) before producing its result — lets tests
	// exercise real timeout/cancellation behavior instead of asserting it
	// structurally.
	Delay time.Duration

	mu        sync.Mutex
	connected bool
	closed    bool
}
```

And replace `CallTool`:

```go
// CallTool returns CallFunc's result or an echo, after respecting Delay
// against ctx.
func (m *MockClient) CallTool(ctx context.Context, name string, input json.RawMessage) (Result, error) {
	m.mu.Lock()
	connected := m.connected
	delay := m.Delay
	m.mu.Unlock()
	if !connected {
		return Result{}, fmt.Errorf("mock client not connected")
	}
	if delay > 0 {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if m.CallFunc != nil {
		return m.CallFunc(name, input)
	}
	return Result{Content: fmt.Sprintf("%s(%s)", name, string(input))}, nil
}
```

Add `"time"` to `internal/mcp/mock.go`'s import block (currently `context`, `encoding/json`, `fmt`, `sync`).

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/mcp/... -v`
Expected: PASS — every test in the package, including the new one.

- [ ] **Step 5: Commit**

```bash
git add internal/mcp/mock.go internal/mcp/mcp_test.go
git commit -m "$(cat <<'EOF'
test(mcp): make MockClient.CallTool respect context cancellation

Adds a Delay field so tests can simulate a hung MCP call and verify real
timeout/cancellation behavior end-to-end, instead of asserting it
structurally. Needed by the upcoming tool-calling wiring's timeout tests.
EOF
)"
```

---

### Task 3: `internal/tui/mcp_tools.go` — spec conversion, dispatch, catalog registration

**Files:**
- Create: `internal/tui/mcp_tools.go`
- Test: `internal/tui/mcp_tools_test.go`

**Interfaces:**
- Consumes: `mcp.Registry` (`List() []*mcp.Server`, `Get(name string) (*mcp.Server, bool)`, `CallTool(ctx, server, tool string, input json.RawMessage) (mcp.Result, error)`), `mcp.Server{Config mcp.ServerConfig, Status mcp.Status, Tools []mcp.Tool}`, `mcp.ServerConfig{Name string, Enabled bool, Timeout time.Duration, ApproveMode() string}`, `mcp.StatusConnected`, `mcp.ApproveAuto`; `tools.Call{MCPServer, MCPTool, MCPArgs string}`, `tools.JoinMCPToolName`, `tools.Result{Call tools.Call, Output string, Err error}`, `tools.Runner.Execute(tools.Call) tools.Result`, `tools.Registry.Register(tools.CapabilityInfo) error`, `tools.CapabilityInfo`, `tools.SafetyExternalMCP` (all from Task 1 and pre-existing code).
- Produces: `mcpToolSpecs(mcpReg *mcp.Registry) []provider.ToolSpec`, `containsMCPCall(calls []tools.Call) bool`, `mcpBatchNotice(calls []tools.Call) string`, `executeMCPCall(ctx context.Context, mcpReg *mcp.Registry, c tools.Call) tools.Result`, `runMixedToolBatch(ctx context.Context, runner *tools.Runner, mcpReg *mcp.Registry, calls []tools.Call) tea.Cmd`, `mcpToolResultsMsg{results []tools.Result}`, `registerMCPCapabilities(reg *tools.Registry, mcpReg *mcp.Registry)` — all consumed by Tasks 4, 5, 6, 7, 8.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/mcp_tools_test.go`:

```go
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
	c := tools.Call{ID: "call_1", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{"issue_key":"DEMO-1"}`}
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestMcpToolSpecs|TestExecuteMCPCall|TestContainsMCPCall|TestRunMixedToolBatch|TestRegisterMCPCapabilities' -v`
Expected: FAIL to compile — none of `mcpToolSpecs`, `executeMCPCall`, `containsMCPCall`, `runMixedToolBatch`, `mcpToolResultsMsg`, `registerMCPCapabilities` exist yet.

- [ ] **Step 3: Create `internal/tui/mcp_tools.go`**

```go
package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// defaultMCPTimeout bounds one MCP call when a server has no configured
// timeout (mirrors tools.Runner's own 30s default for run_command).
const defaultMCPTimeout = 30 * time.Second

// mcpToolResultsMsg carries the ordered results of an async tool batch that
// contained at least one MCP call (see runMixedToolBatch).
type mcpToolResultsMsg struct {
	results []tools.Result
}

// mcpToolSpecs converts every connected, enabled MCP server's tools into
// native function-calling specs, named "mcp__<server>__<tool>" so multiple
// servers can never collide on name. Returns nil if mcpReg is nil or no
// server is currently connected.
func mcpToolSpecs(mcpReg *mcp.Registry) []provider.ToolSpec {
	if mcpReg == nil {
		return nil
	}
	var out []provider.ToolSpec
	for _, srv := range mcpReg.List() {
		if srv.Status != mcp.StatusConnected || !srv.Config.Enabled {
			continue
		}
		for _, t := range srv.Tools {
			out = append(out, provider.ToolSpec{
				Name:        tools.JoinMCPToolName(srv.Config.Name, t.Name),
				Description: t.Description,
				Parameters:  t.Schema,
			})
		}
	}
	return out
}

// mcpServerTimeout resolves the bounded timeout for one server's calls,
// falling back to defaultMCPTimeout when unset or the server is unknown.
func mcpServerTimeout(mcpReg *mcp.Registry, server string) time.Duration {
	if mcpReg == nil {
		return defaultMCPTimeout
	}
	srv, ok := mcpReg.Get(server)
	if !ok || srv.Config.Timeout <= 0 {
		return defaultMCPTimeout
	}
	return srv.Config.Timeout
}

// executeMCPCall runs one MCP call with a bounded timeout, converting the
// result (or any error) into a tools.Result. It never panics: an unknown
// server, a disconnected server, malformed arguments, a timeout, or a
// cancellation all land in Result.Err so the model can see what happened
// and retry, matching the native tools' "the model sees the problem" style.
func executeMCPCall(ctx context.Context, mcpReg *mcp.Registry, c tools.Call) tools.Result {
	res := tools.Result{Call: c}
	if mcpReg == nil {
		res.Err = fmt.Errorf("mcp server %q: MCP is not available", c.MCPServer)
		return res
	}
	timeout := mcpServerTimeout(mcpReg, c.MCPServer)
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := c.MCPArgs
	if args == "" {
		args = "{}"
	}
	out, err := mcpReg.CallTool(callCtx, c.MCPServer, c.MCPTool, []byte(args))
	if err != nil {
		switch {
		case errors.Is(callCtx.Err(), context.DeadlineExceeded):
			res.Err = fmt.Errorf("mcp %s.%s timed out after %s", c.MCPServer, c.MCPTool, timeout)
		case errors.Is(ctx.Err(), context.Canceled):
			res.Err = fmt.Errorf("mcp %s.%s cancelled by the user", c.MCPServer, c.MCPTool)
		default:
			res.Err = err
		}
		return res
	}
	res.Output = out.Content
	if out.IsError {
		res.Err = fmt.Errorf("%s", out.Content)
	}
	return res
}

// containsMCPCall reports whether any call in the batch targets an MCP
// server — the signal runToolCalls (Task 7) uses to decide between the
// unchanged synchronous native path and the new async path.
func containsMCPCall(calls []tools.Call) bool {
	for _, c := range calls {
		if c.MCPServer != "" {
			return true
		}
	}
	return false
}

// mcpBatchNotice names the first MCP call in a batch, so the UI shows what
// it's waiting on instead of looking frozen while the async command runs.
func mcpBatchNotice(calls []tools.Call) string {
	for _, c := range calls {
		if c.MCPServer != "" {
			return fmt.Sprintf("⚒ running %s: %s…", c.MCPServer, c.MCPTool)
		}
	}
	return "⚒ running tool call(s)…"
}

// runMixedToolBatch executes a batch that contains at least one MCP call as
// a single async tea.Cmd: every call in the batch runs sequentially, in
// order — including the native ones, so ordering is never split across
// messages — because MCP servers commonly serialize session state
// (jiraWorklog sets allow_parallel: false) and the latency cost of
// sequential execution is negligible next to model-inference time for the
// handful of calls a typical turn makes.
func runMixedToolBatch(ctx context.Context, runner *tools.Runner, mcpReg *mcp.Registry, calls []tools.Call) tea.Cmd {
	return func() tea.Msg {
		results := make([]tools.Result, 0, len(calls))
		for _, c := range calls {
			if c.MCPServer != "" {
				results = append(results, executeMCPCall(ctx, mcpReg, c))
				continue
			}
			results = append(results, runner.Execute(c))
		}
		return mcpToolResultsMsg{results: results}
	}
}

// registerMCPCapabilities adds every connected server's tools into reg so
// /tools list and /tools inspect show them alongside native and web tools —
// the seam internal/tools/registry.go's own DefaultRegistry comment already
// anticipated. Source is "mcp:<server>" so /tools list <filter> can match
// either "mcp" (every server) or one server's exact name.
func registerMCPCapabilities(reg *tools.Registry, mcpReg *mcp.Registry) {
	if mcpReg == nil {
		return
	}
	for _, srv := range mcpReg.List() {
		if srv.Status != mcp.StatusConnected {
			continue
		}
		for _, t := range srv.Tools {
			_ = reg.Register(tools.CapabilityInfo{
				Name:        tools.JoinMCPToolName(srv.Config.Name, t.Name),
				Description: t.Description,
				Source:      "mcp:" + srv.Config.Name,
				Safety:      tools.SafetyExternalMCP,
				Approval:    srv.Config.ApproveMode(),
				Parameters:  t.Schema,
			})
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -run 'TestMcpToolSpecs|TestExecuteMCPCall|TestContainsMCPCall|TestRunMixedToolBatch|TestRegisterMCPCapabilities' -v`
Expected: PASS — all new tests.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/mcp_tools.go internal/tui/mcp_tools_test.go
git commit -m "$(cat <<'EOF'
feat(tui): add MCP tool-spec conversion, dispatch, and catalog registration

New internal/tui/mcp_tools.go is the sole file importing both internal/tools
and internal/mcp. Connected servers' tools convert to native ToolSpecs named
mcp__<server>__<tool>; execution is bounded by ServerConfig.Timeout and
never panics on an unknown/disconnected server, malformed args, a timeout,
or cancellation. Not yet wired into the request/approval/execution loop —
that's Tasks 4-7.
EOF
)"
```

---

### Task 4: Offer MCP tools to the model (`buildRequest`)

**Files:**
- Modify: `internal/tui/pipeline.go:349-367` (`buildRequest`)
- Test: `internal/tui/toolloop_test.go`

**Interfaces:**
- Consumes: `mcpToolSpecs` (Task 3), `m.useNativeTools()`, `m.mcpRegistry` (pre-existing `Model` field).
- Produces: `m.activeToolSpecs() []provider.ToolSpec` — used by Task 5's cache-key fix too.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/toolloop_test.go`:

```go
func TestBuildRequestIncludesConnectedMCPTools(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.mcpRegistry = newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Description: "start a session", Schema: json.RawMessage(`{"type":"object"}`)},
	}, nil)

	req := m.buildRequest(nil)
	found := false
	for _, spec := range req.Tools {
		if spec.Name == "mcp__jiraWorklog__session_start" {
			found = true
		}
	}
	if !found {
		t.Fatalf("req.Tools = %+v, missing the connected MCP server's tool", req.Tools)
	}
}

func TestBuildRequestOmitsMCPToolsWhenToolsOff(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = false
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.mcpRegistry = newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Schema: json.RawMessage(`{"type":"object"}`)},
	}, nil)

	req := m.buildRequest(nil)
	if len(req.Tools) != 0 {
		t.Errorf("req.Tools = %+v, want empty when /tools is off", req.Tools)
	}
}
```

Add `"encoding/json"` and `"github.com/patrikcze/llmtui/internal/mcp"` to `internal/tui/toolloop_test.go`'s import block if not already present.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestBuildRequestIncludesConnectedMCPTools|TestBuildRequestOmitsMCPToolsWhenToolsOff' -v`
Expected: FAIL — `req.Tools` doesn't include the MCP tool yet (first test); second test should already pass trivially but run it anyway to confirm no regression before the change.

- [ ] **Step 3: Add `activeToolSpecs` and use it in `buildRequest`**

In `internal/tui/pipeline.go`, replace `buildRequest` (lines 349-367):

```go
// activeToolSpecs assembles every tool spec offered to the model under
// current settings: native workspace tools, web tools, and connected MCP
// servers' tools. buildRequest and cacheKey (Task 5) both call this so a
// request and its cache key can never disagree about which tools were
// actually offered.
func (m *Model) activeToolSpecs() []provider.ToolSpec {
	if !m.useNativeTools() {
		return nil
	}
	specs := tools.Specs()
	if m.webOn {
		specs = append(specs, tools.WebSpecs()...)
	}
	specs = append(specs, mcpToolSpecs(m.mcpRegistry)...)
	return specs
}

// buildRequest assembles a ChatRequest for the given messages under the
// current settings, offering native tool specs when enabled.
func (m *Model) buildRequest(messages []provider.Message) provider.ChatRequest {
	return provider.ChatRequest{
		Model:       m.model,
		Messages:    messages,
		Temperature: m.effectiveTemperature(),
		TopP:        m.cfg.Chat.TopP,
		MaxTokens:   m.cfg.Chat.MaxTokens,
		Stream:      m.cfg.StreamEnabled(),
		Tools:       m.activeToolSpecs(),
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS — the whole package, including the two new tests and every pre-existing test (in particular `TestComposeInjectsToolInstructions`, `TestCacheKeyChangesWithToolState`, and every `toolloop_test.go` test — `buildRequest`'s native/web behavior is unchanged, just refactored through `activeToolSpecs`).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/pipeline.go internal/tui/toolloop_test.go
git commit -m "$(cat <<'EOF'
feat(tui): offer connected MCP servers' tools to the model

buildRequest now merges native, web, and MCP tool specs through one shared
activeToolSpecs() helper, gated by the existing /tools on switch — no new
toggle. This is the change that makes a connected server's tools (e.g.
jiraWorklog) actually visible to native function calling for the first
time; execution wiring follows in later tasks.
EOF
)"
```

---

### Task 5: Cache-key fix — hash the active tool set

**Files:**
- Modify: `internal/cache/cache.go:30-63` (`Key` struct and `Hash`)
- Modify: `internal/tui/pipeline.go` (add `toolSpecsFingerprint`, use it in `cacheKey`)
- Test: `internal/cache/cache_test.go`, `internal/tui/toolloop_test.go`

**Interfaces:**
- Consumes: `m.activeToolSpecs()` (Task 4).
- Produces: `cache.Key.ToolsHash string`; `toolSpecsFingerprint(specs []provider.ToolSpec) string`.

- [ ] **Step 1: Write the failing tests**

`internal/cache/cache_test.go` already has `TestKeyHashStability`, which checks "every field participates in the hash" via a `variants` table (see `testKey()` and the `variants := []func(*Key){...}` slice near the top of the file). Add one more entry to that existing slice, right after the `HistoryHash` variant:

```go
		func(k *Key) { k.HistoryHash = "different" },
		func(k *Key) { k.ToolsHash = "different" },
	}
```

(That's the only change to this file — the surrounding table-driven test body already exercises every entry generically.)

Add to `internal/tui/toolloop_test.go`:

```go
// TestCacheKeyChangesWithConnectedMCPServer guards the specific gap this
// project's cache-key-completeness invariant calls out: connecting an MCP
// server changes what's actually sent to the provider (req.Tools grows) even
// though the user message, history, and toolsOn/native state are unchanged
// — a request built before connecting must not be served the response
// cached for a request built after.
func TestCacheKeyChangesWithConnectedMCPServer(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	factory := func(c mcp.ServerConfig) (mcp.Client, error) { return &mcp.MockClient{ServerName: c.Name}, nil }
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: time.Second,
	}}, factory)
	keyDisconnected := m.cacheKey("hi", nil)

	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	keyConnected := m.cacheKey("hi", nil)

	if keyDisconnected.Hash() == keyConnected.Hash() {
		t.Error("cache key must differ once an MCP server connects, even with the same message, history, and tools-on state")
	}
}
```

Add `"context"`, `"time"`, and `"github.com/patrikcze/llmtui/internal/mcp"` to `internal/tui/toolloop_test.go`'s import block if not already present (this file already imports `os`, `path/filepath`, `strings`, `testing`, `tea`, `provider`, `tools` per Task 3/4's additions — add only what's missing).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cache/... ./internal/tui/... -run 'TestKeyHashStability|TestCacheKeyChangesWithConnectedMCPServer' -v`
Expected: FAIL — `Key` has no field `ToolsHash` (compile error); once that's visible, the second test fails because both keys hash identically (nothing about MCP connection state feeds the key yet).

- [ ] **Step 3: Add `ToolsHash` to `cache.Key`**

In `internal/cache/cache.go`, replace the `Key` struct and `Hash` method (lines 30-63):

```go
type Key struct {
	Provider     string
	BaseURL      string
	Model        string
	UserMessage  string
	SystemPrompt string
	PromptMode   string
	Template     string
	Temperature  float64
	TopP         float64
	MaxTokens    int
	HistoryHash  string
	// ToolsHash fingerprints the tool specs actually offered to the model
	// (native, web, and MCP) — connecting or disconnecting an MCP server
	// changes what's sent to the provider even when nothing else about the
	// request changes, and a cache hit must not straddle that difference.
	ToolsHash string
}

// Hash returns a stable content hash for the key. Free-text fields are
// hashed individually so the canonical string cannot be confused by
// separator characters inside them.
func (k Key) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "v3|%s|%s|%s|%s|%s|%s|%s|%.4f|%.4f|%d|%s|%s",
		k.Provider,
		hashText(k.BaseURL),
		k.Model,
		hashText(k.UserMessage),
		hashText(k.SystemPrompt),
		k.PromptMode,
		k.Template,
		k.Temperature,
		k.TopP,
		k.MaxTokens,
		k.HistoryHash,
		k.ToolsHash,
	)
	return hex.EncodeToString(h.Sum(nil))
}
```

(Bumped the version prefix from `v2` to `v3`: entries written under the old key shape simply become unreachable rather than risking any collision with the new field.)

- [ ] **Step 4: Add `toolSpecsFingerprint` and use it in `cacheKey`**

In `internal/tui/pipeline.go`, add `"sort"` to the import block (currently `context`, `crypto/sha256`, `encoding/hex`, `fmt`, `strings`, `time`).

Add, right after `historyFingerprint` (after line 276):

```go
// toolSpecsFingerprint hashes the active tool set so the cache key changes
// whenever which tools are actually offered to the model changes — e.g.
// connecting or disconnecting an MCP server — even though nothing else
// about the request changed. Specs are sorted by name first: server/tool
// listing order isn't guaranteed to be stable across connects.
func toolSpecsFingerprint(specs []provider.ToolSpec) string {
	sorted := make([]provider.ToolSpec, len(specs))
	copy(sorted, specs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	for _, s := range sorted {
		h.Write([]byte(s.Name))
		h.Write([]byte{0})
		h.Write([]byte(s.Description))
		h.Write([]byte{0})
		h.Write(s.Parameters)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

Then in `cacheKey` (lines 241-261), add `ToolsHash` to the returned `cache.Key`:

```go
func (m *Model) cacheKey(raw string, images []provider.Image) cache.Key {
	_, pc, _ := m.cfg.ActiveProvider()
	composed, _ := m.compose(raw, images, true)
	systemPrompt := m.cfg.Chat.SystemPrompt
	if len(composed.Messages) > 0 && composed.Messages[0].Role == provider.RoleSystem {
		systemPrompt = composed.Messages[0].Content
	}
	return cache.Key{
		Provider:     m.prov.Name(),
		BaseURL:      pc.BaseURL,
		Model:        m.model,
		UserMessage:  raw,
		SystemPrompt: systemPrompt,
		PromptMode:   m.effectivePromptMode(),
		Template:     m.template,
		Temperature:  m.effectiveTemperature(),
		TopP:         m.cfg.Chat.TopP,
		MaxTokens:    m.cfg.Chat.MaxTokens,
		HistoryHash:  historyFingerprint(m.session.Messages),
		ToolsHash:    toolSpecsFingerprint(m.activeToolSpecs()),
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/cache/... ./internal/tui/... -v`
Expected: PASS — both packages in full, including the pre-existing `TestCacheKeyChangesWithToolState` (still passes: that test varies `toolsOn` itself, which still changes `SystemPrompt` exactly as before, plus now also changes `ToolsHash` since `activeToolSpecs()` returns `nil` when tools are off).

- [ ] **Step 6: Commit**

```bash
git add internal/cache/cache.go internal/tui/pipeline.go internal/cache/cache_test.go internal/tui/toolloop_test.go
git commit -m "$(cat <<'EOF'
fix(cache): fingerprint the active tool set into the cache key

cache.Key had no field for which tools were actually offered to the model.
Connecting or disconnecting an MCP server now changes ToolsHash (sorted,
content-hashed from the same activeToolSpecs() buildRequest uses), so a
cached response from one tool set can never be served under a materially
different one. Bumped the hash version prefix v2->v3 so old entries become
unreachable rather than risking any collision.
EOF
)"
```

---

### Task 6: Approval — `mcpAutoApprove` and per-server gating

**Files:**
- Modify: `internal/tui/app.go` (`Model` struct, `startToolBatch`, `resolveApproval`; new `callNeedsApproval`)
- Test: `internal/tui/toolloop_test.go`

**Interfaces:**
- Consumes: `tools.Call.MCPServer`, `m.mcpRegistry.Get`, `mcp.ServerConfig.ApproveMode()`, `mcp.ApproveAuto` (all pre-existing or from Task 1/3).
- Produces: `Model.mcpAutoApprove bool`; `m.callNeedsApproval(c tools.Call) bool` — used by Task 7's async path too (a batch must still be approval-gated before it runs, sync or async).

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/toolloop_test.go`:

```go
func TestMCPCallNeedsApprovalPerServerConfig(t *testing.T) {
	m := newTestModel(t)
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) { return &mcp.MockClient{ServerName: c.Name}, nil }
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "ask-server", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "ask", Timeout: time.Second},
		{Name: "auto-server", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "auto", Timeout: time.Second},
	}, factory)

	askCall := tools.Call{MCPServer: "ask-server", MCPTool: "t"}
	autoCall := tools.Call{MCPServer: "auto-server", MCPTool: "t"}
	if !m.callNeedsApproval(askCall) {
		t.Error("ask-server call should need approval")
	}
	if m.callNeedsApproval(autoCall) {
		t.Error("auto-server call should not need approval")
	}
}

func TestApprovalAlwaysScopesToBatchKinds(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) { return &mcp.MockClient{ServerName: c.Name}, nil }
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "srv", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "ask", Timeout: time.Second},
	}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "srv"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// A pure-native batch: "Always" must not grant mcpAutoApprove.
	m.pendingCalls = []tools.Call{{ID: "c1", Tool: tools.ToolWriteFile, Path: "a.txt", Body: "x"}}
	m.resolveApproval(approvalAlways)
	if !m.toolsAutoApprove {
		t.Error("native Always did not set toolsAutoApprove")
	}
	if m.mcpAutoApprove {
		t.Error("native-only Always must not also grant mcpAutoApprove")
	}

	// Reset and check the reverse: a pure-MCP batch must not grant toolsAutoApprove.
	m.toolsAutoApprove = false
	m.mcpAutoApprove = false
	m.pendingCalls = []tools.Call{{ID: "c2", MCPServer: "srv", MCPTool: "t", MCPArgs: "{}"}}
	m.resolveApproval(approvalAlways)
	if m.toolsAutoApprove {
		t.Error("mcp-only Always must not also grant toolsAutoApprove")
	}
	if !m.mcpAutoApprove {
		t.Error("mcp Always did not set mcpAutoApprove")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestMCPCallNeedsApprovalPerServerConfig|TestApprovalAlwaysScopesToBatchKinds' -v`
Expected: FAIL to compile — `m.callNeedsApproval` and `m.mcpAutoApprove` don't exist yet.

- [ ] **Step 3: Add the `mcpAutoApprove` field**

In `internal/tui/app.go`, in the `Model` struct's workspace-tools block (around line 128-131, right after `toolsAutoApprove bool`):

```go
	toolsOn          bool
	toolsAutoApprove bool // "auto" approval mode: skip the y/n prompt
	// mcpAutoApprove is a separate "always" state for MCP calls, kept apart
	// from toolsAutoApprove: choosing "Always" on a workspace-tool prompt
	// (e.g. write_file) must never silently start auto-approving an MCP
	// call with real external side effects (e.g. jiraWorklog's
	// worklog_submit), and vice versa.
	mcpAutoApprove   bool
	toolsNative      bool // offer tools via native function calling
```

- [ ] **Step 4: Add `callNeedsApproval` and use it in `startToolBatch`**

In `internal/tui/app.go`, add right before `startToolBatch` (before line 693):

```go
// callNeedsApproval reports whether one call must be confirmed before it
// runs. Native calls keep the workspace Runner's existing policy exactly
// (including toolsAutoApprove). MCP calls are gated per-server by
// ServerConfig.Approve ("ask"|"auto"), or skip entirely once mcpAutoApprove
// has been granted for this session — a state kept separate from
// toolsAutoApprove (see the Model.mcpAutoApprove comment).
func (m *Model) callNeedsApproval(c tools.Call) bool {
	if c.MCPServer == "" {
		if m.toolsAutoApprove {
			return false
		}
		return m.toolRunner.NeedsApproval(c)
	}
	if m.mcpAutoApprove {
		return false
	}
	srv, ok := m.mcpRegistry.Get(c.MCPServer)
	return !ok || srv.Config.ApproveMode() != mcp.ApproveAuto
}
```

Then replace the approval-check block inside `startToolBatch` (lines 715-725):

```go
	for _, c := range calls {
		if m.callNeedsApproval(c) {
			m.overlayOpen = false
			m.pendingCalls = calls
			m.approvalIdx = 0
			m.refreshViewport()
			return nil
		}
	}
	return m.runToolCalls(calls)
```

(This replaces the old `if !m.toolsAutoApprove { for _, c := range calls { if m.toolRunner.NeedsApproval(c) {...} } }` block — behaviorally identical for pure-native batches, since `callNeedsApproval` checks `toolsAutoApprove` first for those; now also correct for MCP calls.)

- [ ] **Step 5: Scope "Always" to the batch's call kinds**

In `internal/tui/app.go`, replace the `approvalAlways` case in `resolveApproval` (lines 872-877):

```go
	case approvalAlways:
		hasNative, hasMCP := false, false
		for _, c := range m.pendingCalls {
			if c.MCPServer == "" {
				hasNative = true
			} else {
				hasMCP = true
			}
		}
		var granted []string
		if hasNative {
			m.toolsAutoApprove = true
			granted = append(granted, "workspace")
		}
		if hasMCP {
			m.mcpAutoApprove = true
			granted = append(granted, "mcp")
		}
		calls := m.pendingCalls
		m.pendingCalls = nil
		m.notice = fmt.Sprintf("⚒ tool approvals set to auto (%s) for this session (/tools ask to revert)", strings.Join(granted, " + "))
		return m.runToolCalls(calls)
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS — the whole package. In particular, every pre-existing `toolloop_test.go` approval test (`TestMaybeRunToolsAsksBeforeWriting`, `TestMaybeRunToolsAutoApproveSkipsPrompt`, `TestNativeToolCallsRespectApproval`, etc.) must still pass unchanged, since `callNeedsApproval`'s native branch preserves the exact prior behavior.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/app.go internal/tui/toolloop_test.go
git commit -m "$(cat <<'EOF'
feat(tui): gate MCP tool calls with a separate approval state

Adds mcpAutoApprove, independent of the existing toolsAutoApprove: choosing
"Always" on a workspace-tool prompt can no longer silently start
auto-approving an MCP call with real external side effects (e.g.
jiraWorklog's worklog_submit), and vice versa. Per-call gating for MCP
calls reuses ServerConfig.Approve ("ask"|"auto"), previously computed but
never actually consulted anywhere.
EOF
)"
```

---

### Task 7: Async execution for batches containing an MCP call

**Files:**
- Modify: `internal/tui/app.go` (`Model` struct, message types, `Update`, `runToolCalls`, `handleCtrlC`, the `Esc` key case)
- Test: `internal/tui/toolloop_test.go`

**Interfaces:**
- Consumes: `containsMCPCall`, `mcpBatchNotice`, `runMixedToolBatch`, `mcpToolResultsMsg` (Task 3).
- Produces: `Model.mcpBatchCancel context.CancelFunc` — nothing downstream depends on it; this is the terminal task in the execution chain.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/toolloop_test.go`:

```go
func TestPureNativeBatchStaysSynchronous(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	withToolReply(m, "```tool write_file a.txt\ndata\n```")

	// Unchanged behavior: the write already happened by the time
	// maybeRunTools returns, before its returned cmd is ever invoked.
	cmd := m.maybeRunTools()
	if cmd == nil {
		t.Fatal("expected a follow-up dispatch command")
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("pure-native batch must still execute synchronously: %v", err)
	}
}

func TestMixedBatchRunsAsyncAndDeliversResults(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.mcpAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, CallFunc: func(name string, input json.RawMessage) (mcp.Result, error) {
			return mcp.Result{Content: "session started"}, nil
		}}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	m.session.AddUser("start a session")
	m.thinking = true

	done := provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{
		{ID: "call_1", Name: "mcp__jiraWorklog__session_start", Arguments: `{"issue_key":"DEMO-1"}`},
	}}
	_, cmd := m.handleStreamEvent(streamEventMsg{event: done, ok: true})
	if cmd == nil {
		t.Fatal("expected an async command for the MCP call")
	}
	// Nothing has executed yet — this is the async path.
	if m.toolOK != 0 {
		t.Fatalf("toolOK = %d before the async command ran, want 0", m.toolOK)
	}

	msg := cmd()
	resultsMsg, ok := msg.(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want mcpToolResultsMsg", msg)
	}
	_, cmd2 := m.Update(resultsMsg)
	if cmd2 == nil {
		t.Fatal("expected a continuation command after the results arrive")
	}
	if m.toolOK != 1 {
		t.Errorf("toolOK = %d, want 1", m.toolOK)
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || last.Content != "session started" {
		t.Errorf("tool result message = %+v", last)
	}
}

func TestMCPBatchCancelViaEsc(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.mcpAutoApprove = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, Delay: time.Second}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: 5 * time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	cmd := m.startToolBatch([]tools.Call{{ID: "c1", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: "{}"}})
	if cmd == nil || m.mcpBatchCancel == nil {
		t.Fatal("expected an in-flight async batch with a cancel func set")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.mcpBatchCancel != nil {
		t.Error("Esc should have cleared mcpBatchCancel")
	}
	if m.errText == "" {
		t.Error("Esc should report the cancellation")
	}

	// The already-dispatched command still completes (the goroutine isn't
	// killed, just its context) and reports the cancellation as an error.
	msg := cmd()
	resultsMsg, ok := msg.(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want mcpToolResultsMsg", msg)
	}
	if len(resultsMsg.results) != 1 || resultsMsg.results[0].Err == nil {
		t.Fatalf("results = %+v, want one cancelled result", resultsMsg.results)
	}
}
```

Add `"encoding/json"` to `internal/tui/toolloop_test.go`'s imports if not already present from Task 4/5.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestPureNativeBatchStaysSynchronous|TestMixedBatchRunsAsyncAndDeliversResults|TestMCPBatchCancelViaEsc' -v`
Expected: `TestPureNativeBatchStaysSynchronous` PASSES already (no regression yet, confirms the baseline). The other two FAIL — `mcpBatchCancel` doesn't exist, native tool calls with an `mcp__`-prefixed name currently execute through the unmodified synchronous `runToolCalls` and fail with "unknown tool", and `Esc` doesn't know about any MCP batch.

- [ ] **Step 3: Add `mcpBatchCancel` and the `mcpToolResultsMsg` case**

In `internal/tui/app.go`, in the MCP block of the `Model` struct (around line 150-151) — this is a different location in the struct from `mcpAutoApprove`, which Task 6 already placed next to `toolsAutoApprove`; this step touches only the `mcpRegistry` line:

```go
	// Optional MCP servers (config/interfaces only; no transport wired yet).
	mcpRegistry    *mcp.Registry
	mcpBatchCancel context.CancelFunc // cancels an in-flight async MCP tool batch, if any
```

Add the message-handling case in `Update`'s big `switch msg := msg.(type)` block, right after the existing `case mcpConnectMsg:` block (after line 581):

```go
	case mcpToolResultsMsg:
		m.mcpBatchCancel = nil
		for _, res := range msg.results {
			if res.Err != nil {
				m.toolErr++
			} else {
				m.toolOK++
			}
		}
		m.notice = fmt.Sprintf("⚒ ran %d tool call(s) — round %d/%d", len(msg.results), m.toolDepth, m.toolMaxIter())
		return m, m.sendToolResults(msg.results)
```

- [ ] **Step 4: Split `runToolCalls` into sync/async paths**

In `internal/tui/app.go`, replace `runToolCalls` (lines 742-757):

```go
// runToolCalls executes an approved batch and feeds the results back.
// Pure-native batches run synchronously, exactly as before. A batch
// containing any MCP call runs asynchronously instead: an MCP call is a
// subprocess round-trip that can itself block on a network service (unlike
// a local file op), so it must not freeze the UI for however long that
// takes.
func (m *Model) runToolCalls(calls []tools.Call) tea.Cmd {
	m.toolDepth++
	if !containsMCPCall(calls) {
		results := make([]tools.Result, 0, len(calls))
		for _, c := range calls {
			res := m.toolRunner.Execute(c)
			if res.Err != nil {
				m.toolErr++
			} else {
				m.toolOK++
			}
			results = append(results, res)
		}
		m.notice = fmt.Sprintf("⚒ ran %d tool call(s) — round %d/%d", len(calls), m.toolDepth, m.toolMaxIter())
		return m.sendToolResults(results)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.mcpBatchCancel = cancel
	m.notice = mcpBatchNotice(calls)
	return runMixedToolBatch(ctx, m.toolRunner, m.mcpRegistry, calls)
}
```

- [ ] **Step 5: Wire cancellation into Esc and Ctrl+C**

In `internal/tui/app.go`, replace the `tea.KeyEsc` case inside `Update` (lines 481-493):

```go
		case tea.KeyEsc:
			if m.thinking && m.cancelStream != nil {
				// Stop generation, keeping the partial reply.
				m.cancelStream()
				m.finishStream(nil)
				m.errText = "generation stopped"
				m.refreshViewport()
			} else if m.mcpBatchCancel != nil {
				m.mcpBatchCancel()
				m.mcpBatchCancel = nil
				m.errText = "mcp tool batch cancelled"
				m.refreshViewport()
			} else if strings.HasPrefix(m.input.Value(), "/") {
				m.input.Reset()
				m.updateSuggestions()
				m.syncInputHeight()
			}
			return m, nil
```

Replace `handleCtrlC` (lines 986-1007):

```go
func (m *Model) handleCtrlC() (tea.Model, tea.Cmd) {
	if time.Since(m.ctrlCAt) < ctrlCWindow {
		return m, m.quit()
	}
	m.ctrlCAt = time.Now()
	switch {
	case m.thinking && m.cancelStream != nil:
		m.cancelStream()
		m.finishStream(nil)
		m.errText = "generation stopped"
		m.notice = "press ctrl+c again to exit"
		m.refreshViewport()
	case m.mcpBatchCancel != nil:
		m.mcpBatchCancel()
		m.mcpBatchCancel = nil
		m.errText = "mcp tool batch cancelled"
		m.notice = "press ctrl+c again to exit"
		m.refreshViewport()
	case m.input.Value() != "":
		m.input.Reset()
		m.updateSuggestions()
		m.syncInputHeight()
		m.notice = "input cleared — press ctrl+c again to exit"
	default:
		m.notice = "press ctrl+c again to exit (session auto-saves)"
	}
	return m, nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS — the entire package, including all three new tests and every pre-existing test (in particular `TestNativeToolCallsExecuteAndContinue`, `TestNativeToolCallsRespectApproval`, and every other `toolloop_test.go`/`app_test.go`/`bugfix_test.go` test — none of them exercise an MCP call, so they all take the unchanged synchronous branch).

- [ ] **Step 7: Commit**

```bash
git add internal/tui/app.go internal/tui/toolloop_test.go
git commit -m "$(cat <<'EOF'
feat(tui): run MCP tool batches asynchronously with a bounded, cancellable timeout

runToolCalls now branches: a pure-native batch is byte-for-byte the same
synchronous path as before; a batch containing any MCP call runs as a
tea.Cmd instead, bounded by ServerConfig.Timeout (previously stored but
never enforced anywhere) and cancellable via Esc/Ctrl+C the same way an
in-flight streaming response already is. This is the final piece that makes
a connected MCP server's tools actually callable end-to-end by the model.
EOF
)"
```

---

### Task 8: `/tools list` and `/tools inspect` show connected MCP servers

**Files:**
- Modify: `internal/tui/commands_local.go:1508-1543` (`toolsListOverlay`), `internal/tui/commands_local.go:1546-1575` (`toolsInspectOverlay`)
- Test: `internal/tui/mcp_tools_test.go`

**Interfaces:**
- Consumes: `registerMCPCapabilities` (Task 3).
- Produces: nothing new — this is a leaf/UI task.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/mcp_tools_test.go`:

```go
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestToolsList|TestToolsInspectShowsMCPToolParameters' -v`
Expected: FAIL — `TestToolsListShowsConnectedMCPServer` and `TestToolsInspectShowsMCPToolParameters` fail (the connected server's tool isn't registered into `tools.DefaultRegistry()` yet). `TestToolsListOmitsUnconnectedMCPServer` passes trivially already — run it anyway to confirm no regression.

- [ ] **Step 3: Wire `registerMCPCapabilities` into both overlays**

In `internal/tui/commands_local.go`, in `toolsListOverlay` (replace lines 1508-1522):

```go
// toolsListOverlay renders the /tools list output: one row per capability
// with source, safety class, and approval policy.
func (m *Model) toolsListOverlay(args string) string {
	reg := tools.DefaultRegistry()
	registerMCPCapabilities(reg, m.mcpRegistry)
	filter := strings.TrimSpace(args)
	caps := reg.List()

	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("capability registry") + "\n\n")
	b.WriteString(m.theme.UserLabel.Render(
		fmt.Sprintf("%-16s %-8s %-18s %-8s %s", "name", "source", "safety", "enabled", "approval"),
	) + "\n")

	enabledSources := map[string]bool{
		"builtin": m.toolsOn,
		"web":     m.toolsOn && m.webOn,
	}
	if m.mcpRegistry != nil {
		for _, srv := range m.mcpRegistry.List() {
			if srv.Status == mcp.StatusConnected {
				enabledSources["mcp:"+srv.Config.Name] = m.toolsOn && m.useNativeTools()
			}
		}
	}
```

(The rest of the function — the `for _, c := range caps` loop and everything after — is unchanged.)

In `toolsInspectOverlay` (replace lines 1546-1555, the opening of the function up to the `reg := tools.DefaultRegistry()` line):

```go
// toolsInspectOverlay renders detailed info for a single capability.
func (m *Model) toolsInspectOverlay(name string) string {
	var b strings.Builder
	if name == "" {
		b.WriteString(m.theme.Badge.Render("usage") + "\n\n")
		b.WriteString("  /tools inspect <name>\n\n")
		b.WriteString(m.theme.SystemNote.Render("run /tools list to see available names") + "\n")
		return m.overlayFooter(&b)
	}
	reg := tools.DefaultRegistry()
	registerMCPCapabilities(reg, m.mcpRegistry)
	info, ok := reg.Get(name)
```

(Everything after `info, ok := reg.Get(name)` is unchanged.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/... -v`
Expected: PASS — the whole package.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/commands_local.go internal/tui/mcp_tools_test.go
git commit -m "$(cat <<'EOF'
feat(tui): show connected MCP servers' tools in /tools list and inspect

Fulfills the seam internal/tools/registry.go's own DefaultRegistry comment
already anticipated: /mcp-connected tools now register into the same
capability catalog native and web tools use, filterable by "mcp" (every
server) or one server's exact name via /tools list <filter>.
EOF
)"
```

---

### Task 9: Documentation

**Files:**
- Modify: `docs/mcp.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Add a "Tool calling" section**

In `docs/mcp.md`, insert a new section after the existing "## Configuration" section and before "## Safety" (i.e. after the closing ``` of the YAML example, before the `## Safety` heading):

```markdown
## Tool calling

Once a server is connected (`/mcp connect <server>`), its tools are offered
to the model automatically whenever `/tools on` — the same switch native
workspace tools use. There is no separate toggle: `/mcp connect` plus
`/tools on` is enough.

Each tool is exposed to the model as `mcp__<server>__<tool>` (e.g.
`mcp__jiraWorklog__session_start`), so tools from different servers can
never collide by name. This is **native function-calling only** — a model
without native tool-calling support won't see MCP tools, the same as today.

**Approval.** MCP calls have their own "always approve" state, kept
separate from the workspace-tools one: accepting "Always" on a file write
never silently starts auto-approving an MCP call with real external side
effects (e.g. submitting a Jira worklog), and vice versa. Per-call approval
is otherwise governed by each server's own `approve: ask | auto` config.

**Execution.** Unlike native tools (which run synchronously), a batch
containing any MCP call runs asynchronously and is bounded by that server's
`timeout` (default 30s) — an MCP call is a subprocess round-trip that can
itself block on a real network service, and must not freeze the UI. Press
Esc or Ctrl+C to cancel an in-flight batch, the same as an in-flight
streaming response.

A timeout means *llmtui* gave up waiting — it does not mean the server
rolled anything back. A slow `session_start` may still have created a
session on the server's side even though the timeout fired locally; check
the server's own state (e.g. `/mcp inspect <server>`, or the server's own
tools) if that matters for what you're doing.
```

- [ ] **Step 2: Verify the doc renders sensibly**

Run: `cat docs/mcp.md` and read it top to bottom — confirm the new section sits between "Configuration" and "Safety", uses the same heading level (`##`) as its siblings, and doesn't duplicate anything already stated in "Safety" below it.

- [ ] **Step 3: Commit**

```bash
git add docs/mcp.md
git commit -m "docs(mcp): document tool-calling exposure, approval, and execution model"
```

---

### Task 10: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Format**

Run: `go fmt ./...`
Expected: no files listed (or only whitespace normalization — if any `.go` file is printed, `git diff` it to confirm the change is purely formatting, then proceed).

- [ ] **Step 2: Vet**

Run: `go vet ./...`
Expected: no output.

- [ ] **Step 3: Full test suite**

Run: `go test ./...`
Expected: `ok` for every package, no failures.

- [ ] **Step 4: Race-detector pass over the packages this plan touched**

Run: `go test -race ./internal/tools/... ./internal/mcp/... ./internal/tui/... ./internal/cache/...`
Expected: `ok`, no data-race reports (the new async `tea.Cmd` path — `runMixedToolBatch`'s goroutine plus `Model.mcpBatchCancel`/`mcpAutoApprove`/`toolOK`/`toolErr` mutated back on the main goroutine via `mcpToolResultsMsg` — is exactly the kind of change worth race-checking specifically).

- [ ] **Step 5: Commit (only if Steps 1-4 required any fixes)**

```bash
git add -A
git commit -m "chore: gofmt/vet fixes from the MCP tool-calling wiring pass"
```

If Steps 1-4 required no changes, there is nothing to commit — the feature is complete as of Task 9's commit.
