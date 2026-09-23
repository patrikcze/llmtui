# ADR 0006: Ephemeral body lifetime and optional private spooling

Status: Accepted
Date: 2026-09-23

## Context

Once retained tool-result bodies existed (ADR 0004), they needed a storage
policy: how long they live, whether they ever touch disk, and what happens
to them across a session reset or process restart. The codebase has no
existing durable-history mechanism for raw tool output, and the plan's
non-goals explicitly exclude "automatic durable memory promotion of raw
resources" and "persistence of images, clipboard data, personal-app content,
or secret-file bodies through the new layer" (§3).

## Decision

Bodies are memory-only by default (`entities.output_storage: memory`) and
bounded by a separate byte budget from small semantic entities
(`Limits.MaxBodyBytes`/`MaxTotalBodyBytes`, default 4 MiB/16 MiB). An
explicit opt-in (`entities.output_storage: disk`) adds a private,
per-session disk spool (`internal/entity/body_disk.go`): a random directory
under `os.TempDir()`, owner-only permissions (`0700` dirs, `0600` files),
fixed random hex basenames unrelated to tool names/URLs/model IDs,
`redact.Secrets` applied before any byte reaches disk, atomic publish via a
temp-sibling-then-rename, an owner marker plus an exclusive lock file, and a
startup sweep that removes only stale directories carrying llmtui's own
marker whose PID is no longer alive and whose lock can be acquired — a live
session is never swept, and an unmarked directory is never touched.
`Registry.Reset()` performs a hard wipe of all bodies (memory and disk
alike), including any currently pinned by an open lease, because a session
reset is an explicit boundary the caller crossed, not routine eviction — an
already-open `BodyLease`'s in-flight read stays valid (memory backend hands
back an independent byte-slice copy; disk backend has already read the file
into memory by the time a lease exists), but a *new* `OpenBody` for that ID
correctly reports it gone. No body of any kind survives a saved-session
reload; nothing here writes to the chat history/session file.

Alternatives considered and rejected: unconditional disk backing (rejected —
raises the stakes of every retained byte from "lost on process exit" to
"written to a local filesystem," which the plan requires to be an explicit,
labeled choice, not a default); a silent raw-content disk cache with no
redaction pass (rejected — directly contradicts the "no secret-file bodies
through the new layer" non-goal); a cross-session lookup API letting a new
process resume a prior session's spooled bodies (rejected — "Existing live
registry migration is unnecessary because upgrades restart the process," and
a resumable spool would need its own durability/versioning contract this
program does not take on).

## Consequences

- Losing a retained body is always safe: worst case is `resource_unavailable`
  on the next `OpenBody`, never stale or cross-session content.
- Enabling disk backing is a deliberate, visible config change with its own
  quota, sweep, and diagnostics surface — not a silent side effect of
  entities being enabled.
- A crash-abandoned disk spool is eventually reclaimed by the next process's
  startup sweep without ever risking a live session's files.
