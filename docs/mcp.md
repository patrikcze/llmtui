# MCP servers (Model Context Protocol)

llmtui has **MCP-ready** architecture: you can declare MCP servers in config,
inspect them, and toggle them from the TUI. It is **optional and disabled by
default**, and no server is ever contacted or started unless you explicitly
enable it.

> **Status:** this build ships the MCP **configuration, interfaces, and
> registry only**. No wire transport is wired in yet, so servers can be
> declared, validated, inspected, and enabled/disabled, but they cannot
> connect and advertise tools. Connecting over a real transport (stdio) is a
> separate step. `/mcp status` shows each server as `no_transport` when
> enabled.

## Commands

```text
/mcp                     # same as /mcp status
/mcp status | list       # table of configured servers and their state
/mcp tools               # tools advertised by connected servers
/mcp inspect <server>    # one server's config (env values redacted)
/mcp enable <server>     # mark a server as intended-to-run
/mcp disable <server>    # disable a server and drop any connection
/doctor mcp              # validate MCP config
```

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
  contact or launch any server. Starting a server runs its `command`, which
  is treated as a potentially dangerous action under the same approval model
  as the workspace tools (`approve: ask` by default).
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
