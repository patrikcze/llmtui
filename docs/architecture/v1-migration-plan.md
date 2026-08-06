# v1 Migration Plan

> **Status update**: the progress ledger, live budget enforcement, and the
> `golang.org/x/text` dependency bump described below have landed
> (`fix(agent): block no-progress tool-call repetition and enforce budgets
> live`, `security(deps): bump golang.org/x/text to close GO-2026-5970`, on
> `feat/v1-agent-runtime`). The rest of this document was written
> prospectively, before implementation, and is left largely as originally
> written since the design it anticipated is what shipped — this note is
> the only change needed to bring it up to date.

## Summary

Based on the audit (`v1-audit.md`), v1.0.0 is a **behavior-hardening**
release, not a rewrite. No orchestration path is being replaced (ADR 0001);
no existing config schema field is being removed or renamed. The changes
that reach users are additive: new default-on protection against
unbounded tool-call repetition, and budget enforcement that becomes
stricter (in the sense of "matches what was already documented") rather
than looser.

## Breaking changes anticipated

**None identified yet at the architecture stage.** This section will be
updated as implementation slices land; per master-prompt §3.2, any
breaking change must be necessary, documented, tested, and covered here
before release — this document is the running ledger for that requirement,
not a one-time write.

The one behavior change users will observe, even though no config field
changes shape:

- **Tool-call repetition that currently runs unchecked will now be capped
  by the progress ledger** (`v1-agent-runtime.md` §3), by default, in both
  ordinary tool chat and `/agent on`. A workflow that relies on
  intentionally repeating an identical tool call (not polling, not
  pagination, not a retry after failure — those are explicitly exempted)
  will see it blocked after the threshold and surfaced as a notice. This
  is the fix for the reported failure mode by design; if it surfaces false
  positives in practice, the threshold/exemption rules are implementation
  tuning, not an architecture change, and should be adjusted rather than
  the protection removed.
- **`/agent on` runs may terminate earlier** than before once live budget
  enforcement lands (`v1-agent-runtime.md` §4), because a tool-call spree
  that previously could exceed `agent.max_tool_calls`/`agent.max_tokens`
  before a cycle boundary caught it will now stop at the true limit. This
  makes behavior match what `docs/agent-loop.md` already documents (see
  `v1-audit.md` §4.3) — it is a bug fix relative to the docs, not a new
  restriction relative to user expectation.

## Configuration migration

No existing YAML fields are renamed, removed, or change meaning. Any new
configuration surface (e.g. a progress-ledger repeat-threshold override, if
one is added beyond the proposed default) will be additive with a safe
default matching the behavior described in `v1-agent-runtime.md`, following
the existing pattern of `agent.max_repeated_failures`. `agent.enabled`
continues to default to `false`; ordinary chat, cache behavior, history,
providers, streaming, tools, approvals, skills, MCP, and RAG continue to
follow their existing path unless a user has enabled tools/agent mode —
consistent with `docs/agent-loop.md`'s existing compatibility statement,
which this release does not change.

## Rollback

Both new mechanisms (progress ledger, live budget enforcement) are
kernel-level additions, not replacements of existing code paths — they can
each be gated behind a config flag defaulting to enabled, so a user who
hits an unexpected false positive can disable the specific mechanism
(e.g. `agent.progress_ledger.enabled: false` or equivalent naming decided
at implementation time) without reverting the release. This is a
deliberate implementation requirement carried from master-prompt §13's
release-gate checklist ("explain how to disable new optional behavior or
revert safely") and should be treated as a hard requirement of the
implementation slice, not an afterthought.

## Dependency changes

`golang.org/x/text` will be bumped from `v0.38.0` to `>= v0.39.0` to close
`GO-2026-5970` (`v1-security-review.md` §3.1). This is a patch-level
dependency bump with no expected API surface change.

## Documentation updates required at implementation time

- `docs/agent-loop.md` lines 74-76: once live budget enforcement lands,
  confirm the existing "run-level tool-call limit" claim is now literally
  true and update only if implementation details (e.g. exact wording of
  the `budget_exhausted` notice) diverge from what's written.
- `docs/security.md`: add a pointer to `docs/architecture/v1-security-review.md`
  for the carried-forward accepted-risk items (§2 of that document) so they
  are not only tracked in an architecture doc a future contributor may not
  think to check.
- New: document the progress-ledger notice and any new config surface in
  `docs/agent-loop.md` and `docs/configuration.md` once implemented.

## Recommended follow-up issues (ordered by priority)

1. ~~Implement the §9.2 regression fixtures (`v1-test-matrix.md`).~~ **Done**
   — `internal/tui/toolloop_progress_test.go`, plus the agent-mode variant
   and the legitimate-repetition counter-fixture.
2. ~~Implement the progress ledger and live budget enforcement
   (`v1-agent-runtime.md` §3-4, ADR 0002).~~ **Done** —
   `internal/tui/progress.go`, `agentHardBudgetExceeded`,
   `terminateAgentBudget`.
3. **Still open**: close ordinary-tool-loop truncation blindness
   (`v1-agent-runtime.md` §2) — the ordinary (non-agent) tool-continuation
   path still does not consult `ChatEvent.Truncated` when deciding whether
   to continue. Not required to close the reported failure mode (the
   progress ledger and live budget enforcement do that independently), so
   it was correctly deferred out of this slice, but it remains a real gap.
4. ~~Bump `golang.org/x/text`.~~ **Done**
   (`security(deps): bump golang.org/x/text to close GO-2026-5970`).
5. Extend `provider.Capabilities` per `v1-provider-capabilities.md` —
   independent of the critical path, can be parallelized.
6. Structural untrusted-content delimiter, replacing the prose preamble
   (`v1-security-review.md` §2 item 2) — accepted risk, not urgent.
7. RAG content-based secret scanner (`v1-security-review.md` §2 item 3) —
   accepted risk, explicitly optional since the original review.
8. Re-verify `approval_policy.go` against the original per-tool/per-path
   granularity goal (`v1-security-review.md` §2 item 1) — needs a direct
   read, not re-derivation.
9. Promote the progress ledger's "no_progress" outcome from a reused
   `DecisionFailed` (with a `no_progress:` reason prefix) to a dedicated
   `agent.DecisionNoProgress` value, if a distinct terminal state proves
   useful for TUI/debug-view purposes beyond the `StopReason` string. This
   session deliberately reused `DecisionFailed` rather than extending
   `internal/agent`'s persisted `Decision` enum, to keep the fix's blast
   radius small — the enum change is a reasonable but separable follow-up,
   not a correctness gap.
10. Consider per-call (not per-batch) progress-ledger filtering for mixed
    batches — see the scoping note in `internal/tui/progress.go`'s package
    doc comment.
11. Provider conformance fixtures and TUI test gaps (`v1-test-matrix.md`
    §9.3, §9.5) — breadth work, not urgent for the release gate.
