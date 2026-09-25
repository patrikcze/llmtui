# ADR 0013: A pure yield policy at the no-tool completion boundary

Status: Accepted
Date: 2026-09-25

## Context

`internal/tui/app.go`'s `handleStreamEvent` already runs one verified-agent
`Cycle` through many consecutive model/tool turns inside a single
`StageExecutor` episode (native-tool loops, fenced-tool loops, provider/tool
recovery, context compaction). But the moment a model produces a clean
no-tool completion, the current code makes exactly one decision: call
`startAgentVerification`. There is no policy step in between that asks
whether the executor actually finished its own obligations, as opposed to
merely stopping talking. Existing verification can correctly ask for another
cycle, but a whole new `Cycle` is a heavier, more visible unit than "the
executor should keep going in the same episode" — it re-selects an
objective, resets cycle counters, and is not meant to be the mechanism for
"you forgot to read the file the criterion named."

A prior audit (`.claude/tasks/plans/agent-execution-harness-v2.md`, not
committed — see its own note on why it lives in the git-ignored
`.claude/tasks/plans/` directory) traced this gap in detail and proposed a
phased plan. Phase 0 closed four protocol/coverage integrity gaps that would
have made this gate less trustworthy once built (typed next-offset metadata,
streamed-response terminality, a truncated-fenced-call admission gap, and
same-cycle criterion-proof freshness). This ADR is Phase 1: the policy
itself, as pure types and a pure function, with no execution authority yet.

The design also surveyed a reference multi-agent coding tool (OMP, pinned
commit cited in the plan) for transferable *behavior*, not code: an inner
loop with an explicit `onBeforeYield` boundary, a stop-time completeness
check, and several distinct stop guards (retry, abort, compaction dead end,
async work, stop hooks). None of that architecture was imported; only the
boundary concept and the discipline of an explicit, ordered precedence table
were.

## Decision

Add `internal/agent/yield.go` as a **pure, total function**:
`EvaluateYield(YieldInput) YieldDecision`. It takes an immutable,
adapter-assembled projection of run status — cancellation, safety blocks,
budget state, pending human barriers, protocol completeness, pending
already-owned async work, no-progress nudge counters, and bounded
criterion-linked mechanical obligations — and returns a typed `YieldAction`
(`Continue`, `Verify`, `NeedsUser`, `Wait`, `Blocked`, `Failed`, `Cancelled`,
`BudgetExhausted`, or the zero-value `Unknown`) plus a `YieldReason` and any
criterion IDs involved. It holds no `Model`, provider client, tool runner, or
mutable entity registry; it makes no network or inference call; nothing in
this package calls it yet. Wiring it into `handleStreamEvent` is Phase 2.

The function evaluates nine ordered precedence rules exactly as specified by
the plan's §7 (cancellation and existing safety/invariant outcomes first,
then hard budgets, then human barriers, then protocol completeness, then
already-owned async work, then the no-progress nudge cap, then mechanical
continuation gated on context feasibility, and only then quiescence). The
ordering is load-bearing, not incidental: `TestEvaluateYieldPrecedenceOrder`
deliberately sets two or more tiers true at once in every case and asserts
the higher-precedence one wins, so the rules cannot silently be reordered by
a future edit without a table test failing.

`YieldVerify` is the only action that implies quiescence (`Quiescent()`); no
second, independently mutable "quiescent" status was added. Quiescence is
deliberately blind to unresolved *semantic* criteria — `YieldInput` has no
field for them at all, because they never affect this decision (they are the
existing verifier's concern, not this gate's). A criterion being `Pending` is
not, by itself, a mechanical obligation: `MechanicalObligation.Actionable`
must be explicitly true (capability available, permission not denied,
snapshot not lost, not an unexecutable wildcard) or the obligation is
filtered out and the run falls through to `YieldVerify` like any other
unresolved criterion, never silently retried forever.

Terminal actions reuse the existing `Decision` vocabulary instead of adding
parallel terminal strings: `TerminalDecision()` maps `NeedsUser` →
`DecisionNeedsUserInput`, `Cancelled` → `DecisionCancelled`,
`BudgetExhausted` → `DecisionBudgetExhausted`, `Failed` → `DecisionFailed`,
and `Blocked` → `DecisionNoProgress` (no-progress-stalled reason) or
`DecisionEscalated` (safety or context-infeasible reasons) — the same two
outcomes `internal/agent/policy.go` already produces for equivalent
conditions today.

`types.go` gains an optional, additive `Cycle.Episode *EpisodeCheckpoint`
field: bounded per-episode metadata (policy version, executor request count,
no-progress nudge count, last yield reason, last progress digest, capped
unresolved criterion IDs, a revision counter, and an interrupted flag). It is
`nil` for every cycle today and for any run persisted before this field
existed; nothing writes it yet. Its JSON tag is `omitempty` and it needs no
special-casing in `store.go`'s encode/decode or redaction path — both are
already generic over the marshaled tree, not an explicit field allowlist.

Alternatives considered and rejected: giving `EvaluateYield` access to the
live `Model`/run so it could compute its own inputs (rejected — the whole
value of a pure function here is that its precedence and quiescence logic
can be table-tested without a provider, tool runner, or TUI, per the Phase 1
gate; assembling `YieldInput` is deliberately left to the Phase 2 adapter);
introducing new terminal `Decision` strings for yield-specific stops
(rejected — the plan explicitly says not to add new terminal strings to
persisted run status unnecessarily, and the existing `DecisionNoProgress` /
`DecisionEscalated` already mean exactly what a stalled or blocked yield
means); letting a `Pending` semantic criterion alone justify `YieldContinue`
(rejected — semantic criteria are usually pending until verification, so
that rule would starve the verifier, which is the plan's central risk
called out in its executive summary).

## Consequences

- No runtime behavior changes for any user yet. `EvaluateYield` has no
  caller outside its own test file; `EpisodeCheckpoint` is written by
  nothing. `agent.yield.enabled` (the config flag gating Phase 2's actual
  wiring) does not exist yet either.
- Phase 2 can wire `handleStreamEvent` to build a real `YieldInput` and act
  on `YieldDecision` with reasonable confidence the *policy* itself is
  correct, because its precedence and quiescence rules are already covered
  by deterministic table tests independent of any provider or TUI fixture.
- The bounded, additive `EpisodeCheckpoint` shape is fixed now, before any
  code writes to it, so Phase 2's wiring and Phase 6's persistence work
  extend one agreed-on shape instead of renegotiating it under a wiring
  change. `Interrupted`, `Revision`, and the policy-version fields exist
  specifically so Phase 6 does not need a schema bump to add restart safety.
- `TerminalDecision()` gives Phase 2 a single call to end a run from a
  terminal yield without re-deriving which existing `Decision` value a given
  `YieldReason` corresponds to, keeping that mapping in one tested place
  instead of duplicated at each call site.
