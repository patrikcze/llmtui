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

## Update (Phase 2 same-episode continuation, 2026-09-25)

`internal/tui/agent_yield.go` (new) wires `EvaluateYield` into
`handleStreamEvent`'s clean no-tool completion boundary and into every
`afterVisionCapture` completion callback in `vision_observation.go`, gated
by the new `agent.yield.enabled` config flag (default `false`, matching this
ADR's original design). Phase 2's mechanical-obligation source is narrow, as
planned: `AgentRun.PendingExactReadObligations` (new, `internal/agent/criteria.go`)
previews — without mutating any criterion status — which pinned criteria
`ExactReadCriterionTarget` (also new, factored out of the existing
`isExactReadCriterion`) recognizes as an atomic "read this exact file"
instruction not yet proven by the current cycle's `ExecutionResult`. Sharing
`evaluateCriterion`'s own matching logic for that preview, rather than
reading `run.Criteria[i].Status` directly, is load-bearing: `ApplyDeterministicCriteria`
still only commits status at verification, so a read that already succeeded
earlier in the same cycle would otherwise be reported outstanding forever —
exactly the false no-progress loop this ADR's freshness prerequisite exists
to prevent.

Two Phase 2 simplifications, deliberately narrower than a full reading of
§10, are recorded here rather than left silent: `MechanicalObligation.Actionable`
checks tool-capability availability (`m.toolsOn && m.toolRunner != nil`)
only, not workspace-rules/path validation, because the latter would mean
duplicating `internal/tools`' own confined path resolver in the TUI layer —
a Workspace Tool Safety Invariant this project treats as a real risk, not a
style preference. An unreadable target still costs at most
`max_nudges_without_progress` wasted continuations before the episode parks
as `no_progress`, not an unbounded loop. And `ContextFeasible` is
optimistically always `true`: `continueChat` already runs `prepareRequest`
and fails safely through its own existing error path if preparation is not
feasible, so Phase 2 does not duplicate that check ahead of time.

`EpisodeCheckpoint` is populated for real now (`ExecutorRequests`,
`NoProgressNudges`, `LastYieldReason`, `UnresolvedCriterionIDs`, `Revision`)
but still only in memory — `persistAgentRun`'s snapshot does not yet
special-case it beyond whatever plain JSON marshaling already does for an
additive struct field, and restart-safety validation remains Phase 6 work as
planned.

## Update (Phase 4a coverage-aware read proof, 2026-09-25)

Phase 2's exact-read proof had a real gap, recorded as harness plan §4
finding #3: `evaluateCriterion`'s `CriterionSemantic` branch (and therefore
`PendingExactReadObligations`, which shares its logic) treated *any*
successful `read_file` call touching the criterion's target path as proof —
including a narrow windowed read of a large file the model never fully saw.
Phase 4's own gate text calls this out directly: "validate resource
publication and full/read-range proof."

The fix adds a new bounded receipt, `agent.ReadObservation` (`internal/agent/types.go`),
to `ExecutionResult`: target identity, source digest (when known), the
delivered 1-based line window, and the source's total line count (when the
read's own scan reached EOF without a scan-limit truncation). `internal/tui/agent_loop.go`'s
`readObservationFromResult` translates a successful `read_file` `tools.Result`
into one — `internal/agent` still never imports `internal/tools`, per
CLAUDE.md's dependency-direction rule. Two read shapes both need this
translation: a windowed read (`tools.ResultMeta.Window` is set) and,
separately, `readFileMetaContextByte`'s legacy whole-file path (no
offset/limit given), which never populates `Window` at all — only
`Coverage.TotalLines`, and only when the read was not byte-truncated. Both
map to an observation; the byte-truncated and byte-range-only cases produce
none (no line window exists to trust).

`evaluateCriterion`'s exact-read branch and `readCoverageComplete`
(`internal/agent/criteria.go`) now require the *union* of a target's
observations — merged only within one consistent, non-conflicting source
digest and total-line count — to gaplessly span `[1, TotalLines]`. A single
full read still resolves in one observation, exactly as before; a sequence
of partial reads resolves once their windows join up; a gap, or windows from
two different file versions, never resolves it. `NextReadOffset` exposes the
same coverage state as a precise "read starting at line N for M more lines"
hint, which `internal/tui/agent_yield.go`'s `buildAgentYieldDirective` now
uses to name an exact `read_file({"path":...,"offset":...,"limit":...})`
call in the continuation directive instead of always saying "use the
offered tool again" — closing the gap between this ADR's original §9
example and what Phase 2 actually shipped. `ReadObservation` carries no raw
body and is bounded to `MaxReadObservations` (32) entries via
`AppendReadObservation`'s append-and-trim, matching `AppendEvidence`'s
existing shape.

Two existing tests needed their fixtures strengthened, exactly as the
harness plan's §19 test-matrix note anticipated for
`TestVerifiedAgentExactReadCriterionStopsWithoutSemanticReplay`: both used a
bare `ToolCallRecord{Succeeded: true}` fixture with no observation at all,
which the new coverage requirement correctly no longer accepts as proof.

Deliberately out of scope for this update, left for a later Phase 4 pass:
the generic `RecoveryHint` struct and producer table from harness plan §12
(grep/glob/list_dir/web/MCP typed recovery — only `read_file`'s own
existing `Window`/`Coverage` fields are consulted here, nothing new was
added to `tools.ResultMeta`); the model-authored `read_coverage` contract
requirement type from §10 (this update instead makes the *existing*
exact-read grammar coverage-aware, rather than adding a new criterion
kind or extending `agentverify.Contract`'s JSON schema); and the full
three-file (`zscaler-mock`) target scenario from §19 — the new
`TestAgentYieldContinuationNamesPreciseOffsetForPartialCoverage` covers the
same coverage-proof mechanism with a single large fixture file instead.

## Update (Phase 4 gate: source-change end-to-end proof, 2026-09-25)

Phase 4's own delivery gate (harness plan §23) names three things: "full
target scenario, source-change/expiry cases, and no side-effect rerun for
command truncation." The prior update's unit-level tests
(`TestExactReadCriterionMixedSourceVersionsNeverCombine` and siblings in
`internal/agent/criteria_test.go`) already proved `readCoverageState`'s
digest-consistency check in isolation. `TestAgentYieldSourceChangedBetweenPartialReadsNeverFalselyCompletes`
(`internal/tui/agent_yield_test.go`) now proves the same guarantee through
the real pipeline: a fixture file is read (lines 1-100), genuinely modified
on disk (different bytes, same line count — a different `SourceDigest`,
which two same-line-number windows would otherwise gaplessly union), then
read again (lines 101-200). The episode correctly never reports the
criterion satisfied and instead exhausts its no-progress nudge budget,
exactly as an unrecoverable coverage gap should. `agentScriptStep` gained a
`before func()` hook (test-only) to inject the file mutation at a precise
point in a scripted multi-turn sequence without a real time-based race —
useful for any future test needing the same shape.

The remaining gate item, "no side-effect rerun for command truncation," is
about `run_command` recovery specifically (harness plan §12's producer
table: "Never rerun a command to recover lost bytes"). It does not yet have
a corresponding runtime behavior to test: this update's yield-continuation
directive only ever names `read_file` calls (`buildAgentYieldDirective`
only consults `agent.NextReadOffset`, which is read-coverage-specific), and
no mechanical obligation type exists yet for `run_command` at all — so
there is no path today by which the runtime could suggest rerunning a
command. That guarantee becomes a real, testable claim only once a
`run_command`-linked obligation exists, which requires the deferred
`RecoveryHint`/read-coverage-contract work above. Recorded here rather than
left silently unaddressed.

**Full three-file target scenario:** still deliberately not attempted. It
requires the deferred `read_coverage` contract-schema work (§10) — the
narrow exact-read grammar this phase extends only recognizes a criterion
shaped exactly like "Read the file `<path>`.", not the scenario's own
"inspect wrapper" / "compare behavior" phrasing, which the plan itself
says (§19, "Phase 2's narrow exact-read fixture proves the mechanism; it
does not claim this richer contract/coverage scenario works before Phase
4") requires that richer contract layer first.
