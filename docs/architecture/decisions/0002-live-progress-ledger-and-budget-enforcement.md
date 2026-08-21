# ADR 0002: Add a live, kernel-level progress ledger; move budget enforcement off the cycle boundary

Status: Accepted
Date: baseline commit `0458b5f`, branch `feat/v1-agent-runtime`

## Context

`v1-audit.md` §4.1-4.2 confirms two compounding defects, re-verified
directly against source:

1. No canonicalized fingerprint of `(tool, args, resource)` exists anywhere
   in the codebase. Nothing distinguishes a legitimately repeated tool call
   (pagination, polling, a retry after a transient failure) from a stuck
   loop.
2. `agent.Decide()` — the only place hard budgets (`max_tool_calls`,
   `max_tokens`, `max_repeated_failures`) are evaluated — is only reachable
   from `handleAgentVerification`, which is only reached when a turn
   produces zero tool calls. A model that keeps emitting tool calls every
   turn never reaches this check. The live per-round guard that does exist
   (`agentToolBudgetExceeded`) compares a counter reset to empty every cycle
   against a limit the docs describe as run-level, so it can under-enforce
   by up to `max_cycles` × `max_tool_calls` calls in the worst case.

Both defects are structural gaps, not bugs in existing logic — there is
nothing to "fix" in the sense of correcting wrong behavior; the behavior
that should exist does not exist yet.

## Decision

1. **Progress ledger.** Introduce a per-run progress ledger, owned by the
   shared kernel (not `internal/agent`-only), that canonicalizes and
   fingerprints every tool call using at minimum: tool identity, canonical
   validated arguments, the relevant resource identity (path/URL/query),
   the tool's result class, and a result digest — per master-prompt §7.2.
   It applies to **both** the ordinary tool loop (b) and `/agent on` (c),
   because defect 4.1 exists in both.
   - Repetition is not banned outright. The ledger tracks whether a repeat
     call produced materially new evidence (different result digest,
     advanced pagination token, explicit freshness/polling request, or a
     retry immediately following a transient failure). Only a repeat with
     no new evidence, past a small bounded threshold, blocks the call.
   - When blocked: the call is not executed, a structured evidence entry
     explains why, and the model/state machine is required to change
     strategy, change arguments, or the run terminates as `no_progress` /
     `needs_user_input`.
   - A visible TUI notice is shown ("Repeated tool call blocked: no new
     evidence"), per master-prompt §7.2.
2. **Live budget checks.** Move the hard-budget comparisons
   (`MaxToolCalls`, `MaxTokens`) that currently live only inside
   `agent.Decide()` into a check invoked from `startToolBatch`/
   `runToolPlan` on every round, using the true run-level running totals
   (not the per-cycle-reset `execution.ToolCalls` counter). `Decide()`
   remains the authority for cycle-level stop-policy decisions
   (`continue`/`retry`/`done`/etc.); the live check only needs to answer
   "has a hard ceiling already been crossed," which is a strict subset of
   what `Decide()` computes.
3. Both mechanisms are part of the shared kernel's deterministic transition
   decision (master-prompt §6.2's target shape: "Deterministic transition
   decision → Continue, pause, verify, finish, cancel, or fail"), not new
   `/agent`-only state.

## Consequences

- `docs/agent-loop.md`'s existing claim ("Agent mode adds a total run-level
  tool-call limit... Reaching the run limit... rejects further calls")
  becomes literally true instead of only true at cycle boundaries; no
  documentation rewrite is needed once the implementation lands, only a
  changelog/migration note.
- The ordinary (non-agent) tool loop gains no-progress protection it did
  not have before. This is new default-on behavior and must be documented
  as such in `v1-migration-plan.md` — it can change observable behavior for
  existing users whose workflows include intentional repeated calls
  (polling, pagination), so the "legitimate repetition" carve-outs above
  are a hard requirement, not a nice-to-have.
- Because the ledger is fingerprint-based and kernel-owned, `/agent on`'s
  existing `RepeatedFailures`/`FailureKey` mechanism (which fingerprints
  verifier-outcome text, not tool calls) is complementary, not replaced —
  it continues to catch the "verifier keeps recommending the same
  objective" case that the tool-call ledger cannot see.
- This is additive to the state machine in terms of *data* (a ledger keyed
  per run) but must not become a second decision authority: `Decide()`
  remains the single place cycle-level stop decisions are made, consistent
  with ADR 0001.
