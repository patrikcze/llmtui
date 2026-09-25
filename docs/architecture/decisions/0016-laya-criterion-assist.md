# ADR 0016: Keep criterion assist one-way and G2-gated

## Status

Accepted for the Phase 4b implementation; no production profile is approved.

## Context

Phase 3 can measure whether Laya's fixed support/contradiction questions agree
with controller-admitted criterion evidence. Phase 4a lets the existing
semantic verifier inspect the same bounded proof. A measured assistant could
be useful for detecting a criterion that deserves semantic review, but a
local decision model must not become a second acceptance authority or reduce
semantic coverage.

The plan's G2 gate requires proposition faithfulness/coverage evaluation,
held-out negative and ambiguous cases, improvement in review outcomes, and no
worsening of verifier anchoring or repeat work. No such approved report is
currently present in this repository.

## Decision

Add `criterion_assist` as an explicit decision-engine mode with an empty,
code-owned `criterionAssistProfiles` map. A profile, when separately added by
a future authority amendment, binds the exact model alias/revision, a frozen
contradiction threshold, a bounded wait, and its G2 evidence citation.

Only the existing adaptive synthetic-success eligibility gate may invoke the
shared Phase 3 criterion batch, and it invokes that batch once. Semantic
routes dispatch the existing verifier immediately and are never delayed by
criterion advice. A strong contradiction or ambiguous signal may request that
same verifier; support-only advice, missing/invalid evidence, errors, timeout,
cancellation, and below-threshold contradiction return the original
synthetic result. The wait is bounded by the profile and remaining run
deadline.

The handler never calls `ApplyCriteriaUpdates`, never changes evidence or
permissions, never reruns the executor, and never cancels a verifier already
required by policy. The verifier remains authoritative and receives all
unresolved semantic criteria. Diagnostics are additive and content-free.

## Consequences

The capability is reviewable and testable without changing normal behavior:
`criterion_assist` is inert until a future G2-backed profile is deliberately
added. Once active, it can add at most one existing semantic review to an
eligible synthetic cycle; it cannot turn a positive advisory into completion
or hide unresolved criteria. Rollback is a mode change or removal of the
profile, with no state migration.

## Rollback

Use `criterion_shadow`, `shadow`, or `decision_engine.enabled: false`. The
profile map remains empty in the shipped implementation.
