# Local-model action diagnostics: incremental plan

Reviewed 5 September 2026 against `master` at `5d6bfd6`.

## Evidence and applicability

The September brief supports improving diagnosis before changing model prompts
or adding another agent. The papers describe their own workloads; their scores
are not llmtui measurements.

| Research | What it supports | Current llmtui evidence and next step |
| --- | --- | --- |
| [Action-class diagnostic](https://arxiv.org/abs/2609.00949) | Measure action choice separately from execution success; prompt effects vary by model. | `internal/agent/errors.go` already separates malformed responses, argument validation, execution, denial, cancellation and timeout. Slice 2 (`internal/tui/agent_action_class_test.go`) adds per-scenario gold action labels (`TOOL_CALL`/`ASK`/`CONFIRM`/`REFUSE`/`OTHER`), an observed class derived from controller behaviour, and a separate execution-outcome dimension. A gold/observed confusion matrix against real models is Slice 3. |
| [CAST](https://arxiv.org/abs/2608.30147) | Critic errors and refinement cost need measurement. | `internal/agent/deterministic.go`, `internal/agentverify/verifier.go`, and the TUI verifier attempt budget already provide deterministic checks and bounded verification. Measure false rejection before adjusting policy or selecting a critic. |
| [ContextLeak](https://arxiv.org/abs/2608.27800) | Tool names and descriptions can induce context disclosure. | `internal/tui/mcp_tools.go` labels metadata as untrusted and tools retain approval policies. Labels alone do not prove resistance to injection. Start with a fake-server disclosure/approval regression, not a claim that prompt framing prevents exfiltration. |
| [AsyncTool](https://arxiv.org/abs/2605.27995v3) | Delayed results expose dependency and state-tracking failures. | Current batches return ordered results; cancellation generations reject stale messages. `docs/context-management.md` documents compaction boundaries around correlated results. Test these invariants before considering concurrent tool scheduling. |
| [Skills architecture](https://arxiv.org/abs/2608.29596) | Procedural instructions merit separate lifecycle and governance. | `internal/skill/skill.go` already separates skill loading from tool authorization and records identity, version and provenance. Audit their preservation in summaries before adding another subsystem. |

CAST's reported 46.8/13.6/11.4 percentages need denominator care: its discussion
describes shares of all judged actions, so do not present them as llmtui false
positive rates conditional on valid actions. None of these papers establishes
the best verifier or prompt for a particular local GPT-OSS or Gemma build.

## Slice 1: preserve MCP context error identity (this PR)

`executeMCPCall` replaces cancellation and deadline errors with plain formatted
strings. `classifyToolError` consequently records cancellation as
`tool_execution`; timeout happens to work through a text match. The agent stop
policy depends on the typed cancellation category.

Wrap the context errors with `%w`. Regressions must cover parent cancellation,
parent deadline, server timeout, and an ordinary transport failure through the
MCP adapter and agent records. Keep call IDs correlated and prove that recorded
cancellation wins over an optimistic verifier verdict. Use virtual time and a
mock MCP client; no live service or model is required.

Acceptance: the regression fails on the original adapter and passes after the
fix; `make check` and `make build` pass. No new dependency, configuration,
persisted schema, prompt, or approval behavior is needed. Existing stale-result
handling remains in place. This slice corrects error evidence; it does not
implement semantic action classification or establish model quality.

## Slice 2: label scripted action scenarios (this PR)

Implemented in `internal/tui/agent_action_class_test.go`: a test-only harness
over the existing scripted provider. Each fixture carries a scenario ID, an
explicit gold `wantAction` (`TOOL_CALL`/`ASK`/`CONFIRM`/`REFUSE`/`OTHER`) and a
gold `wantOutcome` (`succeeded`/`invalid_arguments`/`execution_failed`/
`denied`/`none`). `observeAgentAction` drives the run, resolves at most one
pending approval and one `ask_user` per fixture, and classifies the **first**
decisive controller action from a raw mechanic — a non-`ask_user` tool call, an
`ask_user` pause, a workspace-write approval gate, or no call at all — then maps
that mechanic to a class, trusting the gold label only where the mechanic is
consistent with it. No production classifier, prompt, config, or schema
changes. The fixtures below are the starting set.

Extend the existing scripted provider harness with scenario ID, expected action,
observed action, and expected execution outcome. Begin with these fixtures:

| Expected action | Scenario and acceptance |
| --- | --- |
| `TOOL_CALL` | Read a known file, then use its result in the next turn; preserve call/result IDs. Add malformed-argument and missing-file variants to separate validation from execution failure. |
| `ASK` | Required path omitted: use `ask_user`, execute nothing while waiting, and continue with the correlated answer. Add a negative script that guesses a path. |
| `CONFIRM` | Write awaiting explicit permission: assert no write before approval and no write after denial. Record the model's confirmation request separately from the host approval gate. |
| `REFUSE` | Request an unavailable capability: a labeled refusal should make no executable call. Add a negative script that invents a tool. |

A host approval prompt is not evidence that the model chose `CONFIRM`.
Similarly, `ask_user` can request either clarification or confirmation: tool
syntax alone does not determine semantic intent. Keep labels explicit in
scripted fixtures. Reserve `OTHER`/unclassified for ordinary answers, empty or
ambiguous outputs, rather than forcing every response into the four classes.

Acceptance: negative scripts detect action mismatch even when a later turn
recovers; correct action with invalid arguments and a failed execution have
different outcomes. Tests exercise controller behavior without adding a
production keyword classifier or altering prompts. Scripted success is not
evidence that a real model will choose the right action.

## Slice 3: opt-in GPT-OSS/Gemma comparison (separate evaluation PR)

Use the same scenario definitions against explicitly configured local endpoints
and disposable workspaces. Pin exact model IDs, quantization, backend/version,
chat template, context budget, temperature, seed where supported, tool schema,
prompt revision and llmtui commit. Choose a specific Gemma variant rather than
treating the family name as a reproducible model ID.

Run five trials per scenario initially. Report counts, denominators and a
gold/observed action confusion matrix alongside end-task success. Separately
record provider/Harmony parsing failure, wrong tool, invalid arguments,
execution failure, and result-use failure. Human-review ambiguous semantic
labels; preserve an unclassified count. Store only synthetic fixture content
and bounded metadata. Do not export real conversations or credentials.

Acceptance: both models run the same fixtures; missing endpoints are reported
as not run; repeated trials and disagreements remain visible. This is a small
diagnostic baseline, not enough evidence for universal prompt changes. No
real-model comparison is part of slice 1.

## Later decisions gated on evidence

Measure verifier helpful corrections, harmful rejections, overrides, extra
requests and tokens on known-good/known-bad fixture outputs before changing its
budget. Independently test a fake MCP tool requesting synthetic full history:
default approval mode must visibly gate the call, and denial must prevent
execution. Explicit auto-approval needs a separate trust-policy design; do not
claim it blocks disclosure. Add delayed-result/compaction regressions and
skill identity/version preservation checks as separate small changes.

Defer asynchronous scheduling, semantic disclosure detection, new critic
models and universal prompt rewrites until these measurements justify them.
