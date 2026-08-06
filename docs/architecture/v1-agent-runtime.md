# v1 Agent Runtime — Target Design

This document defines the v1 target for the shared orchestration kernel
identified in [`v1-audit.md`](v1-audit.md) §2-3 and
[ADR 0001](decisions/0001-single-orchestration-kernel.md). It is additive to
the existing kernel, not a replacement for it (per ADR 0001, no rewrite is
justified).

## 1. Explicit state machine

The current implementation already has a real (if implicit) state machine
distributed across `tui.Model` fields (`toolDepth`, `pendingCalls`,
`streamGen`, `mcpBatchGen`) and `agent.AgentRun.Stage`/`Status`. v1 makes the
states explicit and typed, covering both ordinary tool chat and `/agent on`
under one vocabulary:

| State | Ordinary tool chat | `/agent on` |
| --- | --- | --- |
| Idle | no request in flight | no active run |
| Preparing | `prepareRequest`/composition | same, plus `agentDirective()` injection |
| Model streaming | `startRequest` | same |
| Waiting for approval | `pendingCalls` non-empty | same |
| Executing tools | `runToolCalls` | same |
| Processing tool results | `sendToolResults`/`continueChat` | same |
| Verifying | n/a | `startAgentVerification` |
| Compacting context | `contextmgr.Decide`/`Split` mid-`prepareRequest` | same |
| Waiting for user input | n/a (loop ends, ordinary chat has no "needs input" terminal state) | `DecisionNeedsUserInput` |
| Completed | final answer rendered | `DecisionDone` |
| Cancelled | `Esc`/`Ctrl+C` mid-stream | `/agent cancel`, same signal |
| Failed | provider/tool error surfaced | `DecisionFailed` |
| Budget exhausted | `tools.max_iterations` reached, renewal declined | `DecisionBudgetExhausted` (today: cycle-boundary only — see §3) |

Ordinary tool chat currently has no equivalent to `needs_user_input` or
`parked` as *terminal* states — a denied approval simply ends the batch. v1
does not need to add these as new user-facing states for ordinary chat; the
audit found no evidence this causes the reported failure mode, and adding
unrequested terminal states to ordinary chat would be scope creep beyond
what the evidence justifies (master-prompt §3.1). The state table above is
descriptive/unifying vocabulary, not a mandate to make ordinary chat behave
like `/agent on`.

Illegal transitions (e.g. a verify message arriving for a stage that already
moved past verification) are already guarded by the run/cycle/gen triple
check at `agent_loop.go:325-327` and the `streamGen`/`mcpBatchGen` checks at
`app.go:1601,796`. These are confirmed correct (`v1-audit.md` §6) and are
preserved as-is.

## 2. Deterministic termination

The controller already decides when execution stops; this is not a gap.
What is a gap (per `v1-audit.md` §4.2) is that one class of stop decision —
hard-budget exhaustion — is only evaluated at a cycle boundary that a
tool-call spree can indefinitely postpone reaching.

v1 terminal outcomes (supersets of what exists today):

- final response completed — exists (ordinary + agent `DecisionDone`)
- user approval required — exists (`pendingCalls`)
- user input required — exists in `/agent on` (`DecisionNeedsUserInput`)
- cancelled — exists (`Esc`/`Ctrl+C`, `/agent cancel`)
- provider failure — exists (`EventError` handling)
- tool failure — exists (structured `tools.Result{Err:...}`)
- invalid protocol — exists (`toolsRejectedError`, malformed native call
  rejection)
- **no progress — new** (§3 below; does not exist today, per
  `v1-audit.md` §4.1)
- **repeated-call loop — new** (§3 below; same gap)
- maximum turns/`tools.max_iterations` — exists, but renewal is uncapped in
  ordinary chat (`v1-audit.md` §9 risk 3); v1 does not remove the human
  renewal prompt (it is a deliberate user-control point, master-prompt
  §3.2), but the renewal prompt must surface progress-ledger state so the
  user can see *why* another round is being requested, not just that one
  is.
- maximum cycles — exists (`agent.max_cycles`)
- maximum tool calls / maximum tokens — exists at cycle boundary; **v1 adds
  a live check** (§4 below, ADR 0002) so it is enforced within a cycle too
- maximum elapsed time — exists and is already live (`resetAgentContext`,
  `context.WithTimeout`)
- context exhausted — exists (`contextmgr`)
- verifier failure — exists, and already correctly cannot be overridden by
  the verifier itself (deterministic-evidence override, `v1-audit.md` §6)

A model response with no tool calls and a valid final answer already
finishes the loop correctly (confirmed, `v1-audit.md` §2). A response
truncated during a tool call is already correctly rejected as success
inside `/agent on` (`ErrorTruncated`, commits `c5766af`…`0458b5f`). **Gap:**
the ordinary (non-agent) tool-continuation path does not consult
`ChatEvent.Truncated` when deciding whether to continue the tool loop. This
should be closed as part of the same slice that adds live budget checks,
since it is the same code region (`app.go` stream-event handling) and the
same class of "was this turn actually complete" question.

## 3. Progress ledger (new)

Per [ADR 0002](decisions/0002-live-progress-ledger-and-budget-enforcement.md).
Design constraints, restated from master-prompt §7.2 and grounded in the
confirmed gap:

**Fingerprint composition.** `(tool_name, canonical_args, resource_key)`
where:
- `canonical_args` is the validated/normalized argument set (stable key
  ordering, no incidental whitespace/formatting differences) — not the raw
  JSON string, so cosmetically different but semantically identical calls
  still collide.
- `resource_key` is tool-specific: for `web_search`, the normalized query;
  for `web_fetch`, the canonical URL (post-redirect, per
  `v1-security-review.md`'s SSRF handling); for file tools, the resolved
  workspace-relative path; for `run_command`, the command plus relevant
  argument set.

**Evidence of progress**, any of which resets the repeat counter for that
fingerprint:
- result digest differs from the prior call's result digest for the same
  fingerprint;
- pagination/cursor token advanced;
- an explicit freshness or polling policy is in effect for that call
  (tool-declared, not inferred from model text);
- the call is a retry immediately following a transient failure
  (network/timeout error class) on the same fingerprint.

**Threshold and response.** A small bounded repeat count (proposed default:
`3`, matching the existing `agent.max_repeated_failures` default for
consistency — final value is an implementation-time tuning decision, not an
architectural one). On threshold:
1. the repeated call is not executed;
2. a structured tool-result-shaped evidence entry is appended explaining
   the block (so the model sees it as part of normal tool-result flow, not
   a silent failure);
3. the run either receives this as a forcing function to change strategy,
   or — if the same fingerprint is blocked again after a strategy change
   window — the run terminates `no_progress` (ordinary chat: end the batch
   and surface the notice; `/agent on`: feed into `Decide()` as
   deterministic evidence, same as a verifier failure).
4. TUI notice: "Repeated tool call blocked: no new evidence" (master-prompt
   §7.2, verbatim wording as the baseline; final copy is a TUI-polish
   decision).

**Scope.** Ledger state is per-run (ordinary chat: per user-turn/tool-batch
sequence since the last final answer; `/agent on`: per `AgentRun`, spanning
cycles, since the failure mode described in master-prompt §2 can span
multiple cycles' worth of identical searches). This means the ledger's
lifetime does **not** reset when `/agent on`'s `execution.ToolCalls` resets
per cycle (`v1-audit.md` §4.2) — it is intentionally a different, longer-
lived counter from the per-cycle budget counter, and the two must not be
conflated in implementation.

## 4. Live budget enforcement (new)

Per ADR 0002. The check added to `startToolBatch`/`runToolCalls` answers a
strictly narrower question than `agent.Decide()`: "would executing this
batch cross an already-known hard ceiling?" It uses the true run-level
running totals (`AgentRun.ToolCalls`, `AgentRun.PromptTokens +
CompletionTokens`, updated live as today via `finishStream`), not the
per-cycle `execution.ToolCalls` counter that `agentToolBudgetExceeded`
currently reads. `Decide()` is unchanged as the authority for cycle-level
`continue`/`retry`/`done` decisions; the live check only short-circuits
before those are reached, and reports `budget_exhausted` exactly as
`Decide()` would.

## 5. Tool protocol correctness

Confirmed already correct and preserved as-is (`v1-audit.md` §6): tool-call
ID correlation (`EnsureToolCallIDs`), exactly-one-result-per-accepted-call,
stale-event rejection. No changes required here beyond what §3/§4 add as
new evidence entries flowing through the existing result-append path — the
progress ledger's "blocked" entries must use the same `tools.Result`
shape as every other outcome (accepted, denied, timed out, failed) so no
special-cased result type is introduced.

## 6. Regression scenario (master-prompt §9.2)

The deterministic fixture required before any of the above is implemented:
a fake provider that requests detailed weather for Brno-Bystrc, calls
`web_search`, fetches a result, then repeats the same search and fetch with
only minor query variation, with materially unchanged tool output each
time. It must be built against the **ordinary (non-agent) tool loop first**,
since `v1-audit.md` §4.1 confirms that path has zero protection today — this
is the scenario most directly matching the master prompt's reported
failure, not (only) a hypothetical `/agent on` case. A second fixture must
verify a *legitimate* polling/freshness scenario is not blocked. See
[`v1-test-matrix.md`](v1-test-matrix.md) for the full table.
