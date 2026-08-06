# v1 Migration Plan

> **Status update**: the progress ledger, live budget enforcement, the
> `golang.org/x/text` dependency bump, independent config toggles for both
> new mechanisms, the ordinary-tool-loop truncation fix, and the dedicated
> `agent.DecisionNoProgress` outcome have all landed on
> `feat/v1-agent-runtime` (`fix(agent): block no-progress tool-call
> repetition and enforce budgets live`, `security(deps): bump
> golang.org/x/text to close GO-2026-5970`, `fix(agent): close remaining
> findings — config toggles, truncation, no_progress`). The rest of this
> document was written prospectively, before implementation, and is left
> largely as originally written since the design it anticipated is what
> shipped — this note is the only change needed to bring it up to date.
> The rollback story below (previously "planned") is implemented and
> tested, not merely described.
> The final slice also adds tri-state provider capability overrides,
> structural framing for RAG/web/MCP content, content-based RAG secret
> scanning, and per-call filtering for mixed tool batches.

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

Provider capability overrides under `providers.<name>.capabilities` are
additive and optional. Omitted booleans retain the tri-state `unknown`
behavior; explicit `false` is not conflated with omission.

The RAG index schema is now versioned and secret-scanned during load.
Unversioned pre-scanner indexes are intentionally rejected and must be
rebuilt with `/rag index`; source files and configuration are unaffected.

## Rollback

Both new mechanisms (progress ledger, live budget enforcement) are
kernel-level additions, not replacements of existing code paths, and each
is gated behind its own config flag, defaulting to enabled:

- `tools.no_progress.enabled: false` disables the progress ledger,
  reverting to pre-v1 pass-through behavior. `tools.no_progress.threshold`
  is the softer alternative — raise it instead of disabling outright if a
  legitimate pattern is being blocked.
- `agent.enforce_budgets_live: false` disables the live budget check,
  reverting to the pre-v1 cycle-boundary-only check `agent.Decide()`
  already performs.

A user who hits an unexpected false positive can disable the specific
mechanism without reverting the release. Both toggles are proven to
actually disable their mechanism by dedicated tests
(`TestNoProgressDetectionCanBeDisabledViaConfig`,
`TestLiveToolBudgetEnforcementCanBeDisabledViaConfig`), fulfilling
master-prompt §13's release-gate requirement to "explain how to disable
new optional behavior or revert safely."

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
3. ~~Close ordinary-tool-loop truncation blindness (`v1-agent-runtime.md`
   §2).~~ **Done** — a tool call truncated by `max_tokens` is now rejected
   before execution in both ordinary tool chat and `/agent on`
   (`TestTruncatedNativeToolCallIsNotExecuted`,
   `TestVerifiedAgentTruncatedToolCallIsNotExecuted`).
4. ~~Bump `golang.org/x/text`.~~ **Done**
   (`security(deps): bump golang.org/x/text to close GO-2026-5970`).
5. ~~Extend `provider.Capabilities` per `v1-provider-capabilities.md`.~~
   **Done** — tri-state model capabilities, explicit overrides, runtime
   request shaping, selected-model embedded detection, and model-scoped
   rejection learning.
6. ~~Structural untrusted-content delimiter, replacing the prose preamble.~~
   **Done** — deterministic collision-checked framing for RAG, web, and MCP.
7. ~~RAG content-based secret scanner.~~ **Done** — high-confidence
   content scanning plus persisted-index version/integrity enforcement.
8. Re-verify `approval_policy.go` against the original per-tool/per-path
   granularity goal (`v1-security-review.md` §2 item 1) — needs a direct
   read, not re-derivation.
9. ~~Promote the progress ledger's "no_progress" outcome to a dedicated
   `agent.DecisionNoProgress` value.~~ **Done** — a run blocked by the
   progress ledger's terminal streak now reports status `no_progress`,
   distinct from `failed` and `budget_exhausted`.
10. ~~Consider per-call (not per-batch) progress-ledger filtering for mixed
    batches.~~ **Done** — immutable positional plans block only stuck calls,
    execute fresh calls, and atomically merge exactly one ordered result per
    call. Synthetic block results never reset the ledger.
11. Provider conformance fixtures and TUI test gaps (`v1-test-matrix.md`
    §9.3, §9.5) — breadth work, not urgent for the release gate.
