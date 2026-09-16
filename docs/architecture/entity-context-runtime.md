# Entity Context Runtime

The Entity Context Runtime (ECR) gives the model short-lived opaque references
to useful tool results. It avoids making every later request repeat a large
result while keeping the source data controller-owned and bounded.

## Purpose and data model

`internal/entity` owns the low-level registry. A candidate has a typed kind
(`file`, `web_result`, `web_page`, or `mcp_result`), provenance, a bounded
payload, a short preview, trust label, and lifetime scope. `Registry.Put`
returns an identifier of the form `ent_00001`; IDs contain no path, URL,
secret, pointer, or authorization data.

The registry enforces maximum entity count, per-payload bytes, total payload
bytes, and full-expansion count. Eviction is deterministic (oldest use, then
ID), and reset preserves the sequence so an old ID cannot resolve to a new
object. Expired, malformed, unknown, and unavailable references produce
explicit statuses.

## Lifecycle and detail levels

Entities are held only in the live `tui.Model`. They are not serialized into
history, response-cache entries, durable memory, RAG, or agent evidence.
Session entities survive ordinary turns and tool continuations. `/clear`, a
resumed session, config rebuild, and process exit reset them. Agent-run
entities are scoped to the run and released when that run ends.

The model sees a minimal record containing the ID, kind, label, safe source,
trust/scope, preview, and digest. `get_entity_details` accepts up to eight IDs
and requests `identifier`, `minimal`, or `full`. Full payloads remain bounded,
consume a per-request expansion budget, and are returned inside an untrusted
structural frame.

## Tool and prompt flow

The existing `turnRuntime` remains the sole orchestration state machine. Tool
adapters attach candidates to successful `read_file`, `web_search`,
`web_fetch`, and MCP results. The shared `sendToolResults` boundary registers
them, appends compact references alongside the result sent to the model, and
keeps native and fenced protocols aligned. The controller handles
`get_entity_details`; the generic runner cannot execute it.

`internal/prompt` adds an `Entity Context` section when live references exist.
It uses the same untrusted framing policy as RAG, web, and MCP data. The raw
user message remains verbatim and last. Entity context is independent of
`memoryindex`: entities are transient runtime observations, not preferences,
project facts, episodes, or retrieval records. Context compaction may omit an
entity from the prompt; the registry is still the authority for a later valid
ID until eviction or expiry.

## Agent relationship

Agent cycles reuse the same registry and can carry session-scoped references
across legitimate cycle boundaries. Run-scoped references are released at run
end. An entity is never acceptance evidence by itself; the existing agent
verification and evidence ledger remain authoritative.

## Security

Entity IDs are strict opaque tokens and resolve only in the current in-memory
registry. They cannot bypass workspace path checks, approvals, disabled tools,
MCP discovery, or web SSRF policy. File, web, and MCP candidates retain their
untrusted origin. The detail capability is read-only and cannot be used as a
path or command argument. Secret-file reads remain governed by the existing
runner approval policy; failed results and secret-file reads are not
registered, while an explicitly redacted candidate can retain metadata only.

## Configuration and diagnostics

```yaml
entities:
  enabled: true
  max_session_entities: 256
  max_payload_bytes: 65536
  max_total_payload_bytes: 4194304
  max_context_tokens: 1200
  max_full_expansions: 8
```

`/entities status` shows bounded counters and expansion usage. `/entities
list` shows IDs and metadata only. `/entities inspect <id>` shows the typed
minimal metadata and never dumps the payload. No telemetry leaves the process.

## Example

After a web search, the model receives a compact result reference such as
`ent_00001` and may ask for `get_entity_details` with that exact ID. A stale
or malformed ID returns a status instead of triggering a new search or
inventing missing fields.

## Non-goals

ECR is not a vector store, durable memory system, capability/permission
handle, general object graph, or replacement for existing tool safety and
agent verification.
