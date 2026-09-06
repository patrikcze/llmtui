# Agent-mode reliability: contract park + recovered-failure loop

Status: **Resolved** — implemented in PRs #59–#62 (merged 2026-09-06). Retained
as the investigation record. Current architecture: [`README.md`](README.md).

Reviewed 6 September 2026 against `master` at `51d955e`.

## What was observed

Manual testing in `/Users/patriknakladal/VSCode/local/test_llm` with workspace
tools in **auto-approve** mode. Default local stack: LM Studio
(`openai_compatible`), models `google/gemma-4-e4b`, `openai/gpt-oss-20b`,
`qwen/qwen3.6-35b-a3b`; adaptive verifier, `verifier.model` unset (falls back
to the chat model). **Chat-only** handled every prompt well. **`/agent on`**
showed two reliability problems, both reproducible from the persisted
`~/.local/share/llmtui/agent-runs` records.

### A. Some requests park at cycle 0 in the contract stage

| Request | Runs | Result |
| --- | --- | --- |
| "Read the file I mentioned and give me its heading." | 4 | `parked`, stage `contract`, 0 cycles |
| "Read absent.md and summarize it." | 1 | `parked`, stage `contract` |
| "…list files with similar functionality … ask me which one I choose?" | 2 | `parked`, stage `contract` |

Every park is a single contract call + its one repair (≈5 s), ending with:

```
task contract unavailable: parse task contract: malformed model control
output: user-input contract requires an empty criteria array and a question
```

Restarting llmtui does not help. Comparable prompts ("Read report.md …",
"Read config.txt …", "Can you read py_calculator.py …") completed with pinned
criteria.

### B. Recovered `ask_user` argument errors cause a retry loop, then failure

Run `34cd081b…` — "Ask me whether to create result.txt containing approved.
Only create it if I say yes.":

1. Executor emits `ask_user` twice with no `question` argument → both rejected
   (`invalid arguments for ask_user: call is missing required argument
   "question"`).
2. Executor emits a valid `ask_user`; the user answers "yes".
3. Executor writes `result.txt` — succeeds.
4. Cycle-1 semantic verification runs, **rejects the cycle**, and injects the
   retry objective *"Retry the process by calling the 'ask_user' tool with the
   correct question…"*.
5. The retry re-asks, re-writes `result.txt` (**`Update(result.txt) — no
   changes`**), and Gemma then returns an empty completion twice in a row →
   run `failed`.

### C. `Delete /etc/passwd` ran the shell command — working as designed

The test had `⚒ workspace tools on (auto-approve)`. `Guardrails.ClassifyCommand`
classifies an outside-workspace `rm` as non-auto, so `Runner.NeedsApproval`
returns true and default mode prompts; **auto-approve deliberately bypasses
that prompt**. Chat-only refused because no executor system prompt was in
play. The agent executor prompt nudges some models toward acting instead of
refusing (brief §1) — a Slice 3 measurement, not a bug. No fix here.

## Root cause

### A — the contract stage is a hard gate that discards the model's output on any `needs_user_input` shape it did not expect

`internal/agentverify/contract.go:237-243`:

```go
if needsInput {
    if len(criteria) != 0 || question == "" {
        return Contract{}, wrap(fmt.Errorf("%w: user-input contract requires an empty criteria array and a question", agent.ErrMalformedControl))
    }
} else if len(criteria) == 0 || question != "" || len(options) != 0 {
    return Contract{}, wrap(fmt.Errorf("%w: executable contract requires criteria with no question or options", agent.ErrMalformedControl))
}
```

**Confirmed by probing `agentverify.EstablishContract` directly against the
real LM Studio provider (same model, same `ResponseConstraint` path):**

- `google/gemma-4-e4b` returns, for *every* file-read task including the ones
  that "worked", `{"criteria":[],"needs_user_input":true,"question":"Please
  provide the content of '<file>' …","user_options":[…]}`. It asks the user to
  **paste the file contents** — because the contract stage is tool-free and
  the model has no idea a `read_file` tool will exist at execution time. This
  shape *parses fine* and should surface as a clarifying question.
- `openai/gpt-oss-20b` returns `{"needs_user_input":false,"criteria":[…]}` for
  every task (for "the file I mentioned" it invents the criterion "Identify
  the file name or path specified by the user"). Also parses.
- `qwen/qwen3.6-35b-a3b` returns an empty body → a *different* parse error
  ("JSON object not found").

So the exact envelope that produces "user-input contract requires an empty
criteria array and a question" (`needs_user_input:true` **and** non-empty
`criteria`, **or** an empty `question`) was **not reproduced by a direct
probe** — it is a lower-probability output shape, most likely from the
**repair** call: the first contract attempt fails for some reason, then
`contractRepairMessages` appends *"A non-ambiguous task must have at least one
criterion."* (`contract.go:176`), and the model — still wanting the file —
returns `needs_user_input:true` **with** a criterion to satisfy that
instruction, tripping the rule.

What *is* firmly established:

1. The park is **deterministic per session** for these prompt shapes and gives
   the user an opaque internal string.
2. `handleAgentContract` (`internal/tui/agent_loop.go:471-487`) receives the
   bounded raw model output in `msg.out.Raw` and **throws it away**, so
   neither the user nor a maintainer can see what the model actually returned.
3. The recovery path for a *well-formed* `needs_user_input` contract already
   exists: `handleAgentContract:489-506` turns it into a clarifying question,
   and `resumeVerifiedRunWithInput` (`agent_loop.go:562-569`) re-runs the
   contract stage with the answer in `ContractInput.UserInput`.
4. Both prompt templates (`contractMessages` `contract.go:165`,
   `contractRepairMessages` `contract.go:176`) end by telling the model a task
   "must" have a criterion, which pushes criteria onto genuinely ambiguous
   requests.

Not adding conversation history to the contract prompt is the right call — it
is fresh-context by design.

### B — recovered in-cycle failures pollute what the semantic verifier sees

`rejectWholeBatch` → `recordAgentToolResultsCount`
(`internal/tui/agent_loop.go:1134-1173`) appends, for **every** rejected call:

- a failed `ToolCallRecord` to `execution.ToolCalls`, and
- an `agent.RunError{Kind: ErrorToolValidation}` to `execution.Errors`.

Nothing removes those once the model recovers:

- **`EvaluateDeterministic` already exempts recovered failures** — it only
  judges the *trailing* tool call (`internal/agent/deterministic.go:48-62`)
  and explicitly no-ops `ErrorToolValidation`/`ErrorToolExecution` in its
  `Errors` loop (`deterministic.go:80-86`). Deterministic evidence is fine.
- **The semantic verifier is not exempted.** Cycle 1 always runs semantic
  verification (`agent_loop.go:782-802`), and `Verify` marshals the whole
  `agent.ExecutionResult` — including `Errors:[tool_validation,
  tool_validation]` — into the evidence blob (`verifier.go:126`, `:307`). A
  4B Gemma verifier reads two typed validation errors and rejects a cycle
  that in fact succeeded.
- **`CollectEvidence` compounds it** (`internal/agent/criteria.go:272-308`):
  one named `EvidenceToolFailure` per failed call, but successes collapse to
  a single unnamed *"N tool call(s) succeeded"*. The cumulative ledger cannot
  show that a valid `ask_user` and a confirmed `write_file` came *after* the
  invalid calls.

### C — no-op writes are scored as file changes

`agent_loop.go:1158-1161` (in `recordAgentToolResultsCount`):

```go
if result.Err == nil && (result.Call.Tool == tools.ToolWriteFile || result.Call.Tool == tools.ToolEditFile) && strings.TrimSpace(result.Call.Path) != "" {
    m.agentLoop.execution.ChangedFiles = append(m.agentLoop.execution.ChangedFiles, result.Call.Path)
    m.agentLoop.execution.Artifacts = append(m.agentLoop.execution.Artifacts, result.Call.Path)
}
```

`write_file` on identical content still succeeds and returns
`"Update(path) — no changes"` (`internal/tools/diff.go:53`); the path is
recorded as changed regardless. On the bug-B retry, a duplicate no-op write is
scored as fresh progress (`NewEvidence`, an `EvidenceFile` item), making a
pointless retry look productive right before the run fails.

## Fix plan — three small PRs, in order

### PR 1 — contract clarification recovery + diagnostics (the direct fix for A)

**Scope:** `internal/agentverify/contract.go`, `internal/tui/agent_loop.go` +
tests. No new persisted schema, no loosened bounds, no conversation added to
the contract prompt.

1. **Salvage a usable clarification.** In `ParseContract`, when
   `needsInput == true` **and** `question != ""`: discard non-empty
   `criteria`, keep the question and genuine `user_options`, and return the
   clarification contract. Keep the strict field set, types, and maximums.
2. **Keep the genuinely useless cases parked.** `needsInput == true` with an
   empty `question` is still `ErrMalformedControl` → the one repair → park.
   `needsInput == false` is unchanged.
3. **Reword the prompt.** In `contractMessages` / `contractRepairMessages`
   replace *"A non-ambiguous task must have at least one criterion."* with
   something that does not push criteria onto ambiguous tasks, e.g. *"An
   ambiguous task returns an empty `criteria` array with a `question`; a clear
   task returns 1–8 `criteria` and an empty `question`."*
4. **Make the next park diagnosable.** On a contract park in
   `handleAgentContract`, record the bounded, sanitised `msg.out.Raw` as a
   run `Event` detail (it is already length-bounded and reasoning-free) and
   include it in `--debug` output. This is the single change that turns "it
   parks and I don't know why" into an actionable report.

**Tests (`internal/agentverify/verifier_test.go`, beside the `ParseContract`
cases at lines 102-133):**

- `needs_user_input:true` + question + stray `criteria` → parses as a
  clarification: `Criteria` empty, `Question` and `UserOptions` preserved.
- `needs_user_input:true` + empty question → still `ErrMalformedControl`.
- Existing executable-contract and empty-question cases unchanged.

**Controller regression (`internal/tui/agent_loop_test.go`), scripted
provider:**

- Contract reply `{"criteria":["provisional"],"needs_user_input":true,
  "question":"Which file did you mean?","user_options":[]}` → run reaches
  `DecisionNeedsUserInput` at `StageContract`, the question is surfaced,
  **zero tool calls, zero cycles**.
- Supplying the answer re-enters the contract stage with `ContractInput.UserInput`
  set and the run proceeds to cycle 1.
- A contract park records the raw output in the run's events.

**Acceptance:** "Read the file I mentioned and give me its heading." asks
*which file* instead of parking; a future park is diagnosable from the run
record.

### PR 2 — accurate recovered-tool evidence (prevents the B loop)

**Scope:** `internal/tui/agent_loop.go`, `internal/agent/criteria.go`,
`internal/agent/deterministic.go` + tests. A genuinely failing cycle is
unchanged.

1. **Do not carry recovered argument failures into `execution.Errors`.** When
   a later call for the *same tool* in the same cycle succeeds, drop (or flag
   `recovered`) the earlier `ErrorToolValidation` entry so the verifier's
   evidence is not dominated by errors the model already fixed. Failed
   `ToolCallRecord` entries may stay, but `MechanicallyComplete`
   (`deterministic.go:109`) and the verifier evidence must treat a
   recovered-then-succeeded tool as clean.
2. **Name successful tools in the ledger.** In `CollectEvidence`, replace the
   single *"N tool call(s) succeeded"* aggregate with one bounded entry per
   distinct successful tool name.
3. **Only record a real content change.** Gate the
   `ChangedFiles`/`Artifacts` append on the write actually changing bytes —
   check the tool output is not the `"— no changes"` render, or have
   `Runner.writeFile` return `changed bool` in `tools.Result` and thread it
   through. A no-op write stays a successful `ToolCallRecord` but adds no
   `EvidenceFile` and no `NewEvidence`.

**Regression (`internal/tui/agent_loop_test.go`):** two malformed `ask_user`
calls, one valid `ask_user`, an answered confirmation, a `write_file` that
succeeds — the cycle verifies **passed on the first attempt**, no retry
objective, `ChangedFiles == ["result.txt"]` once; a second identical request
writes the same bytes and that cycle's `ChangedFiles` is empty
(`NewEvidence == false`).

**Acceptance:** recoverable `ask_user` argument failures do not trigger a
verifier rejection or a duplicate `write_file` side effect.

### PR 3 — real Gemma / GPT-OSS evaluation slice (validates A + B against models)

Extends the opt-in Slice 3 matrix in
[`local-llm-evaluation-plan.md`](local-llm-evaluation-plan.md) (§ "Slice 3").
No production code.

- Add two cases: the ambiguous file request (expect `ASK` via a contract
  clarification, not a park) and the conditional-write request (expect `ASK`
  → `CONFIRM` → one `write_file`, no retry).
- Run against **each** loaded model (`gemma-4-e4b`, `gpt-oss-20b`) — they
  behave oppositely at the contract stage (Gemma over-clarifies, GPT-OSS
  over-decomposes), which is itself the finding.
- Five disposable-workspace trials each; record model ID, quantization,
  prompt mode, context size, temperature/seed, tool schema, approval mode,
  llmtui commit.
- Report separately: action choice, contract-parse failures, verifier
  false-rejections, duplicate writes, empty completions, final outcome.
  Preserve an unclassified count.

**Acceptance:** both cases run against both models; missing endpoints reported
as not-run; per-trial disagreements visible. Not evidence for a universal
prompt change.

## Open question (not in any PR yet)

The contract stage is tool-free so the model cannot "plan actions", but that
is *why* Gemma asks the user to paste file contents for every file task — it
does not know a `read_file` tool exists. A minimal capability hint ("tools
including file read/write and command execution will be available during
execution; do not ask the user to supply information a tool can obtain")
without schemas or history might let the contract stage produce executable
contracts for these prompts. This is a design change with its own risks
(scope creep, the model planning tool calls in `criteria`) and should be
measured in Slice 3 before it is attempted.

## Not in scope

- Feeding conversation history into the contract prompt.
- Changing executor/verifier prompts for the `/etc/passwd` behaviour (that was
  auto-approve working as designed).
- Loosening any contract/verifier field, bound, or approval gate.
