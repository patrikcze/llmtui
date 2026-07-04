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
