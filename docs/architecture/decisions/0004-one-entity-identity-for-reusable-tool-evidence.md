# ADR 0004: One entity identity for reusable tool evidence

Status: Accepted
Date: 2026-09-23

## Context

Tool output that exceeded a display cap (a large `run_command` result, a
truncated `web_fetch` page, a capped `grep` result set) was previously lost
once the caller moved on: only the already-clipped preview survived in
transcript text. The next-generation tool runtime plan (`.claude/tasks/plans/next-generation-tool-runtime.md`, §12–§13) proposed making the existing
`internal/entity.Registry` — already the session's one bounded, model-facing
identity space for semantic entities (`web_result`, `file`, `mcp_result`,
`vision_observation`) — the sole owner of retained result bodies too, rather
than introducing a second, parallel artifact/resource registry.

## Decision

`internal/entity.Registry` gained a second, parallel path
(`Registry.Publish`/`Registry.OpenBody`) alongside its existing
`Put`/`Resolve` path for small semantic entities. Published bodies get a new
random ID family (`ent_` + 26 lowercase base32 characters encoding 128 random
bits) that is disjoint by construction from the legacy sequential `ent_00001`
IDs `Put` still mints — no migration, no aliasing, one registry. `read_file`
and `grep` accept an explicit `resource_id` selector (alongside `path`) that
resolves through the same registry a plain `get_entity_details` lookup uses;
there is no second addressing scheme, no `artifact://` URI, and no
filesystem-spool path handed to a model. `internal/tools.ResourceReader` is
declared in the consumer package (`tools` already imports `entity` for
`entity.Candidate`), and `internal/tui` supplies a thin adapter
(`tool_resource_adapter.go`) around the live `*entity.Registry` rather than
handing the pointer to `Runner` directly, leaving a seam for future
generation-checking without changing the interface again.

Alternatives considered and rejected: a separate public artifact/resource
store independent of `entity.Registry` (rejected — a second identity space a
model would need to reason about, and a second lifecycle/quota/eviction
system to keep consistent with the first); reusing the existing bounded
`agent.ObservationCache` excerpt mechanism as a general blob store (rejected
— that cache is deliberately small, bounded, run-scoped proof evidence, not
a reusable-content substrate, and conflating the two would remove the
distinction between "evidence a verifier saw" and "bytes a model can page
through").

## Consequences

- One registry, one mutex, one eviction/quota policy owns both small
  semantic entities and larger retained bodies; they share accounting only
  where genuinely shared (session/turn/agent-run scoping) and never merge
  their separate byte budgets (`Limits.MaxPayloadBytes`/`MaxTotalPayload` vs
  `Limits.MaxBodyBytes`/`MaxTotalBodyBytes`).
- A model that has never seen `resource_id` in a schema still works exactly
  as before — the selector is optional and additive on `read_file`/`grep`.
- Reading a `resource_id` never triggers network or filesystem I/O; it only
  ever returns bytes this process already captured and retained.
