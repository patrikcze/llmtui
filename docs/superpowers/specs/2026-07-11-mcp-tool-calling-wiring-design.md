# MCP Tool-Calling Wiring

Date: 2026-07-11
Status: approved

## Goal

`internal/mcp` can already connect to MCP servers over stdio, list their
tools, and call them (`Registry.CallTool`) — but nothing in the app ever
calls that method. `/mcp connect` gets you visibility (`/mcp status|list|
tools|inspect`) and nothing else: no model, local or otherwise, can actually
invoke a connected server's tools through llmtui's chat loop. This wires
that gap shut: connected MCP servers' tools become part of the model's
native function-calling tool set, on the same approval-gated, budget-bounded
loop native workspace tools already use.

Motivating case: `jiraWorklog`, a local MCP server with 21 tools (session
tracking, worklog drafting/approval/submission against a real Jira Data
Center instance) — but the design is server-agnostic.

## Non-goals

- **No fenced-block/text-protocol support for MCP.** `internal/tools`' text
  protocol (`` ```tool write_file path ``` ``) exists so tool-calling works
  with local models that lack native function calling. Extending that
  protocol to arbitrary-JSON-schema MCP tools would need a new syntax,
  parser, and instructions text of its own. Explicitly deferred: this round
  wires native function-calling only. Models without native tool support
  simply won't see MCP tools, exactly as today.
- No changes to `internal/mcp` itself (registry, stdio transport, config
  schema) beyond what's listed under "Test-double changes" below — this is
  purely a consumer of the existing `Registry`/`Client` interfaces.
- No per-tool safety classification for MCP tools (e.g. inferring that
  `jira_get_issue` is safer than `worklog_submit`). MCP carries no
  standardized safety metadata in the protocol revision this client speaks
  (`2024-11-05`). Approval is governed purely by the server-level
  `ServerConfig.Approve` field, same granularity the config already offers.

## Decisions

1. **Native function-calling only for v1** (not the fenced-block fallback).
2. **Exposure reuses the existing `/tools on` master switch** — no new
   toggle. A server's tools are offered to the model when `m.toolsOn &&
   m.useNativeTools()` and the server is `StatusConnected` and
   `ServerConfig.Enabled`.
3. **MCP calls get their own auto-approve state** (`mcpAutoApprove`),
   separate from the existing `toolsAutoApprove`. Choosing "Always" on a
   workspace-tool prompt must never silently start auto-approving MCP calls
   with real external side effects (e.g. `worklog_submit`), and vice versa.
4. **MCP execution is async with a bounded timeout**, not synchronous like
   native tools. An MCP call is a subprocess round-trip that itself talks to
   a real network service (e.g. Jira Data Center) — meaningfully longer and
   more variable than local file I/O. `ServerConfig.Timeout` is currently
   stored but never enforced anywhere; this wiring is what makes it do
   something.
5. **Orchestration lives in `internal/tui`**, not `internal/tools` or
   `internal/mcp`. Neither package learns about the other; a new
   `internal/tui/mcp_tools.go` is the only place that imports both.

## Design

### Naming

Each connected server's tools are exposed with a collision-proof prefix:
`mcp__<server>__<tool>` (e.g. `mcp__jiraWorklog__session_start`) — the same
convention Claude Code itself uses. Built when assembling `req.Tools`;
split back into server/tool when routing a returned `ToolCall.Name`.

### `tools.Call` extension

Native tools have fixed, hand-mapped argument fields (`Path`, `Body`,
`Max`). MCP tools have arbitrary JSON-Schema arguments, so they can't be
mapped the same way. `Call` gains three fields, populated only for MCP
calls:

```go
type Call struct {
    ID   string
    Tool string // native: "read_file" | MCP: "mcp__jiraWorklog__session_start"
    Path string
    Body string
    Max  int

    MCPServer string          // non-empty marks this an MCP call, e.g. "jiraWorklog"
    MCPTool   string          // underlying tool name on that server, e.g. "session_start"
    MCPArgs   json.RawMessage // raw JSON arguments, passed through unparsed
}
```

`CallsFromNative` (`internal/tools/native.go`) gains one branch: a
`mcp__`-prefixed `tc.Name` splits into server/tool, and `tc.Arguments` is
copied verbatim into `MCPArgs` — no per-field mapping, since
`internal/tools` doesn't know or care what any given MCP tool's schema
looks like. A malformed prefix (doesn't split into exactly two `__`-joined
parts) still produces a `Call`, so dispatch can report a clear error back
to the model instead of the batch silently vanishing — same philosophy the
existing malformed-native-arguments path already uses.

### Exposure

`buildRequest` (`internal/tui/pipeline.go`) merges one more source into
`req.Tools`, gated per Decision 2: for every server that is both
`StatusConnected` and `ServerConfig.Enabled`, convert each `mcp.Tool` into
`provider.ToolSpec{Name: "mcp__"+server+"__"+tool.Name, Description:
tool.Description, Parameters: tool.Schema}`.

This assembly is factored into a shared `m.activeToolSpecs() []provider.
ToolSpec` (native + web + MCP), used by both `buildRequest` and the cache
key (below) so the two can never disagree about what tool set a request
actually carries.

### Dispatch

`internal/tui/mcp_tools.go` is the only file that imports both
`internal/tools` and `internal/mcp`. Given a `tools.Call`: if
`MCPServer != ""`, route to `m.mcpRegistry.CallTool(ctx, server, tool,
args)` and map the `mcp.Result`/error into a `tools.Result`; otherwise
unchanged — `m.toolRunner.Execute(c)`, exactly as today.

### Approval

Per Decision 3, a new `mcpAutoApprove bool` field on `Model`. Per-call
approval check:

```go
func (m *Model) callNeedsApproval(c tools.Call) bool {
    if c.MCPServer == "" {
        return m.toolRunner.NeedsApproval(c) // unchanged
    }
    if m.mcpAutoApprove {
        return false
    }
    srv, ok := m.mcpRegistry.Get(c.MCPServer)
    return !ok || srv.Config.ApproveMode() != mcp.ApproveAuto
}
```

`startToolBatch`'s existing per-call loop calls this instead of
`m.toolRunner.NeedsApproval` directly. The approval **prompt stays
unified** — one batch, one menu, even for a mixed native+MCP batch. "Always"
scans the pending batch and sets `toolsAutoApprove` and/or
`mcpAutoApprove` depending on which call *kinds* are actually present in
that batch, so approving "Always" on a `write_file` call can never leak
into auto-approving a later `worklog_submit`. The menu row label reflects
what's actually being granted ("always (workspace)" / "always (mcp)" /
"always (both)").

### Async execution

Per Decision 4: `runToolCalls` checks whether the batch contains any MCP
call.

- **Pure-native batch:** completely unchanged, still synchronous. Zero
  behavior change, zero risk, for the common case.
- **Batch containing an MCP call:** the *entire* batch (native calls too,
  for simple deterministic ordering) runs inside one `tea.Cmd` closure.
  Calls execute **sequentially**, in original order — not concurrently.
  (`jiraWorklog`'s own config sets `allow_parallel: false` at the session
  level; sequential execution sidesteps any server-side state race, and the
  latency cost is negligible next to model-inference time for the 1-3 calls
  a typical turn makes.) Each MCP call gets `context.WithTimeout(ctx,
  serverTimeout)`, where `serverTimeout` is `ServerConfig.Timeout` (falls
  back to the existing 30s default if unset). A new message type,
  `mcpToolResultsMsg{results []tools.Result}`, carries the ordered results
  back into `Update()`. `m.notice` updates to name what's in flight (e.g.
  `"⚒ running jiraWorklog: session_start…"`) so the UI doesn't look frozen.
  The parent context is cancellable the same way an in-flight streaming
  response already is (Ctrl+C).

### Catalog visibility (`/tools`)

Both `/tools list`/`/tools inspect` call sites
(`internal/tui/commands_local.go:1509,1554`) build `tools.DefaultRegistry()`
fresh on every invocation — no staleness problem, just a gap. Immediately
after, a new helper registers each connected server's tools:

```go
func registerMCPCapabilities(reg *tools.Registry, mcpReg *mcp.Registry) {
    for _, srv := range mcpReg.List() {
        if srv.Status != mcp.StatusConnected {
            continue
        }
        for _, t := range srv.Tools {
            _ = reg.Register(tools.CapabilityInfo{
                Name:        "mcp__" + srv.Config.Name + "__" + t.Name,
                Description: t.Description,
                Source:      "mcp:" + srv.Config.Name, // e.g. "mcp:jiraWorklog"
                Safety:      tools.SafetyExternalMCP,
                Approval:    srv.Config.ApproveMode(), // "ask" | "auto"
                Parameters:  t.Schema,
            })
        }
    }
}
```

`Source: "mcp:"+serverName` slots into the existing `/tools list <filter>`
substring match for free — `/tools list mcp` shows every MCP tool,
`/tools list jiraWorklog` shows just that server's. This is the seam
`internal/tools/registry.go`'s own comment already anticipated ("`/mcp`
... will register into it later so every surface ... lists tools from one
place").

### Cache-key fix

`cache.Key` (`internal/cache/cache.go:30-42`) has no field for the active
tool set today — not even native/web on/off state. That's a pre-existing
gap this change makes concretely reachable: the same user message and
history can now produce a materially different request depending purely on
which MCP servers happen to be connected, and a cache hit would replay a
response computed under a different tool set. Fix: add `ToolsHash string`
to `cache.Key`, computed the same way `historyFingerprint` hashes messages
— sha256 over each spec's `Name+Description+Parameters`, sorted by name for
stability — fed from the same `m.activeToolSpecs()` helper `buildRequest`
uses. This closes the gap for native/web tools too, not only MCP, since all
three were falling through the same blind spot.

### Error handling

Every failure produces a `tools.Result{Err: ...}` fed back to the model as
a `role:"tool"` message — never a crash:

- **Unknown/malformed MCP name** (doesn't split into `mcp__<server>__
  <tool>`, or names a server not currently registered) → clear `Result.Err`.
- **Server disconnected between request and response** (a real race: the
  model can take a while to answer, and the user can `/mcp disconnect` in
  the meantime) → `Registry.CallTool` already returns `"MCP server %q is
  not connected"`; nothing new needed, just don't assume a tool spec built
  earlier in the turn is still valid at execution time.
- **Malformed JSON arguments from the model** → passed through as-is (the
  stdio client already defaults empty args to `{}`); the server's own
  JSON-RPC error becomes `Result.Err` — same "model sees the problem and
  retries" pattern the native tools already use.
- **Timeout** → `context.WithTimeout`; `StdioClient.call` already selects
  on `ctx.Done()`. Documented caveat: a timeout means *llmtui* gave up
  waiting, not that the server necessarily rolled back anything — e.g. a
  slow `session_start` may have still created a session server-side even
  though the timeout fired locally. Nothing to fix, just something to state
  plainly in the docs.
- **Cancellation (Ctrl+C mid-call)** → parent context is cancellable the
  same way a streaming response already is; produces a distinct "cancelled
  by user" result rather than a raw context error.

### Test-double changes

`internal/mcp.MockClient.CallTool` currently ignores `ctx` entirely, so it
can't simulate a hang. Extend it to select on `ctx.Done()` before returning,
so the timeout/cancellation tests below can exercise real context
semantics rather than asserting structurally.

## Testing plan

- `internal/tools`: `CallsFromNative` splits `mcp__server__tool` into
  `MCPServer`/`MCPTool`/`MCPArgs` correctly; a malformed prefix produces a
  `Call` whose dispatch yields a clear error, not a panic.
- `internal/tui`: cache key differs when the connected-MCP-server set
  differs (same message + history, tool set changed → different
  `ToolsHash` → no false cache hit) — the specific regression case this
  project's CLAUDE.md cache-key invariant calls for.
- `internal/tui`: mixed-batch approval — native-needs-approval +
  MCP-server-`auto` blocks only on the native call, and vice versa;
  "Always" on a pure-native batch does not set `mcpAutoApprove` (and vice
  versa); "Always" on a genuinely mixed batch sets both.
- `internal/tui`: pure-native batches still take the exact synchronous path
  (protects the common case against behavior change); a batch containing
  any MCP call takes the new async path.
- `internal/tui`: a call that hangs past `ServerConfig.Timeout` (via the
  extended `MockClient`) produces a bounded `Result.Err` and the batch
  still completes, rather than hanging the test/TUI.
- `internal/tui`: `/tools list` shows a connected server's tools under
  `Source: "mcp:<name>"`; a configured-but-not-connected server contributes
  nothing.

## Documentation updates

`docs/mcp.md` gets a new section covering: tools are offered to the model
whenever `/tools on` and the server is connected; the separate MCP
auto-approve state and what "Always" does and doesn't grant; and the
timeout-doesn't-mean-rollback caveat.

## Future work (explicitly out of scope here)

- Fenced-block/text-protocol support for MCP tools, so models without
  native function calling can use them too (Non-goals, above).
- Per-tool (rather than per-server) approval policy, if MCP gains
  standardized safety annotations in a later protocol revision.
