# ADR 0013: Laya pinned criterion assessment metadata (Phase 2)

Status: Accepted
Date: 2026-09-25

## Context

The decision architecture needs a stable way to evaluate whether bounded
controller-observed evidence supports a criterion. A model-generated question
or assessment recipe must not become a second acceptance list, evidence store,
or completion authority. The existing `AgentRun.Criteria` records already own
criterion identity and persistence; the contract stage is the only point at
which optional metadata can be attached without allowing executor or verifier
updates to rewrite it.

## Decision

Add an optional version-1 `CriterionAssessmentSpec` to the existing pinned
criterion. It contains only a bounded proposition, one of `receipts` or
`local_read`, and an optional exact target. A local-read target must be a
literal workspace-relative path. The contract response can carry an optional
zero-based criterion index, but the entire extension is discarded if any entry
is malformed, duplicated, out of range, unsafe, unsupported, or would attach to
a truncated criterion. Valid core criteria remain usable.

The extension is requested only by explicit evaluation callers through
`ContractInput.AssessmentVersion`. Ordinary contracting uses the existing
schema and parser behavior. TUI handling passes validated attachments to the
existing `AgentRun` owner; it does not inspect or act on them. Metadata is
copied atomically with criterion IDs and remains immutable after pinning.

Persist it only inside the existing run criteria. If shared secret redaction
would change a proposition or target, omit that attachment instead of storing
a changed claim. Resume strips invalid optional metadata while retaining the
otherwise valid schema-v1 run.

## Consequences

- Phase 2 adds no Laya inference and no active policy behavior.
- Deterministic criteria, semantic verification, tools, approval, and completion
  remain authoritative and unchanged.
- There is no second criteria registry, question store, evidence ledger, body
  cache, or assessment result cache.
- Later shadow assessment work must derive bounded evidence from existing
  observations and treat missing, stale, or redacted content as unavailable.
