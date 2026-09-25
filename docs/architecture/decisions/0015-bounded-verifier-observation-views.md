# ADR 0015: Give the semantic verifier bounded observation views

## Status

Accepted for the Phase 4a implementation.

## Context

The Phase 3 criterion-assessment experiment can inspect a narrowly admitted
local-read excerpt, but the authoritative semantic verifier still sees only
structured receipts and summaries. That content-blindness makes a useful
comparison impossible for criteria whose proof is textual. The verifier must
gain this context without creating a second evidence store, rereading the
workspace, trusting executor prose, or making Laya part of the verification
path.

## Decision

The existing verifier input accepts a separate `Observations` projection. The
TUI selects at most four complete, successful, current-cycle local-read views
in unresolved-criterion order, with a 2048-byte aggregate excerpt cap. It uses
the Phase 3 cache and admission rules exactly; missing, stale, truncated,
ambiguous, evicted, or post-mutation proof is omitted. The unresolved
semantic `Criteria` list remains authoritative and is not reduced to the
criteria that happen to have views.

Each excerpt is redacted and framed as untrusted user evidence. It is never
inserted into the verifier system instruction, and no arbitrary evidence text
can become an instruction. The user evidence message states that the bounded
list is not exhaustive and that omitted proof remains unknown. The existing
request admission estimates the complete prompt, including the excerpt
content. If the content-bearing request cannot be admitted, the controller
retries the same verifier with `Observations` removed; it does not silently
exceed the run budget or skip semantic verification.

Laya is not imported into this path and its modes do not change the behavior.
Tool use, approval, criterion status, evidence persistence, routing, and stop
decisions remain owned by the existing controller and verifier.

## Consequences

The semantic verifier can evaluate eligible textual proof while retaining a
truthful unknown state for omitted proof. The projection is intentionally
small and may omit useful material when several criteria compete for the
bound; omission is safer than rereading or truncating a supposedly complete
proof. A resumed run starts without cached body excerpts and therefore uses
the summary-only path until fresh evidence is produced.

## Rollback

Remove the projection at the verifier call site or disable the semantic
verifier as before. No persisted schema or run migration is required; the
existing `Input` fields and controller-owned evidence remain valid.
