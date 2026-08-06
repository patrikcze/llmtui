# v1 Test Matrix

Maps master-prompt §9's required test coverage against what exists at
baseline `0458b5f`, so implementation work has a concrete gap list instead
of a vague "add more tests" instruction. Existing test names are cited
directly (verified via `grep -n "^func Test"` against the actual test
files, not reconstructed from memory).

## 9.1 State-machine / unit tests

| Required case | Existing coverage | Status |
| --- | --- | --- |
| Final response after zero tools | implicit in every non-tool test path | Covered |
| One tool then final response | `TestNativeToolCallsExecuteAndContinue` | Covered |
| Multiple sequential tools | `TestMixedBatchRunsAsyncAndDeliversResults` | Covered |
| Multiple tool calls in one model turn | `TestPureNativeBatchRunsAsync` | Covered |
| Denied approval | `TestDenyPendingToolsReportsToModel` | Covered |
| Cancelled approval | `TestNativeCommandBatchCancelViaEsc`, `TestMCPBatchCancelViaEsc` | Covered |
| Command timeout | not found in `toolloop_test.go`/`tools` package by name | **Gap** |
| Malformed tool arguments | referenced via BUG-005/007 fix (`ed91570`) but no test name matched directly in this pass | Needs confirmation |
| Unknown tool | covered by MCP unknown-tool suggestion work (`mcp_tools.go`); exact test name not confirmed in this pass | Needs confirmation |
| Duplicate result | `TestToolCallIDsBackfilledConsistently` | Covered |
| Missing result | not found by name | **Gap** |
| Stale stream event | `TestStaleStreamEventIsDropped`, `TestStaleFirstStreamMsgNotAdopted` | Covered |
| Stale tool completion | `TestMCPStaleResultsDroppedAfterPlainEsc`, `TestMCPStaleBatchDoesNotClobberResendBatch` | Covered |
| Provider disconnect | covered at provider-package level (streaming parser tests), not re-verified in this pass | Needs confirmation |
| Truncated tool call | agent-mode: covered by truncation-signal work (`ErrorTruncated`, commits `c5766af`…`0458b5f`). **Ordinary tool loop: not covered — the path doesn't consult `ChatEvent.Truncated` at all (`v1-audit.md` §2, `v1-agent-runtime.md` §2).** | **Gap (ordinary mode)** |
| Context exhaustion | `contextmgr` package tests (not re-enumerated here) | Covered at package level |
| Maximum turns | `TestIterationCapAsksUserToContinue`, `TestIterationCapDeclineAsksModelToWrapUp` | Covered |
| Maximum cycles | `TestCancellationTimeoutAndMaximumCycle` | Covered |
| Maximum tool calls | `TestBudgetsAndPermissionDenial` — but only exercises the cycle-boundary check, not the confirmed live-enforcement gap (`v1-audit.md` §4.2) | **Gap: no test for the live-enforcement defect itself** |
| Token budget | `TestTokenBudgetEnforcement` — same caveat as above | **Gap: same** |
| Elapsed-time budget | covered via `resetAgentContext`'s live timeout; explicit test not confirmed by name in this pass | Needs confirmation |
| No progress | none | **Gap — does not exist** (`v1-audit.md` §8) |
| Repeated-call loop | `TestRepeatedFailureStopsAtBound` covers *verifier-failure* repetition, not *tool-call* repetition | **Gap — tool-call-level repetition untested** |
| Verifier malformed output | `TestMalformedControlOutput` | Covered |
| Verifier contradiction with deterministic evidence | `TestDeterministicFailureOverridesOptimisticModel`, `TestDeterministicFailureOverridesTruncatedExecutorReply` | Covered |
| Resume after input | `TestResumeStartsFreshCycleWithoutReplayingWork` | Covered |
| Safe cancellation | `TestCancellationTimeoutAndMaximumCycle`, `TestVerifierTimeoutAndCancellation` | Covered |
| Persistence corruption | `TestFileStoreCorruptRecovery` | Covered |
| Schema migration | not found by name | **Gap** (low priority: `agent.store` is versioned but no migration-path test exists yet) |

## 9.2 Regression scenario for the reported failure (master-prompt §9.2)

**Does not exist.** This is the single most important item in this matrix
— per master-prompt's operating principle §3.1 and §2, it must be written
**before** the progress-ledger/live-budget implementation lands, not after.
Required fixture, built against the **ordinary (non-agent) tool loop
first** (`v1-agent-runtime.md` §6):

1. Fake/mock provider (`internal/provider/mock` already exists as a test
   double package) scripted to: request weather for Brno-Bystrc, call
   `web_search`, fetch a result, then repeat the same search and fetch with
   only minor query variation, with materially unchanged tool output each
   time.
2. Assert: duplicate/no-progress detection activates within a small bounded
   call count; identical search/fetch calls are not executed indefinitely;
   the model receives structured feedback (a `tools.Result` shaped block,
   not a silent drop); the run either changes strategy and produces a final
   answer, or terminates with a clear `no_progress` outcome; token/tool-call
   totals stay far below the global maximum; cancellation still works;
   the trace/notice clearly explains the decision.
3. A second fixture must assert a **legitimate** polling/freshness scenario
   is *not* blocked (master-prompt §9.2, "also test a legitimate polling or
   freshness scenario") — e.g. a tool call whose result digest changes each
   time, or one explicitly marked freshness-required, must not trip the
   ledger.
4. A third fixture should repeat scenario 1 inside `/agent on` mode, to
   confirm the live budget check (`v1-agent-runtime.md` §4) actually stops
   a tool-call spree before a cycle boundary, closing the specific gap
   re-verified in `v1-audit.md` §4.2.

## 9.3 Provider conformance fixtures

Not enumerated item-by-item in this pass (out of scope for the immediate
regression-test priority). Existing provider-level streaming/parser tests
were confirmed to exist (`internal/provider/openai`, `internal/provider/ollama`
packages all pass at baseline, `v1-audit.md` §7) but were not cross-checked
against every fixture master-prompt §9.3 lists (parallel tool calls,
duplicate IDs, refusal/safety output, backend rejecting tool schemas,
etc.). Recommended as a follow-up audit pass once the §9.2 regression test
lands, since it is independent of the critical-path fix.

## 9.4 Fuzz and race tests

`go test -race ./...` passes clean at baseline (`v1-audit.md` §7) — this is
existing coverage breadth, not per-target fuzz coverage. No fuzz targets
were found for streamed JSON, SSE fields, tool arguments, or path/URL
validation in this pass. Recommended as a follow-up, not blocking the
critical-path fix (master-prompt's own §14 token-discipline guidance:
"prefer small changes with measurable benefit" — fuzzing the reported
failure mode's parsers is lower-value than closing the confirmed gap
itself).

## 9.5 TUI tests

Not audited in this pass — `internal/tui/components` has its own test
package (passes at baseline) but was not cross-checked against every UI
scenario master-prompt §9.5 lists. Recommended as a follow-up once the
progress-ledger TUI notice (`v1-agent-runtime.md` §3, "Repeated tool call
blocked: no new evidence") is implemented, since that notice itself needs
a rendering test.

## 9.6 Manual compatibility matrix

Not performed in this pass — requires live provider instances
(Ollama/LM Studio/etc.) that are not available in this environment.
Tracked as an explicit open item for whoever has access to those backends
before the v1.0.0 release gate (master-prompt §13 requires this
explicitly and requires it be reported honestly, not assumed).

## Priority order for implementation

1. §9.2 regression fixture (ordinary-mode repeated-call scenario) — must
   exist before any implementation change, per master-prompt §3.1/§2.
2. §9.2 legitimate-repetition fixture — prevents the fix from being
   overzealous.
3. §9.1 gaps directly tied to the confirmed defects (`v1-audit.md` §4.1,
   §4.2): no-progress, tool-call-level repeated-call, live budget
   enforcement, ordinary-mode truncation handling.
4. §9.2 agent-mode variant of the regression fixture.
5. Everything else in this matrix (§9.3-9.6) — genuine gaps, but not on the
   critical path to closing the reported failure mode, and should not
   block the v1 slices that do close it.
