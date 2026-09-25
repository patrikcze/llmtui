# ADR 0017: Optional Laya yield shadow

Status: Accepted
Date: 2026-09-25

## Context

The agent execution harness now has a shared, deterministic verification
boundary. Existing Laya observations cover post-cycle action and pre-verifier
need, but they do not record the bounded state at that boundary as a distinct
yield sample. A future policy experiment may need paired observations for the
deterministic route and the semantic route, including outcomes that cannot be
treated as successful evidence.

No measured ambiguous class or calibrated yield profile authorizes changing
the controller. Phase 7 therefore records an optional sample stream only.

## Decision

`decision_engine.yield_shadow` is false by default and requires the existing
`decision_engine.enabled` opt-in as well. When enabled, the TUI makes one
bounded Laya prediction for the existing cycle verification boundary and
records a versioned `agentYieldShadowSample` in session-local memory.

The input contains only a bounded objective, criterion IDs/statuses, a closed
progress category, closed tool outcome codes, deterministic route/proposed
action, and delivered file version metadata. It contains no raw tool bodies,
hidden reasoning, arbitrary arguments, MCP descriptions, or approval data.
The sample is correlated with the already-authoritative stop action after
`agent.Decide` and `ApplyStop` have completed.

Prediction errors are recorded as `missing`; cancellation or runtime reload
records `censored`; a valid pair that arrives after its cycle is no longer
live records `late`. These outcomes are reported separately from available
samples and are never converted into a confident negative or zero probability.

The Laya answer cannot affect verification routing, criteria, tools, budgets,
completion, approval, persistence, user interaction, or ordinary chat. No
calibrated profile is added and no active yield policy is authorized by this
ADR. Any future influence requires a separate evidence review.

## Consequences

- The feature is disabled without changing existing behavior or constructing
  an additional prediction request.
- Samples are bounded, versioned, and not persisted; process restart loses
  them safely.
- `/debug last` reports total, available, missing, censored, late, and
  duplicate counts plus the last closed-vocabulary outcome.
- The implementation reuses the existing long-lived decision service and
  timeout rather than adding a worker, router, or policy kernel.
