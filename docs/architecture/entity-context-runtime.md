# Entity Context Runtime

The Entity Context Runtime (ECR) gives the model short-lived opaque references
to useful tool results. It avoids making every later request repeat a large
result while keeping the source data controller-owned and bounded.

## Purpose and data model

`internal/entity` owns the low-level registry. A candidate has a typed kind
(`file`, `web_result`, `web_page`, `mcp_result`, or `vision_observation`), provenance, a bounded
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

`internal/prompt` adds an `Entity Context` protocol section in every prompt mode
while tools and ECR are enabled, even when no references fit the recent shortlist.
The complete section, including metadata and untrusted framing, is bounded by
`entities.max_context_tokens` using the existing token estimator. A budget too
small for the protocol omits the section; the tool schema still describes lookup.
It uses the same untrusted framing policy as RAG, web, and MCP data. The raw
user message remains verbatim and last. Entity context is independent of
`memoryindex`: entities are transient runtime observations, not preferences,
project facts, episodes, or retrieval records. Context compaction may omit an
entity from the prompt; the registry is still the authority for a later valid
ID until eviction or expiry. The shortlist is based on recency, not semantic
retrieval. A model can discover omitted entities through `get_entity_details`
with `{"query":"name or topic keywords"}`. This performs a bounded,
case-insensitive keyword scan of live labels, safe paths/URLs, previews, and
stored payloads. Label matches rank above metadata, preview, and payload matches;
ties use recency and then ID. It returns at most eight minimal candidates with
`total_matches` and a `truncated` flag for partial results. It never expands
payloads or chooses an identity on the user's behalf. The model can narrow its
keywords if needed, then expand an exact returned ID. No embeddings, network
calls, or additional model requests are used by the lookup itself.

`vision_observation` records are created after a successful normal multimodal
turn, when Entity Context tools are active, by one bounded, tool-free capture
request through the active provider.
The capture returns only validated model-derived text describing visible
content; it does not use the normal answer, hidden reasoning, tools, MCP, web,
memory, or a second provider/runtime. The entity source remains
`user_provided_image`, and its trust is `vision_model_derived`; visual
similarity never becomes web provenance. The attachment's byte digest, turn,
image index, MIME, provider/model, capture version, timestamp, truncation, and
raw-retention state remain controller-owned provenance. Raw image bytes are
never stored in the entity or history.

`get_entity_details` accepts an optional `kinds` filter. Use
`{"query":"topic","kinds":["vision_observation"]}` when the user refers to
a prior image. A filtered query returns no other evidence class; no matching
visual entity means the old visual evidence is unavailable.

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
  vision_enabled: true
  vision_max_tokens: 800
```

`/entities status` shows bounded counters and expansion usage. `/entities
list` shows IDs and metadata only. `/entities inspect <id>` shows the typed
minimal metadata and never dumps the payload. No telemetry leaves the process.

## Example

You can say “What did the release notes say about migration?” without supplying
an entity ID. The model matches the recent labels/previews or calls
`get_entity_details({"query":"release notes"})` to discover candidates. It then
requests `{"entity_ids":["ent_00001"],"level":"full"}` using an actual returned
ID. Query and ID selectors are mutually exclusive; query lookup supports only
minimal detail. An empty match list means the stored data did not match those
keywords, not that the original resource does not exist. Ambiguous results
remain separate candidates. A stale or malformed ID returns a status instead
of triggering a new search or inventing missing fields.

This applies to previously registered tool results in the current live session.
Durable `/memory` records and RAG chunks still use their existing retrieval
paths; ECR does not automatically register them. Restarted/resumed sessions,
expired entries, and evicted payloads cannot be recovered through entity lookup.

## Non-goals

ECR is not a vector store, durable memory system, capability/permission
handle, general object graph, or replacement for existing tool safety and
agent verification.
