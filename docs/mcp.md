# MCP servers (Model Context Protocol)

llmtui can connect to MCP servers over stdio, list their tools, and call
them. It is **optional and disabled by default**, and no server is ever
contacted or started unless you explicitly enable **and** connect it.

The transport is **JSON-RPC 2.0 over newline-delimited stdio**: llmtui runs
the server's command as a subprocess, performs the MCP `initialize`
handshake, then uses `tools/list` and `tools/call`.

## Commands

```text
/mcp                     # same as /mcp status
/mcp status | list       # table of configured servers and their state
/mcp tools               # tools advertised by connected servers
/mcp inspect <server>    # one server's config (env values redacted)
/mcp enable <server>     # mark a server as intended-to-run
/mcp connect <server>    # launch the server and list its tools
/mcp disconnect <server> # stop the server's subprocess (keep it enabled)
/mcp disable <server>    # disconnect and disable a server
/doctor mcp              # validate MCP config
```

Typical flow: `/mcp enable files` → `/mcp connect files` → `/mcp tools`.

## Configuration

Declare servers under `mcp.servers`. Everything defaults to off:

```yaml
mcp:
  enabled: false
  servers:
    filesystem:
      enabled: false
      transport: stdio
      command: "mcp-server-filesystem"
      args: ["/path/to/workspace"]
      approve: ask # ask | auto
      timeout: "30s"
    custom:
      enabled: false
      transport: stdio
      command: "/path/to/server"
      args: []
      env: {} # values are redacted in /mcp inspect and never logged
      approve: ask
      timeout: "30s"
```

A server runs only when **both** `mcp.enabled` and the server's own
`enabled` are true.

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

## Safety

- **Nothing starts on its own.** Building the registry at startup does not
  contact or launch any server. A server subprocess is spawned only when you
  run `/mcp connect <server>` — that explicit command is your authorization
  for running its `command`.
- **Controlled environment.** The server subprocess does not inherit your
  full environment: it gets a small safe base (`PATH`, `HOME`, `USER`,
  `SHELL`, `LANG`, `TMPDIR`, `TERM`) plus the values you configure under
  `env`, so unrelated host secrets are not exposed to the server.
- **Clean shutdown.** Connected servers are stopped when you disconnect,
  disable them, or quit llmtui.
- **Invalid config never blocks startup.** `/doctor mcp` reports config-shape
  problems for every server, but a malformed **disabled** server does not
  affect normal chat. Command existence on `PATH` is only checked for servers
  that are enabled while `mcp.enabled` is true.
- **Environments are redacted.** MCP server environments often carry
  credentials; `/mcp inspect` shows only the variable names, never values,
  and env values are never logged.

## Disabling everything

MCP is off unless you turn it on. Keep `mcp.enabled: false` (the default), or
`/mcp disable <server>` any server you no longer want available.
