# LLMTUI agent improvement plan (2026-09-30)

Audit baseline: master `b3208c8` (v1.0.39). This file is the self-contained
handoff for implementing the fixes. The full audit with raw measurements stays
local with the maintainer; everything an implementer needs is below.

## Read this first (implementer in a cloud / CI session)

- A cloud session has **no LM Studio, Ollama or embedded llama.cpp runtime**.
  Every acceptance criterion marked "fixture" below uses the **mock-provider
  trace fixture**, which needs nothing but `go test`:

  ```bash
  LLMTUI_AGENT_AUDIT_TRACE=1 go test ./internal/tui -run 'TestAgentAudit' -v -count=1
  ```

  `internal/tui/agent_audit_trace_test.go` drives the real TUI pipeline
  (composition, context budget, tool execution in a temp dir, yield, verifier
  routing) with a scripted provider and default config. It runs four variants:
  {http, embedded provider type} × {agent, plain chat}. For each request it logs:
  - kind (contract/executor/verifier) and serialized bytes;
  - the byte prefix shared with the previous executor request (`reused_prefix`)
    and `system_changed`;
  - `copies_of_one_observed_line`.

  `TestAgentAuditCancelAfterCompletedMutation` is scenario S7. Both tests skip
  unless the env var is set. Byte counts are deterministic, so they are valid
  acceptance numbers on any machine.
- Real-model timing (`internal/provider/embedded/llamart/audit_prefill_integration_test.go`,
  env-gated, needs a GGUF + llama.cpp runtime) **cannot run in the cloud**. Do
  not claim latency numbers in a PR. The maintainer re-runs the replay locally
  after merge; criteria marked "local" are theirs.
- Repo rules apply:
  - Read `CLAUDE.md` and the package doc of every package you touch.
  - One branch + one PR per step. Conventional Commits scoped to the package.
    **No AI-attribution trailers.**
  - `make check`, the race subset, `gofmt -l .` (empty) and
    `golangci-lint run ./...` (0 issues) must be green.
  - Steps 1 and 5 touch the tool/approval flow. Keep every Workspace Tool
    Safety Invariant and add the regression tests CLAUDE.md requires.
- Update the docs describing changed behaviour: `docs/prompt-composition.md`,
  `docs/agent-loop.md`, CLAUDE.md "Architecture" rule 5, and the
  `internal/prompt` package doc.
- When a step changes a number the fixture logs, paste the before/after fixture
  lines into the PR body.

## Baseline the fixture reports today (b3208c8)

| Scenario | Mode | Requests (contract/exec/verifier) | Prefix kept after first successful tool round | Notes |
|---|---|---|---|---|
| S8 one-word answer | agent | 3 (1/1/1) | — | plain chat: 1 request |
| S1 last line of a 1,500-line file | agent http | 4 (1/2/1) | 21 %, `system_changed=true` | observed line sent 3× |
| S1 | agent embedded | 4 | 17 %, `system_changed=true` | |
| S1 | plain http | 2 | 30 %, `system_changed=true` | observed line sent 2× |
| S2 non-numeric offset → past EOF → correct read | agent http | 6 (1/4/1) | 99 % on rejected rounds, 21 % after the successful read | harness handles it; extra rounds are model-driven |
| S3 follow-up (second run) | agent http | 4 + 3 | 20 % | prior evidence visible only through the prior answer text |
| S4 three 40-line reads in one batch | agent http | 4 | 12 %; request 16.5 KB → 29.7 KB | |
| S4b three 120-line reads, 8k fallback window | agent & plain | 2 | — | **run fails**: "estimated request is … tokens but only 7680 are available" |
| S5 read → edit → re-read | agent http | 6 (1/4/1) | 21 % after each successful read | |
| S6 retryable mid-stream error after a tool round | agent | 5 | replay keeps 100 % | plain chat: turn ends ("partial reply kept"), no replay |
| S7 Ctrl+C after `edit_file` completed | agent | — | — | file changed on disk; continuation shows neither the call nor its result |

The first executor request carries a fixed prompt:
- system message: 6.3 KB in agent mode, 4.5 KB in plain chat;
- 11 native tool specs: 9.3 KB.

**Counterfactual 1:** a one-line `go test -overlay` changing
`internal/tui/pipeline.go:537` to `deferRuntimeContext := m.useNativeTools()`.
The prefix kept after the first tool round rises to 73 % (agent) and 84 %
(plain), with `system_changed=false` everywhere.

**Counterfactual 2:** `omitRaw && m.useNativeTools()` (all backends,
continuations only). Rounds ≥ 2 rise to 74–78 %, but the first continuation
stays at 17 %, because the fresh request's system message still differs.

**Real model** (maintainer's Mac, Gemma 4 E4B Q4_K_M, embedded, Metal):
- The first continuation re-evaluates 5,480–5,853 of ~6,400 prompt tokens today,
  versus 1,519–1,671 with counterfactual 1. That is ≈ 8 s of prefill per first
  tool round on that host.
- Whole-task evaluated tokens drop 18–33 % on S1, S3 and S5.
- Contract replies are 24–56 tokens (2–4 s); verifier replies are 66–113 tokens
  (5–9.5 s).
- A follow-up task's first executor request is cold (~6.1k tokens evaluated) in
  every variant: the verifier and contract displaced the single KV sequence.

Order below is by (measured benefit × confidence) / risk. Do Steps 1–3 first.

---

## Step 1 — P0: a cancelled batch must not lose completed mutations

**Problem.** Ctrl+C during a tool batch (`handleCtrlC`, `internal/tui/app.go:2347`)
cancels the run. The batch's `mcpToolResultsMsg` then arrives with a stale
generation and is dropped whole (`app.go:1244-1252`). A mutation that already
completed is recorded nowhere the model or the run can see.
`runPlannedToolBatch` does not check cancellation between calls
(`internal/tui/mcp_tools.go:370-392`). The operation journal keys native calls
by call ID (`internal/history/operations.go:194-197`), so a re-emitted identical
mutation gets a new ID and is **not** deduplicated. Reproduced by
`TestAgentAuditCancelAfterCompletedMutation`: the file changed on disk, and the
continuation sees neither the call nor its result.

**Change (smallest).**
1. `runPlannedToolBatch` (`internal/tui/mcp_tools.go:354`):
   - check `ctx.Err()` before starting each call;
   - give calls not started after cancellation a synthetic result:
     `Outcome` "not executed", `Error.Code: "cancelled_before_start"`, text such
     as "Not executed: the batch was cancelled before this call started. Do not
     count this as completed work or verification; retry it only if it is still
     needed."
2. `mcpToolResultsMsg` handling: when the user cancelled the batch (as opposed
   to superseding it), do not discard the results:
   - append the paired tool messages to the session: real results for completed
     calls, synthetic ones for unstarted calls. History stays call/result-paired.
   - record the completed calls in the run's partial execution (`AbandonCycle`
     already writes Partial executions).
   - keep today's behaviour for *superseded* batches.
3. A synthetic not-executed result is never evidence. `recordAgentToolResultsCount`
   must classify it like `ActionRejected`: no `NewEvidence`, not counted by
   `agent.ExecutedToolCalls`, no `CoverageHighWater` advance.

**Files.**
- `internal/tui/mcp_tools.go`
- `internal/tui/app.go`
- `internal/tui/turn_runtime.go` (distinguish a cancelled generation from a superseded one)
- `internal/tui/agent_loop.go`
- tests beside each file

**Tests.**
- Promote the S7 scenario to a regular (non-gated) test: after cancel +
  "continue", the continuation request contains the `edit_file` call **and** its
  real result.
- A two-call batch cancelled before call 2 shows call 2 as not executed, and the
  ledger does not count it.
- A `run_command` case with a durable side effect: the command already completed
  in this run is not silently re-run when re-emitted with identical arguments
  under a new call ID. Scope the content key to the current run, so the fenced
  protocol's existing content-key behaviour is unchanged.

**Acceptance (fixture).**
- S7: `edit_call_visible=true edit_result_visible=true unanswered_tool_calls=0`.
- No synthetic result reaches `ExecutedToolCalls` or `CoverageHighWater`.

**Risk.** Low to moderate. The cancel path is shared with approval handling. The
"pending approval owns the next keypress" invariant is unaffected, because
cancellation happens after approval. Keep the approval regression tests green.

---

## Step 2 — P1: keep early tokens byte-stable for the whole turn

**Problem.** Three volatile sections are rendered **into the system message**:
- Entity Context (`internal/tui/entity_context.go:163`, which adds a preview of
  each new tool result);
- Active Context;
- the agent directive (`agentDirective`, `internal/tui/agent_loop.go:918`).

This happens on every non-embedded request and on the first embedded request of
each turn. Deferral after history is limited to embedded continuations
(`internal/tui/pipeline.go:537`); see `internal/prompt/compose.go:326-350` and
`runtimeSection` at `:376`. Every successful read changes these sections, so the
prompt diverges ~3 KB into the system message, and the tool-spec block plus all
history is re-prefilled.

**Change.** Freeze the system message per user turn:
1. At the fresh request of a turn, compose as today and store the composed
   system text in turn state (`turnRuntime`), keyed by provider+model.
2. For every native-tool continuation of that turn (`omitRaw=true`), on
   **any** provider:
   - reuse the frozen system text byte-for-byte;
   - send the *current* runtime sections as the existing request-local context
     message after history (`RuntimeContextAfterHistory`, already implemented
     and used by embedded).

   Continuations end with tool messages, so this creates no consecutive
   user-role messages.
3. Keep today's label ("Runtime context supplied by llmtui, not a new user
   request…"). The message is never persisted (existing guarantee).

This step deliberately does **not** move runtime sections out of the fresh
request's system message. That is Step 6, which needs a template check.

**Files.**
- `internal/tui/pipeline.go` (`compositionBase`, `prepareRequest`)
- `internal/tui/turn_runtime.go` (frozen-system lifetime; cleared when the turn completes)
- `internal/prompt/compose.go`: new `FrozenSystem string` input. When set, the
  system message is `FrozenSystem` verbatim and every runtime section goes to
  the context message.
- docs listed in the header

**Tests.**
- Update three existing tests:
  - `TestRuntimeContextPlacementIsLimitedToEmbeddedNativeContinuations`: rename
    it; a remote continuation now defers.
  - `TestVerifiedAgentRecordsContinuationCompaction` and
    `TestVisiblePseudoCallRecoveryIsBoundedAndUsesStructuredRetry`: both assert
    that a note sits in the *system* message. Assert that it appears in the
    request instead.
- New table test in `internal/prompt`: `FrozenSystem` is emitted verbatim, and
  every runtime section lands after history.
- Cache-key invariant: the response cache key must still cover the runtime
  context message.

**Acceptance.**
- Fixture: every executor request after the first in a turn has
  `system_changed=false`, on http and embedded, agent and plain.
- Fixture: S1 agent http keeps ≥ 70 % of the prefix (today 21 %).
- Fixture: request counts and run outcomes are unchanged in every scenario.
- Local: the S1 embedded replay's first continuation evaluates ≤ 2,000 tokens
  (today ~5,700).

**Risk.** Low to moderate.
- The frozen system message may carry turn-start runtime data (for example, an
  empty "observed so far"). Fresher data follows later in the context message.
- Some remote templates may render a user message after tool messages unusually.
  The embedded runtime already does this with Gemma, Qwen and GPT-OSS templates.

---

## Step 3 — P1: stop sending the same evidence three times

**Problem.** After one successful read, the same observed line appears 3× in
the agent request and 2× in plain chat:
1. the tool result in history;
2. the Entity Context preview (`internal/tui/entity_context.go:163-187`);
3. the directive's "Retained observations" (`internal/tui/agent_loop.go:966-970`)
   and "Compact runtime-observed evidence" (`:950`). Agent mode only.

In S4, one batch of three 40-line reads grows the request from 16.5 KB to
29.7 KB; about 6.5 KB of that is duplication. Duplication also brings forward
the overflow in Step 5.

**Change.**
1. `agentDirective` "Retained observations": give a full excerpt only for
   observations whose tool result is **not** in the request history (compacted
   or projected away; see `compactedToolResults` and
   `projectCompletedAgentHistory`). For observations still in history, emit a
   one-line citation, e.g. `read_file(big.log) lines 1500-1500 [in history]`.
2. Entity Context: for entities whose producing tool result is in the current
   request history, emit the header line (id/kind/label/digest) without the
   preview.

**Files.**
- `internal/tui/agent_loop.go`
- `internal/tui/entity_context.go`
- `internal/tui/pipeline.go` (pass "results present in history" from
  `requestHistory`)

Read the `internal/entities` package doc first.

**Acceptance.**
- Fixture: `copies_of_one_observed_line=1` while the result is in history.
- Fixture: S4 agent http second executor request ≤ 23 KB (today 29.7 KB).
- Fixture: S1 agent second executor request ≤ 15.5 KB (today 16.9 KB).
- After compaction (`TestVerifiedAgentRecordsContinuationCompaction`), the
  excerpt reappears: evidence is de-duplicated, never lost.
- `get_entity_details` still returns the full preview.

**Risk.** Low. The model loses only a redundant copy.

**Dependency.** After Step 2 (both touch composition; measure each separately).

---

## Step 4 — P1: make control requests cheaper and non-displacing

**Measured.** Agent mode costs 3 requests for a trivial task (plain chat: 1).
Contract and verifier use the executor's provider (`agent_loop.go:610`,
`:1130`). The embedded runtime has one KV sequence
(`internal/provider/embedded/llamart/generate.go:321-366`), serialized by
`genMu` (`internal/provider/embedded/embedded.go:54`). The verifier therefore
displaces the executor prefix, and the next task starts cold.

- **4a. Embedded: second sequence for control requests.** Run contract,
  verifier and summarizer requests on a second llama sequence (`n_seq_max=2`,
  per-sequence `kvTokens`), so sequence 0 keeps the executor prefix. Confined to
  `internal/provider/embedded/llamart`.
  - Cloud: unit-test the per-sequence prefix bookkeeping with the package's fake
    runtime.
  - Local acceptance: in the S3 replay, the follow-up executor request reuses
    ≥ 5,000 tokens.
- **4b. Skip the contract for trivial questions.** Only after real-model trials
  show no increase in false completion. Until then, keep the contract; it and
  the verifier are what stop false "done".
- **4c. Smaller verifier replies.** Let the verifier omit empty arrays, with
  parser defaults, so replies shrink. Parser tests are cloud-runnable; decode
  savings are local (`LLMTUI_AGENT_AUDIT_DECODE=1`).

**Risk.** 4a moderate (native boundary, memory per sequence). 4b needs data.
4c low.

---

## Step 5 — P1/P2: an oversized tool batch must not fail the run

**Problem.** When the active assistant+tool group does not fit, `prepareRequest`
can drop only *older* groups (`internal/tui/pipeline.go:1105-1117`) and then
fails at `:1159-1163`. Reproduced by S4b on the 8k fallback window, in agent
and plain mode.

**Change.** Before failing:
1. Bound the newest tool results in the active group to the remaining budget:
   keep the head and append "[truncated to fit the context window — reread with
   offset/limit]".
2. Record `ReadObservation` only for the lines actually delivered.
3. Re-estimate. Fail only if the fixed prompt alone does not fit.

**Acceptance.**
- Fixture: S4b reports `status=done`.
- Read coverage records only the delivered lines (no false exact-read coverage).
- Add a regression test.

---

## Step 6 — P1 (after a template check): stable system prefix across turns

Move runtime sections out of the **fresh** request's system message too, as a
runtime-context user message before the raw user message. This creates two
consecutive user-role messages, so gate it per template:
- embedded: verify by rendering the GGUF template with two consecutive user
  turns;
- remote providers: keep Step 2 behaviour unless the user opts in.

**Acceptance (fixture).** The S3 second-run executor keeps ≥ 70 % of the prefix
(today 20 %). Needs a maintainer decision, because it touches CLAUDE.md rule 5
("raw user message last").

## Step 7 — P2: plain-chat mid-stream replay parity

On a retryable mid-stream error after a tool round, plain chat ends the turn;
agent runs replay once (`replayInterruptedAgentStream`, `internal/tui/app.go:2814`,
returns early unless an agent run is active). Reuse `claimStreamReplay` for
native-tool continuations in plain chat.

**Acceptance (fixture).** S6 plain replays once and the turn completes.

## Not recommended now

- **Parallel tool scheduling.** Local reads cost microseconds, next to seconds
  of prefill. Revisit only for MCP/web batches with measured latency.
- **Wholesale append-only context manager.** The backend KV already keeps the
  longest common prefix. The problem is where llmtui places volatile bytes,
  which Steps 2 and 6 fix.

## Dependencies

| Step | Depends on |
|---|---|
| 1, 4a, 5, 7 | none (standalone) |
| 3 | Step 2 |
| 6 | Step 2 and the template check |

## Local re-measurement (maintainer only, after merge)

```bash
LLMTUI_AGENT_AUDIT_TRACE=1 LLMTUI_AGENT_AUDIT_DUMP=$DIR go test ./internal/tui -run TestAgentAuditTrace -count=1
LLMTUI_AGENT_AUDIT_REPLAY=$DIR LLMTUI_AGENT_AUDIT_TRIALS=2 LLMTUI_AGENT_AUDIT_DECODE=1 \
  YZMA_LIB=$RUNTIME LLMTUI_TEST_GGUF=$GGUF CGO_ENABLED=1 \
  go test ./internal/provider/embedded/llamart -run TestAgentAuditPrefillReplay -v -count=1 -timeout 120m
```
