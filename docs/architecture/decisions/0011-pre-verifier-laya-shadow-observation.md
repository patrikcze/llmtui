# ADR 0011: Pre-verifier Laya shadow observation, hooked before criteria updates land

Status: Accepted
Date: 2026-09-24

## Context

ADR 0010 shipped a shadow-only Laya observation that fires after
`agent.Decide()` resolves a cycle. Manual calibration (7 rounds, 31
samples, tracked outside this repo) found 0% false-finish but a
consistent, cross-executor `ask_user` blind spot and only ~60% raw
`cycle_action` agreement — hard to interpret further because the
post-cycle question set can't separate "is Laya good at predicting the
final action" from "is Laya specifically good at judging whether semantic
verification was needed," since by the time it's asked, a semantic
verifier (if one ran) has already happened and its output is baked into
the same run state Laya is shown.

The documented (not implemented) future `guarded_assist` phase would need
exactly the narrower, *pre*-verifier judgment: whether to force a semantic
verifier adaptive mode would otherwise skip. This ADR covers wiring a
second, earlier shadow observation purely to gather that calibration
evidence, before any decision is made about implementing `guarded_assist`
itself.

## Decision

The second observation is dispatched from `startAgentVerification`
(`internal/tui/agent_loop.go`), immediately after
`run.ApplyDeterministicCriteria` and before any verifier-mode branching —
not from a new function, and not duplicated across the five different
deterministic-shortcut return paths in that function. All five funnel
through one `syntheticResult` closure; wrapping only that closure's
returned `tea.Cmd` in `tea.Batch(..., preVerifierCmd)`, plus the one
remaining explicit `dispatchVerifierAttempt` return, covers every path
with two edits instead of five.

The compact state snapshot is built by calling the **existing**
`buildAgentDecisionShadowState` unmodified, not a new pre-verifier-specific
struct. That function only ever reads `run`/`execution` fields; at this
hook point in the real flow, `run.Criteria` has not yet received any
verifier `CriteriaUpdates` (those land only inside `AgentRun.CompleteVerification`,
which has not run yet), so the snapshot is provably deterministic-only by
construction, not by any special-case stripping logic. `TestPreVerifierSnapshotExcludesSemanticOutput`
proves this directly: a snapshot taken at this point, compared against one
taken after simulating a verifier's `ApplyCriteriaUpdates`, differs exactly
where it should and nowhere else.

The question set is intentionally narrow — `semantic_verifier_needed` and
`evidence_sufficient` (both noul) — not the 5-way `cycle_action` choice
from the post-cycle observation, since there is no "final action" to
predict at this point in the cycle.

The two observations use **independent generation counters**
(`decisionShadowGen` and `preVerifierShadowGen`, both on `agentLoopState`)
rather than sharing one, so a stale response from either can never be
mistaken for the other's staleness signal — they have different question
sets and different arrival timing relative to the cycle boundary.

**Correlation is either-order-safe by design.** The prediction resolves
asynchronously via `tea.Cmd` and may arrive before or after the cycle's
authoritative outcome becomes known (inside `handleAgentVerification`,
right where the post-cycle shadow is already dispatched). A small, bounded
`map[string]*preVerifierCorrelation` keyed by `runID:cycle`
(`Model.preVerifierCorrelations`) holds whichever half has arrived; once
both are present, the pair folds into `preVerifierShadowMetrics` and a
bounded raw-sample slice (`Model.preVerifierShadowSamples`, capped ~256,
oldest evicted), then the map entry is deleted — the map only ever holds
in-flight cycles, never a growing history. The correlation-map cap (16)
mirrors `evictedResourceKeys`'s existing bounded-eviction pattern
(`agent_loop.go`), so a run that never reaches `handleAgentVerification`
(cancelled, crashed) cannot leak an entry forever.

`recordPreVerifierActual` is only called from `handleAgentVerification`
when `m.decisionShadow != nil` — when the engine is disabled, no
prediction was ever dispatched for that cycle, so recording an actual half
would only create a correlation entry that can never be finalized.

**`FalseNegative` (Laya said verification was unnecessary, but the
authoritative pipeline ran one anyway) is the priority metric**, not
aggregate accuracy — it is exactly the case a future active gate must never
produce, mirroring how `FalseFinishCount` was the priority metric for the
post-cycle observation in ADR 0010. The confusion-matrix counters computed
at a fixed 0.5 probability are explicitly documented as diagnostic-only;
`computeThresholdSweep` is a separate, pure function over the raw bounded
samples for a human (or a future PR) to read a real threshold-sweep report
from — no threshold is selected or acted on anywhere in this phase.

`/debug last`'s `laya shadow` line was also extended to show the full
`cycle_action` probability distribution (`debugInfo.DecisionShadowCycleActionProbabilities`),
not just the winning choice — calibration found the single winning
probability, and `Confidence`, both close to uninterpretable in isolation
(a clearly-correct choice can carry `Confidence` well under 0.5 even in
Laya's own reference fixtures). This is the concrete next diagnostic step
identified during manual calibration of the ask_user blind spot.

The optional calibration harness (`internal/tui/agent_decision_calibration_test.go`)
extends `eval.AgentTrial` additively (`Laya*` fields), matching that
struct's own established pattern (`Status`, `NetworkCalls`), rather than
building a parallel report type — reusing existing agent-evaluation
infrastructure per CLAUDE.md's engineering conventions. It runs scripted,
deterministic executor scenarios (the same `scriptedAgentProvider` pattern
used throughout `agent_loop_test.go`) against either a fake `decision.Engine`
(default, CI-safe — proves the confusion-matrix arithmetic, not real
calibration data) or a real Laya checkpoint (opt-in via
`LLMTUI_TEST_LAYA_MLX=1`, matching `internal/decision/mlx_integration_test.go`'s
existing convention) — deliberately keeping the *executor* side scripted
even in real mode, so a real checkpoint's judgment quality can be isolated
from executor non-determinism.

Alternatives considered and rejected: building a separate pre-verifier
state-snapshot type (rejected — the existing builder already reads only
pre-verifier-safe data at this hook point, so a parallel type would just
duplicate logic with no safety benefit); sharing one generation counter
between the two shadows (rejected — different question sets and arrival
timing make cross-contamination a real risk, not a theoretical one); a
single unbounded correlation history for post-hoc analysis (rejected — no
requirement calls for retaining resolved correlations, and an unbounded
structure is exactly the kind of state CLAUDE.md's tool-safety conventions
warn against accumulating without limit).

## Consequences

- The pre-verifier shadow can never influence `agent.Decide()`, the
  verifier, tool approval, or criteria — verified structurally (the call
  happens before any of those exist for the cycle) and by test
  (`TestPreVerifierDoesNotBlockRealVerifierDispatch`,
  `TestPreVerifierCrashDoesNotAffectAgent`, and the full existing
  post-cycle regression suite, which continues to pass unchanged aside from
  two predict-count assertions updated to reflect a second shadow call now
  sharing the same fake engine in tests).
- `/debug last` now surfaces two independent shadow lines (`laya shadow`,
  `laya pre-verifier`) plus the full `cycle_action` probability breakdown,
  giving future calibration rounds meaningfully more diagnostic signal than
  ADR 0010's single winning-probability-and-confidence view.
- Any future `guarded_assist` implementation has a real false-negative rate
  to calibrate a threshold against, gathered from realistic (or, opt-in,
  real-checkpoint) cycles rather than starting from zero evidence — but no
  threshold exists yet, and this ADR's shadow-only boundary is not relaxed
  by that future work without a fresh ADR.

## Update (Phase 0a measurement-integrity fixes, 2026-09-24)

A follow-up review of this ADR's own correlation bookkeeping (§3/§10 of
`.claude/tasks/plans/laya-decision-architecture.md`) found several defects
that would have understated or overstated calibration evidence without
changing agent behavior — see docs/decision-engine.md's "Measurement
integrity (Phase 0a)" section for the full list. In summary: the correlation
entry is now registered synchronously at dispatch time (not on first
arrival), so a late-but-legitimate result from an old cycle can still
finalize into the confusion matrix and sample history without overwriting
`/debug last`'s fields for whatever cycle is actually live; bounded-map
eviction is now deterministic (true insertion order, not Go's randomized map
iteration); duplicate arrivals for an already-recorded half are idempotent;
and a new, explicit ground-truth axis (`IndependentNeed`, set only by an
evaluation harness, never production) is computed separately from the
existing policy-agreement `ActualRan` sweep via a new pure
`computeNeedThresholdSweep`, so a sample with unknown ground truth or no
legitimate probability can never be silently counted as a known negative.
This ADR's shadow-only boundary is unchanged: every fix here is either
accounting or a correction to bookkeeping this ADR always intended to be
shadow-only.
