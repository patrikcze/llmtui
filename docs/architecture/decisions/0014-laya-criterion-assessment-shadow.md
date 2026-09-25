# ADR 0014: Keep criterion assessment shadow-only

## Status

Accepted for the Phase 3 evaluation implementation.

## Context

Phase 2 added optional, pinned `CriterionAssessmentSpec` metadata to the
existing task contract. That metadata describes a bounded proposition and an
evidence projection; it does not establish a second acceptance system. The
next experiment needs to measure whether a local decision model can judge those
propositions from controller-owned evidence without changing the agent loop.

The existing observation cache is intentionally small and process-local. A
resumed run has no retained body, and a read can become ambiguous or stale
after another operation. Re-reading the workspace or storing a second body
ledger would change the ownership and privacy design.

## Decision

Add the explicit `criterion_shadow` decision-engine mode. In that mode only,
new contract requests ask for assessment metadata version 1. After each cycle
the TUI may submit one bounded batch of pinned semantic assessments. The batch
uses two fixed boolean questions per eligible criterion: direct support and
direct contradiction.

Admission is conservative:

- receipts require one successful, executed current-cycle receipt;
- local reads require one exact current-cycle `read_file`, one complete
  observation-cache view, and no later write/edit/command;
- missing, stale, truncated, evicted, or ambiguous evidence abstains without
  inference;
- each state has one framed/redacted proposition and at most one 512-byte
  excerpt, a 2 KiB serialized-state cap, and a five-second total batch budget.

The result is diagnostic data only. It is bound to spec/evidence fingerprints,
model revision, cycle, and the existing async generation. It cannot update
criterion status or notes, append evidence, alter verifier input or routing,
change stop decisions, authorize tools, or create a new store. Late and
budget-censored measurements are counted explicitly; raw proposition,
observation, provider stderr, and user content are not placed in diagnostics.

## Consequences

This creates honest Phase 3 measurements and preserves the existing controller
when Laya is disabled or unavailable. The restrictive admission policy will
produce abstentions for resumed runs, partial reads, duplicate reads, and
post-read mutations; those are expected denominators, not successful
assessments. Any future criterion-assist behavior requires separate calibration
and an authority decision; this ADR grants none.

## Rollback

Use `decision_engine.mode: shadow` or set `decision_engine.enabled: false`.
No run migration is required. Pinned metadata remains inert and the ordinary
contract/verifier path continues to work without Phase 3 requests.
