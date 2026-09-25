# ADR 0012: Laya guarded verifier escalation (Phase 1)

Status: Accepted
Date: 2026-09-25

## Context

ADR 0010 and ADR 0011 shipped two shadow-only Laya observations — a
post-cycle prediction and an earlier pre-verifier prediction — with a
consistent finding across every manual calibration round so far (7 rounds,
31 samples, tracked outside this repo, referenced from
`docs/decision-engine.md`'s calibration notes): 0% observed false-finish,
but only ~55–65% raw agreement and a persistent `ask_user` blind spot.
`.claude/tasks/plans/laya-decision-architecture.md` (the full architecture
review this phase implements) treats a **G1 report** — a preregistered
acceptable missed-review risk and latency/verifier-call budget, a threshold
chosen on calibration data and frozen, held-out paired evidence of
improvement over current policy, and confidence intervals clustered by
independent task — plus this ADR as the two preconditions for any active
Laya influence at all.

**This ADR is accepted without a completed G1 report.** That is a deliberate,
explicit exception, made at the repository owner's direction after being
shown the gap in writing (no G1 report exists; the manual calibration data
above is explicitly insufficient on its own — see the plan's §10 "Enough
samples" note: zero observed failures in 31 mixed samples has an
approximate one-sided 95% upper bound of 3/31, not a low-failure-rate
proof). The owner chose to ship the mechanism now rather than wait for that
evidence. This ADR does not retroactively claim the evidence exists, and it
does not lower the bar for what counts as an *active* profile — see
Decision, below, for how those two things are kept separate.

## Decision

Guarded verifier escalation is a **one-way** capability: on an
adaptive-mode cycle whose deterministic evidence would otherwise take a
synthetic (no-verifier) completion shortcut, Laya's
`semantic_verifier_needed` probability may force the existing semantic
verifier to run anyway. It can never skip, delay, or replace a verifier
that would otherwise run; never mark a run done; never author, satisfy, or
revoke a criterion; never authorize a tool or bypass approval; never
override a deterministic failure. `dispatchVerifierAttempt` and
`handleAgentVerification` are called completely unmodified when escalation
fires — the guarded path produces nothing an ordinary semantic dispatch
doesn't already produce.

**Eligibility is structural, not a runtime check the escalation path could
get wrong.** `startAgentVerification`'s existing branch logic — which
mode/deterministic-evidence branch a cycle takes — is extracted verbatim
into a pure function, `planAgentVerification`
(`internal/tui/agent_decision_policy.go`), returning a route (`Synthetic`
or `Semantic`) and a `GuardEligible` flag. `GuardEligible` is true only for
the two synthetic-**PASS** branches §7 of the plan permits: the early
all-resolved-criteria shortcut (only when the resolved verifier mode is
itself adaptive — that shortcut fires regardless of mode, but eligibility
does not) and the mechanically-complete-cycle shortcut. Every other
branch — off/deterministic/always routes, a `Semantic` route, and every
synthetic failure/needs-input/blocked outcome — is never eligible.
`dispatchGuardedAssist` is called from exactly one call site, gated on
`GuardEligible`, so a cycle that was already going to dispatch a real
verifier structurally cannot be intercepted (`TestPlanAgentVerificationBranches`,
`TestGuardedAssistNeverDispatchedForSemanticRoute`).

**Activation requires three independent things, all present:**

1. `decision_engine.enabled: true` and `decision_engine.mode: guarded_assist`
   (`config.DecisionEngineConfig.ResolvedMode`, mirroring
   `AgentVerifierConfig.ResolvedMode`'s degrade-to-safe-default convention —
   an unknown mode falls back to `shadow`, never blocks startup).
2. A wired decision engine (`m.decisionShadow != nil`) — unaffected by this
   phase; still nil whenever `enabled` is false.
3. A **resolved calibration profile** for the loaded model alias
   (`decisionCalibrationProfile`, looked up in
   `decisionCalibrationProfiles`).

`decisionCalibrationProfiles` is a `var` (not user config, not a database,
never a public threshold knob) that ships **empty** in this codebase. A
profile binds a frozen threshold, a maximum wait, and an evidence-report
citation to an exact model alias/revision — never synthesized or defaulted
at runtime (no arbitrary 0.5 default). With no entry, `dispatchGuardedAssist`
returns nil unconditionally and the caller falls back to the exact
synthetic result `startAgentVerification` always produced before this
phase existed — this is what "shipped inert" means concretely, and it is
what keeps this ADR's acceptance-without-G1 honest: the code exists and is
reviewable, but it has no behavioral authority until a **separate,
future, independently reviewed** change adds a profile backed by an actual
G1 report. Adding a profile is not authorized by this ADR.

**The guarded predict call reuses, not duplicates, the pre-verifier
shadow's observation for the same cycle.** `dispatchGuardedAssist` registers
the same `preVerifierCorrelation` entry `dispatchAgentDecisionPreVerifierShadow`
would have and issues one `Predict` call with `preVerifierQuestions`; its
result feeds `recordPreVerifierPrediction` exactly as a shadow-only
observation would have, so a guard-eligible cycle's calibration accounting
is unchanged in shape — one prediction, one correlation record — never two
Laya calls for the same stage (`TestGuardedAssistFeedsPreVerifierCorrelationExactlyOnce`).
The post-cycle shadow (ADR 0010's separate mechanism, asking a different
question set at the end of `handleAgentVerification`) is untouched and
still fires as before regardless of how the cycle resolved.

**The call is strict** (`decision.PredictOptions.RequireCompleteInput`,
Phase 0b): the MLX worker rejects — before running inference — any request
whose head/options/state would lose content to the tokenizer's budget, so a
guarded decision can never be made from silently truncated input. A
rejection surfaces as an ordinary `Predict` error, falling back to the
synthetic result exactly like a worker crash or timeout would.

**The wait is bounded on both ends.** Context derives from
`m.agentContext()` (the run's own context) — unlike every shadow call,
which is deliberately independent so it can still record a late-arriving
actual outcome after the run ends, this call is on the authoritative
decision path, so cancelling the run must cancel it too. Its deadline is
`min(profile.MaxWait, time.Until(run.CreatedAt.Add(run.Limits.MaxElapsed)))` —
a guarded wait can never itself cause `agent.Limits.MaxElapsed` to be
exceeded, and a run with no remaining budget never gets a guarded wait at
all (`TestGuardedAssistNoRemainingBudgetNeverDispatches`).
`m.agentLoop.guardedAssistGen`/`guardedAssistCancel`/`pendingVerificationPlan`
mirror the existing `verifyGen`/`verifyCancel` pattern exactly, including
explicit invalidation from `cancelVerifiedRun`, so a stale or cancelled
result can never resolve (escalate or complete) a cycle that already
settled another way (`TestGuardedAssistCancellationNeverResolvesStaleCycle`).

## Consequences

- `decision_engine.mode: guarded_assist` is observable in config, `/debug
  last` (`laya guarded assist` line), and `eval.AgentTrial`'s
  `LayaGuardedAssist*` fields, but has **zero effect on any run** in this
  codebase as shipped — proven directly by
  `TestGuardedAssistWithNoProfileNeverDispatches` and
  `TestGuardedAssistDisabledEngineNeverDispatches`, and indirectly by the
  full pre-existing `agent_loop_test.go` suite passing completely unchanged
  against the extracted `planAgentVerification` (the refactor that made
  guarded-assist possible touches the exact same branch logic every
  existing verification-mode test already exercises).
- A future change that adds an entry to `decisionCalibrationProfiles` is a
  **separate, independently reviewable decision** — this ADR authorizes the
  mechanism's existence and its one-way, structurally-bounded shape, not
  any specific threshold, model, or the judgment that current evidence is
  sufficient. That change should cite its own G1 report explicitly, not
  this ADR's exception.
- CLAUDE.md's Laya-related guidance (startup/loading invariants) is
  unaffected: guarded-assist introduces no new model-loading, accelerator-
  probing, or startup path — every gate above collapses to the same
  `m.decisionShadow == nil` check every other Laya call site already uses
  when the engine is disabled.
