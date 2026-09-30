# LLMTUI agent improvement plan — handoff (2026-09-30)

Companion to `llmtui-agent-improvement-plan.md`. It records what a cloud
session implemented, what it deliberately did not, and what has to happen on
the maintainer's machine (real models, GGUF, llama.cpp runtime). Each open
item ends with a ready-to-paste prompt for a local Claude Code session.

Every number below is from the mock-provider fixture
(`LLMTUI_AGENT_AUDIT_TRACE=1 go test ./internal/tui -run 'TestAgentAudit' -v -count=1`).
The fixture measures bytes and completion, not latency. No latency numbers
have been measured.

## 1. Status per step

| Step | What | Branch | PR | State |
|---|---|---|---|---|
| 1 | Cancelled batch keeps completed mutations; the next run gets a receipt | `fix/tui-cancel-keeps-completed-mutations` | #152 | merged (after a master merge resolving `turn_runtime.go`) |
| 2 | Freeze the system prefix for the whole turn | `perf/tui-frozen-system-prefix` | #153 | merged |
| 3 | Stop sending the same evidence three times | `perf/tui-dedupe-evidence` | #154 | merged (retargeted to `master` after #153) |
| 4a | Second llama sequence for control requests | `perf/embedded-control-sequence` | #158 | merged; S3 target met only together with Step 6 (see §3) |
| 4b | Skip the contract for trivial questions | — | — | **not started: needs real-model trials** |
| 4c | Smaller verifier replies | `perf/agentverify-compact-verdict` | see PR | draft; conservative variant, −16% verifier decode tokens, same decisions (see §3) |
| 5 | Oversized tool batch must not fail the run | `fix/tui-bound-oversized-tool-batch` | #155 | merged |
| 6 | Stable system prefix across turns | — | — | **not started: needs template check + your decision on CLAUDE.md rule 5** |
| 7 | Plain-chat mid-stream replay parity | `fix/tui-plain-chat-stream-replay` | #156 | merged |

Fixture outcomes on the PR branches:
- **S1 agent #3:** prefix reuse went from 21% on `master`, to 77% with Step 2, to 90% with Step 3; `copies_of_one_observed_line` went 3 → 1.
- **S4 agent #3:** prefix reuse went from 12% on `master`, to 9% with Step 2 (the fallback), to 62% with Step 3.
- **S4b:** failed on `master` in all four variants; completes with Step 5.
- **S6 plain:** ended the turn on `master`; replays once and completes with Step 7.
- **S7:** covered by Step 1's tests.

A throwaway merge of #153, #154, #155 and #156 applies cleanly. On that tree, `internal/tui` tests pass and all 32 fixture scenarios complete (16 `done` and 16 plain-chat with an empty stop).

## 2. Merge history (done 2026-09-30)

Merged into `master` with merge commits, in this order: #153, #154 (retargeted
to `master`), #155, #156, then #152. #152 conflicted with Step 2 in
`internal/tui/turn_runtime.go` (two hunks, both pure additions). The conflict was
resolved in a master merge commit on its branch that keeps both sides:
Step 1's `userCancelledGen` and cancel helpers, and Step 2's `frozenSystem` and its
resets. On the merged tree, gofmt, vet, golangci-lint (0 issues),
`go test ./...` and the fixture (all 32 scenarios, S7 included) were clean.

The commits are authored as `Claude <noreply@anthropic.com>` and carry no AI
trailers, per CLAUDE.md.

The merged `perf/…`, `fix/…` branches and this `docs/…` branch still exist
on `origin`, because the cloud session cannot delete branches. Delete them locally with
`git push origin --delete <branch>`.

## 3. What was not implemented, and why

- **Step 1, `run_command` content dedup.** Not implemented, by your decision after the oh-my-pi comparison. oh-my-pi has no content dedup; it relies on truthful history, which Step 1 now provides. A narrower key would either miss real repeats or block legitimate reruns, such as a test run after an edit.
- **Step 3 byte targets** (S4 ≤ 23 KB, S1 ≤ 15.5 KB). Not met: the results are 26.5 KB and 17.4 KB. The targets were set on `master`'s layout.
  - Step 2 deliberately keeps the turn's first runtime sections inside the frozen prefix; that is what buys the 77–90% reuse.
  - Getting under the targets means removing runtime sections from the fresh system message, which is Step 6.
  - Everything verbatim-duplicated is already gone (`copies=1`).
- **Step 4a (merged, #158).**
  - **What it does.** Contract and verifier requests carry `provider.ChatRequest.Isolated`. The embedded runtime evaluates them in llama.cpp sequence 1 (`n_seq_max=2`, unified KV) and empties that sequence afterwards, so they no longer evict the executor's cached prompt.
  - **Memory cost.** About +21 MiB on Gemma 4 E4B at 131k context: SWA cache 40 → 60 MiB; full-attention KV unchanged at 2,048 MiB.
  - **Measured locally.** Gemma 4 E4B Q4_K_M, Metal, prefill replay, S3 follow-up executor request:

    | Setup | Reused | Evaluated |
    |---|---|---|
    | Old behaviour (shared) | 5 | 6,125 |
    | 4a (isolated) | **744** | 5,386 |
    | Step 6 simulated, shared | 5 | 6,196 (10.6 s) |
    | Step 6 simulated + 4a | **4,891** | 1,310 (1.6 s) |

    744 is also the executor-only upper bound, so 4a removes all eviction. The next run still diverges at the directive's `Original goal` line in the fresh system message; removing that is Step 6's job.
  - **Acceptance.** The ≥ 5,000 target is not met by 4a alone. It is reached (4,891) together with Step 6. The target predates the prompt shrink from Steps 2–3.
  - **Within one run (S1/S5/S8).** No change, because the contract precedes the cold first executor request and the verifier comes last.
- **Step 4b.** The plan forbids it until real-model trials show no increase in false completions.
- **Step 4c (draft PR).**
  - **Scope, deliberately conservative.**
    - A later-cycle verification is no longer asked for the establishing-only `proposed_criteria` and `atomic_task`; schema and prompt now come in later-cycle and establishing variants.
    - The parser accepts an omitted `recommended_next` or `needs_user_input`, parsing them exactly like `""` / `false`.
    - `retryable` and `criteria` stay required: omitting either changes stop decisions. A defaulted `retryable` reads as "impossible". Criteria entries, even echoed `pending`, feed the inferred `new_evidence` that lets a retry through.
  - **Measured.** Gemma 4 E4B, unconstrained decode of the verifier requests from 7 fixture scenarios:

    | Variant | Verifier completion tokens | Decisions |
    |---|---|---|
    | Old prompt | 774 | — |
    | Shipped variant | 651 (−16%) | identical in 7/7: verdict, retryable, needs_user_input, criteria statuses; `recommended_next` present in the same 3 inconclusive cases |
    | Aggressive ("omit anything empty"; not shipped) | 503–542 (−30–35%) | changed 1/7 (S2: retryable true→false, `c1` pending→satisfied); one reply omitted `criteria`, which forces an extra repair request |

  - **Request size.** −33 bytes per verifier request.
  - **Wall time.** No reliable difference measured; host throughput varied between runs.
- **Step 5 scope.** Only native-tool continuations are bounded. Fenced-protocol results travel in a user message and still fail as before when oversized.
- **Step 6.** Needs the template check (two consecutive user turns) on your GGUFs and a decision on CLAUDE.md rule 5.
- **Step 7 scope.** Only native-tool continuations in plain chat are replayed. The first request of a plain turn keeps its partial reply, as before. Fenced continuations are not replayed.
- **Flaky test.** `TestToolOutputExpansionPreservesScrollAndSanitizes` (stale bubblezone zone) fails intermittently. It was queued as a separate task and has not been fixed.
- **Environmental failures in the cloud container**, unrelated to these PRs:
  - `internal/procutil` `TestTerminateReapsStubbornGrandchild`;
  - `internal/tools` `TestRunCommandKillsBackgroundDescendants`.
  - Both are intermittent there. If they fail on your Mac too, that is a real bug.

## 4. Local verification after merging (your machine)

Run these on a fresh `master` (`git checkout master && git pull`):

```bash
make check
golangci-lint run ./...        # make lint silently skips when golangci-lint is absent
go test -race -count=1 ./internal/agent/... ./internal/agentverify/... \
  ./internal/mcp/... ./internal/provider/... ./internal/tools/... ./internal/tui/...
LLMTUI_AGENT_AUDIT_TRACE=1 go test ./internal/tui -run 'TestAgentAudit' -v -count=1
```

**Real prefill replay** (plan, "Local re-measurement"). This is the only source of latency numbers:

```bash
DIR=$(mktemp -d)
LLMTUI_AGENT_AUDIT_TRACE=1 LLMTUI_AGENT_AUDIT_DUMP=$DIR go test ./internal/tui -run TestAgentAuditTrace -count=1
LLMTUI_AGENT_AUDIT_REPLAY=$DIR LLMTUI_AGENT_AUDIT_TRIALS=2 LLMTUI_AGENT_AUDIT_DECODE=1 \
  YZMA_LIB=$RUNTIME LLMTUI_TEST_GGUF=$GGUF CGO_ENABLED=1 \
  go test ./internal/provider/embedded/llamart -run TestAgentAuditPrefillReplay -v -count=1 -timeout 120m
```

Run it once on `b3208c8` (the baseline) and once on the new `master`, then compare per-request prefill tokens and ms.

**Manual checks in LM Studio**, with the embedded provider too if you have a GGUF:

1. **Step 4a (embedded only).**
   - Run two consecutive `/agent` tasks with the embedded provider.
   - The second task's first executor request should report prompt progress that starts well past 0, not a full re-process.
   - `TestIsolatedRequestPreservesConversationPrefix` is the automated form of this check; it needs `YZMA_LIB` and `LLMTUI_TEST_GGUF`.
2. **Step 1.**
   - Run `/agent` with a task that writes a file and then runs more tools.
   - Press `Esc` or `Ctrl+C` after the write finished but before the batch ends.
   - Type `continue`. The run must not redo the write. `/debug last` should show the `[llmtui receipt, not a user request] …` block and the carried call/result pair.
3. **Step 2/3.**
   - Run a multi-round `/agent` task.
   - In `/debug last`, the system message must be byte-identical across the turn's continuations.
   - LM Studio's log should show large cached-prefix hits on continuations.
4. **Step 5.**
   - Set `context.max_context_tokens: 8192` and ask for a summary of three ~120-line files in one go.
   - The turn completes, with at least one `[truncated to fit the context window …]` result and a `next_offset` the model can use.
5. **Step 7.**
   - Hard to trigger by hand. One way: in plain chat with tools on, kill or restart the LM Studio server while it streams the answer after a tool call.
   - Expect the notice `provider stream was interrupted — replaying the request once`.

## 5. Prompts for a local Claude Code session

Each prompt stands alone. Start each in a fresh session at the repo root on an up-to-date `master`.

### 5.2 Step 4a — second llama sequence for control requests (local, native)

```text
Read CLAUDE.md, .claude/tasks/plans/llmtui-agent-improvement-plan.md (Step 4
and "Read this first") and .claude/tasks/plans/llmtui-agent-improvement-handoff.md,
plus the package docs of internal/provider/embedded and
internal/provider/embedded/llamart. Implement Step 4a on branch
perf/embedded-control-sequence.

Today the embedded runtime has one KV sequence: Runtime.kvTokens
(llamart/runtime.go:69) and preparePrompt (llamart/generate.go:321) trim
sequence 0 with MemorySeqRm, and embedded.go:54 genMu serializes every
generation. So contract/verifier/summarizer requests displace the executor
prefix. Goal: run control requests on sequence 1 (context n_seq_max=2,
per-sequence kvTokens), so sequence 0 keeps the executor prefix.

Constraints:
- All native contact stays in internal/provider/embedded/llamart; no cgo.
- The provider needs a way to know a request is a control request.
  provider.ChatRequest has no such field today. Propose the smallest
  addition (for example a documented request hint) and check that
  internal/tui/agent_loop.go sets it for contract, verifier and summarizer
  calls only. Stop and ask me before changing the provider contract if
  it is more than one optional field.
- Account for per-sequence memory: check the n_ctx split and that vision
  (kvContaminated) clearing still clears the right sequence.
- Do not touch internal/runtime/pin.json, third_party/, or .github/workflows.

Tests: unit-test the per-sequence prefix bookkeeping with the package's fake
runtime (see llamart/runtime_test.go), in the cloud-safe path. Then run
locally with my runtime and model:
  YZMA_LIB=$RUNTIME LLMTUI_TEST_GGUF=$GGUF LLMTUI_TEST_CPU=... go test ./internal/provider/embedded/llamart -v -count=1
and the prefill replay from the handoff §4. Acceptance: in the S3 replay the
follow-up executor request reuses >= 5,000 tokens. Report the measured
numbers exactly, without extrapolation. Gates: gofmt, vet, golangci-lint,
go test ./..., race subset. Conventional Commits (perf(embedded): ...), no AI
trailers, one draft PR.
```

### 5.3 Step 4c — smaller verifier replies (parser in the cloud or locally; decode savings locally)

```text
Read CLAUDE.md, the plan's Step 4 and the handoff. Read the internal/agentverify
and internal/agent package docs. Implement Step 4c on branch
perf/agentverify-compact-verdict.

internal/agentverify/verifier.go:47 declares eight required fields and the
prompt at :409 demands all eight. Let the verifier omit empty arrays
(criteria, proposed_criteria) and false/empty defaults where the parser can
supply them, and keep verdict, summary and every field whose absence would change
a decision required. Update the JSON schema (the response constraint), the
prompt text and Parse (:502) together. A missing optional field must parse to
exactly what an explicit empty value parses to today. Keep ErrMalformedControl for
genuinely missing required fields.

Tests (stdlib testing, table-driven): every omitted optional field equals
its explicit-empty form; missing required fields still fail; the existing
verifier tests still pass unchanged. Run the fixture and paste the verifier
request/response byte changes. Locally, measure decode savings with
LLMTUI_AGENT_AUDIT_DECODE=1 in the prefill replay (handoff §4) and report
only what it measures. Gates as in CLAUDE.md. One draft PR, no AI trailers.
```

### 5.4 Step 4b — skip the contract for trivial questions (trials first; no code until the data says so)

```text
Read CLAUDE.md, the plan's Step 4 and the handoff. Do NOT implement Step 4b
yet. First design a small real-model trial: 20 tasks split between trivial
questions and tool tasks. Run it with the contract and without (behind a temporary
local-only config flag on a scratch branch that is never pushed). Use my LM Studio
model(s) and, if available, the embedded GGUF. Record per task: final status,
verifier verdict, and whether the answer was actually correct (I will judge
the ones you cannot). Report the false-completion rate for both arms. Only if
the no-contract arm shows no increase, propose the classifier rule and
open a draft PR on perf/agent-skip-trivial-contract, with the trial table in
the body. Otherwise report the numbers and stop.
```

### 5.5 Step 6 — stable system prefix across turns (template check, then my decision)

```text
Read CLAUDE.md (Architecture rule 5), docs/prompt-composition.md, the plan's
Step 6 and the handoff. Part 1 (no code change): for each GGUF I use, render
its chat template with two consecutive user-role messages (a runtime-context
message, then the raw user message) and report whether the template accepts
it, merges them, or errors. Use the repo's existing template handling in
internal/provider/embedded/llamart (template_test.go shows how). Also check
what LM Studio and Ollama do with two consecutive user messages for my
models. Write the findings as a short table and STOP. I need to decide on
rule 5 before any implementation.

Part 2 (only after I approve): on branch perf/prompt-runtime-out-of-fresh-system,
move runtime sections out of the fresh request's system message into a
runtime-context user message before the raw user message. Gate it per
template for embedded (only templates that passed Part 1). Keep remote
providers on the Step 2 behaviour unless the user opts in via config. The raw user message
stays last and verbatim. Update CLAUDE.md rule 5, docs/prompt-composition.md
and the internal/prompt package doc. Acceptance (fixture): the S3 second-run
executor keeps >= 70% of the prefix (was 20%), and the S1/S4 byte targets
from Step 3 are re-measured and reported. Gates as in CLAUDE.md. One draft PR, no
AI trailers.
```

### 5.6 Flaky TUI test

```text
Read CLAUDE.md. internal/tui TestToolOutputExpansionPreservesScrollAndSanitizes
fails intermittently (stale bubblezone zone after re-render). Reproduce with
go test -count=200 -run TestToolOutputExpansionPreservesScrollAndSanitizes ./internal/tui.
Find why the zone is stale: the test clicking before zones are scanned, or
a real bug where the TUI reads a zone from the previous frame. Fix the
root cause without sleeps or skipping the test. If it is a real UI bug, fix
the UI and keep the test. Branch fix/tui-tool-output-zone-flake, one draft
PR, no AI trailers.
```

### 5.7 Fenced-protocol parity for Steps 5 and 7 (optional, only if you use fenced tools)

```text
Read CLAUDE.md, the handoff §3 and internal/tui/tool_result_budget.go. Steps 5
and 7 cover native-tool continuations only. Check whether fenced-protocol
tool results (sent as a user message) can also overflow the window, or lose
work on a mid-stream drop, with my fenced-only models. If so, propose
the smallest extension, with the same invariants: a cut read records only
delivered lines, and a replay never re-executes a tool. Stop for my approval
before coding.
```
