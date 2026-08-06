# v1 State, Cache, and Storage Boundaries

## 1. Source-of-truth table (confirmed, `v1-audit.md` §3 origin)

| Concern | Owner | Notes |
| --- | --- | --- |
| Conversation/message state | `internal/chat/session.go:18` `Session.Messages` | Single instance, shared by all three lifecycles. |
| Run state (`/agent on`) | `internal/agent/types.go:159` `AgentRun` (Stage/Status/Cycle) | Held live in `tui.agentLoopState.run` (agent_loop.go:24). |
| Tool state / in-flight batch | `tui.Model.toolDepth`, `pendingCalls`, `mcpBatchGen`, `activity` (app.go:164-215) | UI-owned, not in `internal/agent`. This is correct: tool-batch state is a UI-loop concern even for `/agent on`, since agent mode reuses the same batch machinery (ADR 0001). |
| Permissions/approval | `tui.Model.approvalPolicy` + `tools.Runner`/guardrails + MCP per-server `approve` config | Three cooperating owners; no single source of truth, but no conflict found either — each governs a disjoint concern (session grants, path/command guardrails, server trust). |
| Context budgeting | `internal/contextmgr` (`Decide`/`Split`), invoked from `pipeline.prepareRequest` | Window source resolved in `Model.contextWindow` (pipeline.go:106-118). |
| Cache | `internal/cache/cache.go` | Final-response cache only — see §2. |
| Persistence (sessions) | `internal/history` | |
| Persistence (agent runs) | `internal/agent/store.go` (`agent.NewFileStore`) | |
| Memory (user preference) | `internal/memory` | |
| Stale-event identity | `tui.Model.streamGen` (app.go:1601), `mcpBatchGen` (app.go:796), `agentLoop.verifyGen` (agent_loop.go:325-327) | Confirmed correct, see `v1-audit.md` §6. |
| **New in v1**: progress ledger | proposed: `internal/agent` or a new small `internal/progress` package, referenced by the shared kernel | See `v1-agent-runtime.md` §3. Must not become a duplicate of `internal/cache`. |

No accidental coupling was found between these owners during the audit —
this is a genuine strength to preserve, not an area needing consolidation.

## 2. Cache architecture

Only **one** cache exists in the current codebase: the final-response cache
(`internal/cache`). Confirmed by `grep -rn "Cache" internal/mcp
internal/tools` returning no hits — there is no tool-result cache and no
MCP discovery/resource cache today, so master-prompt §7.6's requirement to
"not use one ambiguous cache abstraction for unrelated behavior" is
satisfied by omission: there is nothing to disambiguate yet, because only
one cache category is implemented.

**Key composition** (`pipeline.go:418-441`, `cacheKeyFromPrepared`):
provider, base URL, model, message, composed system prompt, full history
fingerprint (`HistoryHash`), tools fingerprint, skills fingerprint, runtime
fingerprint. This was independently spot-checked in this session and found
to already include the composed system prompt and history hash — the two
fields whose omission is explicitly called out as a risk in
[`CLAUDE.md`](/CLAUDE.md)'s Workspace Tool Safety Invariants ("The cache
key for a response must reflect everything that actually varies the
request... never key on a subset that can collide"). **No cache-key defect
was found.**

**Agent-mode bypass**: `/agent on` cycles set `m.bypassCache = true`
(agent_loop.go:160,184,203), matching `docs/agent-loop.md`'s documented
"Agent cycles bypass the response cache because completion must reflect
current workspace/tool evidence." Confirmed correct and preserved.

### Target additions (master-prompt §7.6), scoped by need

Master-prompt §7.6 lists six cache categories. Only two exist today
(final-response cache; conceptually, context-summary snapshots already
exist as `contextmgr`'s deterministic split/summarize output, though not
labeled a "cache"). The other four (per-run tool-call dedup, optional
tool-result cache, MCP discovery/resource cache, provider prompt/prefix
caching metadata) are new:

- **Per-run tool-call dedup** is the progress ledger itself
  (`v1-agent-runtime.md` §3) — it is deliberately *not* a cache in the
  reuse-a-result sense; it is a repetition detector. Do not implement it as
  a keyed cache that returns stale results — it must block execution, not
  substitute a remembered answer, per master-prompt §7.6's explicit
  warning that "agentic or side-effecting runs must not receive stale final
  answers."
- **Optional tool-result cache** (TTL/freshness-based, e.g. for expensive
  read-only lookups) is not required to close the reported failure mode
  and is deferred out of the v1 critical path unless a specific tool
  demonstrates a need (e.g. repeated identical `read_file` calls on an
  unchanged file could legitimately reuse a result — but the progress
  ledger already handles that case by recognizing "no new evidence,"
  without needing a separate cache).
- **MCP discovery/resource cache**: deferred; no evidence in the audit that
  its absence causes correctness or performance problems at current usage
  scale.
- **Provider prompt/prefix caching metadata**: deferred to the provider
  capability work (`v1-provider-capabilities.md`) if/when a provider
  starts reporting it; nothing to report on today.

## 3. Persistence boundaries

Confirmed already separated and versioned per master-prompt §7.7's
checklist, per `docs/agent-loop.md` §"Run memory, privacy, and resume":
versioned JSON, atomic temp-file+rename writes, owner-only permissions,
corruption tolerance (corrupt records skipped on load), size caps (64 KiB
per record, newest 32 retained), and secret redaction before persistence.
No gap found here during the audit. The progress ledger (new) should follow
the same pattern if any part of it is persisted (e.g. to survive `/agent
resume`) — in-memory-only for the ordinary tool loop is sufficient since
that loop does not persist across process restarts today.
