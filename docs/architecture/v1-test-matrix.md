# v1 Test Matrix

Maps master-prompt §9's required test coverage against what exists at
baseline `0458b5f`, so implementation work has a concrete gap list instead
of a vague "add more tests" instruction. Existing test names are cited
directly (verified via `grep -n "^func Test"` against the actual test
files, not reconstructed from memory).

> **Final-slice update:** the ordinary/agent repeated-call fixtures,
> legitimate-changing-evidence counter-fixture, live tool-budget fixture,
> truncation-before-execution fixtures, mixed-batch filtering tests, provider
> capability tests, structural framing tests, and RAG secret/index migration
> tests now exist and pass. Rows below retain unrelated baseline gaps so the
> document remains an honest release checklist.

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
| Truncated tool call | `TestTruncatedNativeToolCallIsNotExecuted`, `TestVerifiedAgentTruncatedToolCallIsNotExecuted` | Covered in ordinary and agent modes |
| Context exhaustion | `contextmgr` package tests (not re-enumerated here) | Covered at package level |
| Maximum turns | `TestIterationCapAsksUserToContinue`, `TestIterationCapDeclineAsksModelToWrapUp` | Covered |
| Maximum cycles | `TestCancellationTimeoutAndMaximumCycle` | Covered |
| Maximum tool calls | `TestBudgetsAndPermissionDenial`, `TestVerifiedAgentLiveToolBudgetStopsExecutionBeforeCycleBoundary`, rollback-toggle fixture | Covered at cycle boundary and live |
| Token budget | `TestTokenBudgetEnforcement`; live check shares the tested pre-execution gate | Covered |
| Elapsed-time budget | covered via `resetAgentContext`'s live timeout; explicit test not confirmed by name in this pass | Needs confirmation |
| No progress | ordinary/agent bounded-loop fixtures plus terminal result-correlation tests in `toolloop_progress_test.go` | Covered |
| Repeated-call loop | `TestRepeatedWebSearchFetchLoopIsBoundedInOrdinaryToolMode`, agent variant, and legitimate-changing-evidence counter-fixture | Covered |
| Verifier malformed output | `TestMalformedControlOutput` | Covered |
| Verifier contradiction with deterministic evidence | `TestDeterministicFailureOverridesOptimisticModel`, `TestDeterministicFailureOverridesTruncatedExecutorReply` | Covered |
| Resume after input | `TestResumeStartsFreshCycleWithoutReplayingWork` | Covered |
| Safe cancellation | `TestCancellationTimeoutAndMaximumCycle`, `TestVerifierTimeoutAndCancellation` | Covered |
| Persistence corruption | `TestFileStoreCorruptRecovery` | Covered |
| Schema migration | not found by name | **Gap** (low priority: `agent.store` is versioned but no migration-path test exists yet) |

## 9.2 Regression scenario for the reported failure (master-prompt §9.2)

**Implemented.** `internal/tui/toolloop_progress_test.go` contains the
ordinary-mode fixture first, the `/agent on` variant, and the legitimate
changing-evidence counter-fixture. Together they prove:

1. A scripted provider requests weather for Brno-Bystrc, calls
   `web_search`, fetch a result, then repeat the same search and fetch with
   only minor query variation, with materially unchanged tool output each
   time.
2. Duplicate/no-progress detection activates within a small bounded
   call count; identical search/fetch calls are not executed indefinitely;
   the model receives structured feedback (a `tools.Result` shaped block,
   not a silent drop); the run either changes strategy and produces a final
   answer, or terminates with a clear `no_progress` outcome; token/tool-call
   totals stay far below the global maximum; cancellation still works;
   the trace/notice clearly explains the decision.
3. A repeated search whose result changes each time is not blocked.
4. The same stuck scenario inside `/agent on` terminates with the dedicated
   `no_progress` outcome.

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

Not exhaustively audited in this pass — `internal/tui/components` has its own
test package and the progress-ledger notice/controller behavior is covered,
but every visual scenario in master-prompt §9.5 was not cross-checked. A
dedicated rendered-notice test remains useful follow-up breadth work.

## 9.6 Manual compatibility matrix

Attempted on 2026-08-06. The deterministic provider fixtures pass, but no
live backend was available, so the release-gate matrix remains explicitly
open rather than being inferred from unit tests:

| Provider | Environment evidence | Manual result |
| --- | --- | --- |
| Ollama | CLI installed; `127.0.0.1:11434` not listening | Not run |
| LM Studio | `127.0.0.1:1234` not listening | Not run |
| Generic OpenAI-compatible | `127.0.0.1:8080` not listening | Not run |
| Embedded GGUF | no `.gguf` fixture in the repository; `YZMA_LIB` unset | Not run |

Run the prompt/stream/tool/reasoning checks against real instances before
v1.0.0. This is the sole environment-dependent item in this matrix; it is
not silently marked complete.

## Original priority order for implementation

1. ~~§9.2 ordinary-mode repeated-call regression fixture.~~ Done.
2. ~~§9.2 legitimate-repetition fixture.~~ Done.
3. ~~Direct confirmed defects: no-progress, tool-call-level repetition,
   live budget enforcement, ordinary-mode truncation handling.~~ Done.
4. ~~§9.2 agent-mode variant.~~ Done.
5. Everything else in this matrix (§9.3-9.6) — genuine gaps, but not on the
   critical path to closing the reported failure mode, and should not
   block the v1 slices that do close it.
