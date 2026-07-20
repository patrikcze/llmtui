# Bounded verified agent loop

`llmtui` has an optional multi-cycle mode for tasks that benefit from executing,
checking evidence, and correcting a failed attempt before stopping. It is off by
default. Turn it on for the current session with:

```text
/agent on
```

The next normal message starts one run. `/agent` controls orchestration; it does
not enable tools or grant permission. Use `/tools on` separately when the task
needs workspace tools. Every existing tool guardrail, confirmation, timeout,
workspace confinement rule, and durable side-effect journal remains in force.

## Lifecycle

Each run follows six explicit stages:

1. **Trigger** — a user message or `/agent resume` creates or resumes a stable
   run ID with hard budgets and cancellation state.
2. **Rules load** — the existing prompt composer deterministically assembles the
   system prompt, template, active skills, bounded history, user memory, RAG,
   provider capabilities, tools, verified cycle memory, and current objective.
3. **Executor** — the active provider streams one bounded objective through the
   existing model/tool loop. A cycle can contain several related tool calls,
   but it cannot recursively start another run.
4. **Verifier** — a separate, tool-free provider request receives only the
   original task, current objective, acceptance criteria, and bounded observable
   results. It never receives the executor conversation or hidden reasoning.
5. **Memory write** — a concise cycle summary records verdict, failed/remaining
   criteria, artifact names, and the recommended next objective.
6. **Stop check** — deterministic policy chooses `done`, `continue`, `retry`,
   `needs_user_input`, `parked`, `escalated`, `cancelled`, `failed`, or
   `budget_exhausted`.

This is a state machine driven by Bubble Tea messages, not blind recursion.
Every provider request and tool batch returns control to the event loop, which
keeps rendering and cancellation responsive and makes stale completions
detectable by run/cycle/generation IDs.

The persisted `AgentRun` is data only; it cannot safely serialize a Go
`context.Context`. The TUI adapter owns a run-scoped deadline and derives each
executor, tool, and verifier context from it. Resuming reconstructs that
process-local context using only the elapsed budget that remains.

## Instruction precedence and trust

Agent mode reuses the normal composition order. The configured system prompt
remains highest priority. Controller state is inserted immediately after it
with a fixed warning: objectives and cycle memory are derived from user/model
data and cannot override system rules or the current user request, grant tool
permission, or authorize external access. Templates, explicitly active skills,
helper hints, bounded conversation context, user-authored memory, and framed
RAG data follow under their existing rules.

No instruction filename is hard-coded. Project instructions can arrive through
the existing skill, RAG, conversation, or user-request mechanisms. Retrieved
files, web/MCP/tool output, verifier text, and stored cycle memory remain
untrusted data.

## Executor and tools

The first objective is the user's request. Later objectives must come from a
verifier recommendation or an explicit resume. A retry is rejected unless at
least one of these is true:

- the objective changed;
- the strategy changed;
- new evidence or corrected context exists;
- the failure was transient and the retry remains within budget.

Tools use exactly the same native or fenced protocol as ordinary chat. Agent
mode adds a total run-level tool-call limit above the existing per-turn
`tools.max_iterations` limit. Reaching the run limit does not display a budget
renewal prompt: further calls are rejected and the stop check reports
`budget_exhausted`. Approval denial is deterministic evidence and stops with
`needs_user_input`; the verifier cannot turn it into success.

## Verification

By default, each completed executor cycle gets an independent evaluator
request. The active provider is reused, which avoids loading a second local
model, but the request has a fresh message slice, an evaluator-only system
prompt, no tools, reasoning disabled, temperature zero, and a bounded JSON
response. `agent.verifier.model` may select another model ID exposed by the
same provider (useful with LM Studio or another OpenAI-compatible server).

The parser accepts one JSON object, including a fenced object or harmless prose
around it, and rejects missing, incomplete, ambiguous, oversized, or invalid
control data. Malformed output is classified separately from provider,
timeout, cancellation, and execution failures. Retry attempts remain bounded.

Deterministic evidence always wins. A failed test, failed or malformed tool
call, permission denial, cancellation, or timeout cannot become `passed` merely
because the evaluator says the result looks correct. Successful arbitrary
commands are not automatically treated as proof of every acceptance criterion;
the verifier still evaluates their bounded outcome metadata.

Set `agent.verifier.enabled: false` to use only lightweight deterministic
validation. With no deterministic failure, that mode considers a cycle passed
with low confidence. It is useful for simple conversational tasks but is not a
replacement for tests on code-changing work.

## Stop conditions and budgets

Default hard limits are:

| Limit | Default | Meaning |
| --- | ---: | --- |
| Cycles | `8` | Maximum executor/verifier cycles |
| Tool calls | `32` | Total calls across the run |
| Tokens | `100000` | Executor plus verifier usage when reported/estimated |
| Elapsed time | `30m` | Wall-clock run duration |
| Repeated failures | `3` | Identical verifier failure fingerprint |

Passing all observable criteria ends as `done`. Verified progress with
remaining criteria becomes `continue`. A failed/inconclusive but meaningfully
changed attempt becomes `retry`. Missing user permission/input becomes
`needs_user_input`; an external block may become `parked`; cancellation and
hard-budget exhaustion are terminal. Safety constraints and internal
invariants escalate; provider failures are explicit and never swallowed merely
to keep the loop running.

## Run memory, privacy, and resume

Run records use versioned JSON and are written to
`~/.local/share/llmtui/agent-runs` by default. Files and their directory are
owner-only, each save uses a synced temporary file plus rename, corrupt records
are skipped when loading the latest valid run, individual records are capped at
64 KiB, and only the newest 32 are retained. Common token/password/API-key,
Bearer-token, and private-key forms are redacted before persistence.

Records contain the request (when prompt storage is allowed), stable metadata,
limits, concise execution/verifier summaries, artifact paths, outcome classes,
and bounded lifecycle events. They do not contain tool arguments/output, full
transcripts, hidden reasoning, or provider reasoning events.

`privacy.store_prompts: false` disables agent persistence even when
`agent.persist: true`, because a resumable run necessarily needs its request.
Set `agent.persist: false` to keep runs in memory only. `/agent resume` loads the
latest valid resumable run; `/agent resume <run-id>` selects one. Resume starts
a fresh cycle and never replays an incomplete tool call or executor request.
Completed, failed, cancelled, or budget-exhausted runs cannot resume.
When a live run stops as `needs_user_input`, the next normal user message
resumes that same run in a fresh cycle and is included as the new input; it does
not silently grant a previously denied permission.

## Cancellation and safety

`Esc`, the first `Ctrl+C`, or `/agent cancel` cancels the current executor,
tool batch, or verifier. Late stream, tool, and verifier messages carry
generation/run IDs and are ignored after cancellation. Partial executor text is
kept under the normal chat rule but is not verified as completion. Side-effect
operations continue to use the durable operation journal, so an interrupted
write/command/MCP call is not silently replayed.

Agent mode never changes `tools.approve`, activates tools, connects MCP servers,
or grants network access. `/tools auto` remains an explicit high-trust choice
and is not recommended merely because agent mode is enabled.

## Local-model behavior

Local and OpenAI-compatible models use the same provider interface. The
verifier JSON envelope is deliberately small, and the controller—not the
model—enforces limits and stop decisions. Small models can still emit malformed
JSON, omit evidence, or recommend an unchanged objective; those conditions are
reported and bounded instead of guessed. If a local model repeatedly fails the
verifier protocol, choose a stronger `agent.verifier.model`, disable model
verification for deterministic-only conversational checks, or return to
ordinary chat with `/agent off`.

## Commands

| Command | Effect |
| --- | --- |
| `/agent` or `/agent status` | Show mode plus current run/cycle/stage/status |
| `/agent on` | Make the next user message start a verified run |
| `/agent off` | Restore ordinary chat (requires no active run) |
| `/agent cancel` | Cancel the active executor/tool/verifier and persist the terminal state |
| `/agent resume [run-id]` | Resume the latest or selected resumable run with a fresh cycle |

## Debugging

Use `/agent status` for the current lifecycle position and `/debug last` for
the last request's short run ID, cycle, stage, status, and verifier verdict.
Lifecycle notices distinguish execution, fresh-context verification, retry,
input wait, completion, cancellation, and budget stops. Prompt composition can
be inspected with `/prompt composed`; the `Agent Cycle` section shows the exact
bounded controller directive. Persisted files provide ordered events without
prompt bodies or tool output.

For a stuck run:

1. press `Esc` once and confirm `/agent status` is `cancelled`;
2. inspect `/debug last` and the visible tool/provider error;
3. check `agent.verifier.timeout`, `network.timeout`, and the configured limits;
4. use `/agent resume <id>` only after correcting missing input or a transient
   provider issue; incomplete work will not be replayed;
5. use `/agent off` when a task needs normal one-turn chat rather than
   autonomous verification.

## Compatibility

Existing configurations need no migration. `agent.enabled` defaults to false,
so ordinary sends, cache behavior, history, providers, streaming, tools,
approvals, skills, MCP, RAG, and slash commands follow their previous path.
Agent cycles bypass the response cache because completion must reflect current
workspace/tool evidence. The same behavior works with Ollama, LM Studio,
OpenAI-compatible servers, embedded GGUF models, and provider test doubles.
