# ADR 0010: Laya shadow advisor is observational only

Status: Accepted
Date: 2026-09-23

## Context

PR #109 merged `internal/decision` (Laya MLX decision engine: `Service`,
`Router`, `ModelManager`, `MLXRuntimeLoader`) to master fully inert — reachable
only through explicit `llmtui decision predict/pull/runtime status` CLI
commands, never touched by the TUI or the bounded agent loop.
`decision_engine.enabled` already existed in config with zero consumers,
reserved for exactly this moment. This ADR covers the first real integration:
wiring the engine into the TUI's agent loop (`internal/tui/agent_loop.go`) so
it observes completed cycles, while deciding how much influence it gets and
how its lifecycle interacts with the rest of the TUI's subsystem-management
conventions.

Two prior decisions in this codebase shaped the options: `agent.Decide()`
(`internal/agent/policy.go`) is the sole authority for a cycle's stop
decision, and the existing "Phase 4 measured-assistance shadow tracker"
(`docs/agent-loop.md`'s "Calibration and assistance" section) already
established the pattern of an observational-only signal recorded in
`/debug last` before any policy change is considered.

## Decision

The Laya integration is **shadow-only**: after `agent.Decide()` resolves a
cycle and `ApplyStop` has already been applied, a compact bounded snapshot is
sent to Laya (`cycle_action`/`goal_complete`/`semantic_verifier_needed`), and
the result is recorded purely for `/debug last` and in-memory calibration
counters (cycle-action agreement, false-finish count, ask-user agreement,
verifier-needed agreement/false-negative rate). Nothing downstream of the
hook point reads the shadow result — `stop.Decision`, `run.Status`, the
verifier, tool approval, and acceptance criteria are computed and applied
before the shadow call is even dispatched, so by construction no return value
from it can change the run's outcome.

The hook point is inside `handleAgentVerification`, immediately after
`agent.Decide()`/`ApplyStop`, not inside `startAgentVerification` (where the
feature was originally proposed). `startAgentVerification` forks into five
different early-return paths depending on verifier mode and deterministic
evidence; `handleAgentVerification` is the single funnel every one of those
paths (plus the real async-verifier path) routes back through via
`agentVerificationMsg`, so it is the only point where `run`, the cycle's
`ExecutionResult`, and the final `StopResult` are all simultaneously in
scope for exactly one shadow-call site.

Lifecycle wiring follows the existing subsystem-construction pattern in
`rebuildFromConfig` (`personalApps`, `mcpRegistry`): a new `decisionShadow
*decisionShadowService` field on `Model`, gated on `cfg.DecisionEngine.Enabled`,
closed in `quit()` and `Run()`'s defensive defer alongside `mcpRegistry`. One
deliberate divergence from that precedent: `mcpRegistry`/`personalApps` are
silently dropped and rebuilt on `/config reload` (an accepted tradeoff
documented at their call sites), but `configureDecisionShadow` explicitly
closes the old `decisionShadowService` before replacing it. An un-Closed
`Router` would leak its MLX subprocess indefinitely, and — verified by
reading `decision.Router.Close`'s actual implementation, not just its doc
prose — `Router.Close` never blocks on an in-flight prediction (a busy entry
is marked `closing` and left for its releasing `Predict` call to close
later), so the explicit close is safe to run synchronously on the `Update()`
goroutine rather than needing a `go func()` escape hatch.

The shadow call's `context.Context` is independently bounded
(`context.WithTimeout(context.Background(), 5*time.Second)`), never
`m.agentContext()`. `endAgentRun()` cancels the run's context synchronously
inside `handleAgentVerification` for every terminal decision, before any
`tea.Cmd` batched in that same `Update()` call actually runs — reusing
`m.agentContext()` would hand a terminal-cycle shadow call an
already-cancelling context.

Alternatives considered and rejected: hooking inside `startAgentVerification`
(rejected — would require duplicating the shadow-call site across five
branches, and dispatched before `agent.Decide()` even runs, so there would be
no authoritative decision yet to compare against); giving Laya influence in
this PR, e.g. skipping verification above a confidence threshold (rejected —
explicitly out of scope; no calibration evidence exists yet to justify any
threshold); a new independent telemetry store for shadow results (rejected —
`debugInfo` already has an established "shadow tracker, diagnostic only"
precedent in the `AssistanceHint`/`AssistanceReason` fields; extending it is
strictly simpler than a parallel store).

## Consequences

- Laya's recommendation can never affect an agent run's outcome in this PR —
  verified directly by construction (the call happens after the outcome is
  already final) and by tests asserting the authoritative result is
  unaffected regardless of what a fake engine returns (`finish` vs a failed
  verifier, `continue` vs done, `ask_user` creating no picker state,
  `semantic_verify` not forcing a real verifier call).
- A decision-runtime error of any kind (disabled, unavailable, worker crash,
  timeout, malformed answer) only ever records an unavailable reason; the
  agent run proceeds identically to a build with no decision engine wired at
  all.
- This establishes a new, documented reload-close pattern
  (`configureDecisionShadow`) that other subsystems holding live
  subprocesses may want to adopt instead of the silent-drop tradeoff
  `mcpRegistry`/`personalApps` currently accept — not retrofitted onto them
  by this change, but available as precedent.
- A future `guarded_assist` phase (documented, not implemented, in
  `docs/decision-engine.md`) would need its own explicit decision to move
  Laya from observational to a strictly one-way escalation-only capability;
  this ADR's shadow-only boundary is not automatically relaxed by that future
  work without a fresh ADR.
