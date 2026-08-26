# Dynamic HTTP tool registry

llmtui can expose the exact native tool definitions currently provisioned to
its active model as a versioned, read-only HTTP resource. This is useful for an
agent host, gateway, or diagnostics client that needs to inspect llmtui's live
capabilities without parsing the TUI.

The registry is discovery only. It does not execute tools, accept prompts, or
replace the standard OpenAI-compatible `tools` request field. An LLM cannot
autonomously query an HTTP URL unless its host gives it network/tool access;
normally the host queries this endpoint and provisions the returned schemas.

There are two separate but synchronized flows:

1. llmtui assembles the active native tool definitions and sends them directly
  to LM Studio, Ollama, or another provider with each inference request. For
  OpenAI-compatible providers, they are the request's standard `tools` array.
2. The HTTP endpoint exposes that same active snapshot for external agent
  hosts, gateways, and diagnostics clients.

LM Studio does not query this endpoint as part of normal llmtui operation. Its
request log should instead show the definitions in `POST /v1/chat/completions`.
The endpoint is a discovery mirror, not a replacement provider protocol or a
second tool-execution path.

## Configuration

```yaml
tool_registry:
  enabled: true
  listen: "127.0.0.1:7834"
  token_env: ""
  shutdown_timeout: "5s"
```

The default loopback listener may run without authentication. A non-loopback
listener is rejected unless `token_env` names a non-empty environment variable:

```bash
export LLMTUI_REGISTRY_TOKEN="$(openssl rand -hex 32)"
```

```yaml
tool_registry:
  enabled: true
  listen: "0.0.0.0:7834"
  token_env: LLMTUI_REGISTRY_TOKEN
```

Use TLS through a trusted reverse proxy when traffic leaves the host. The
registry does not enable CORS.

## Endpoint

```text
GET /api/v1/tools
```

Loopback example:

```bash
curl http://127.0.0.1:7834/api/v1/tools
```

Authenticated example:

```bash
curl -H "Authorization: Bearer $LLMTUI_REGISTRY_TOKEN" \
  http://host.example:7834/api/v1/tools
```

Response:

```json
{
  "api_version": "v1",
  "generated_at": "2026-08-17T12:00:00Z",
  "tools": [
    {
      "name": "read_file",
      "description": "Read a file in the project workspace and return its contents.",
      "source": "builtin",
      "safety": "read_only",
      "approval": "ask for secret files",
      "input_schema": {
        "type": "object",
        "properties": {"path": {"type": "string"}},
        "required": ["path"]
      }
    }
  ]
}
```

Responses use `Cache-Control: no-store`. Only `GET` is accepted. The response
contains no credentials, provider configuration, MCP process configuration,
workspace path, conversation data, tool arguments, or execution results.

## Live behavior

Each HTTP request asks the Bubble Tea update loop for a fresh snapshot, so it
cannot race session toggles. The snapshot is assembled by the same
`activeToolSpecs` path used for provider requests:

- `/tools off` returns an empty tool array.
- `/web on|off` adds or removes `web_search` and `web_fetch`.
- `skill_load` appears only while the model-visible skill catalog is available.
- A configured MCP server contributes no tools until `/mcp connect` completes
  its MCP `tools/list` request successfully.
- Connected MCP tools use `mcp__<server>__<tool>` names and their server-provided
  JSON Schemas. Small catalogs expose all of them.
- Above `tools.discovery.threshold`, undisclosed MCP tools stay internal to the
  eligible catalog. `tool_search` adds bounded matches to the model-visible
  snapshot for the current human task; the endpoint reflects that change on
  its next request.
- `/mcp disconnect` or `/mcp disable` removes those tools immediately.
- If the active provider/model falls back from native tool calling to the
  fenced prompt protocol, the native registry becomes empty.

Consequently, a model-visible MCP tool is either present in both the HTTP
snapshot and subsequent native LLM requests, or absent from both. Connected but
undisclosed tools in a large catalog are intentionally absent from this endpoint
because it remains an exact model-visible mirror, not an endpoint for every
potential capability. The provider request remains authoritative for a
particular inference because it snapshots its tool array when composed.

### Verify MCP synchronization

Capture the registry's current names while the TUI is running:

```bash
curl -s http://127.0.0.1:7834/api/v1/tools |
  jq -r '.tools[].name'
```

Then connect a configured server with `/mcp connect <server>` and repeat the
request. With a small catalog, its tools should appear immediately. With a
large catalog, invoke `tool_search` through the model and repeat after the next
inference; the disclosed `mcp__<server>__<tool>` names should then appear. The
provider request must contain the same names.

Run `/mcp disconnect <server>` or `/mcp disable <server>`, then repeat both
checks. Those MCP names should be absent from the endpoint and the next
provider request. This before/after test proves that MCP `tools/list`, HTTP
discovery, and per-request model provisioning share the current tool set.

### Native fallback

This endpoint exposes structured native tool schemas only. If a provider/model
rejects native function calling and llmtui switches that pair to the fenced
text protocol, the endpoint returns an empty `tools` array even though llmtui
can continue offering its supported fenced tools through prompt instructions.
That state does not mean the registry server failed.

## Versioning

The URL and `api_version` identify the contract. Additive fields may be added
within `v1`; incompatible changes require a new `/api/v2/` endpoint. Unknown
JSON fields should be ignored by clients.