# Agent evolution evaluation

The agent-runtime changes are calibrated with deterministic fixtures first and
with live endpoints only when a person explicitly configures one. This avoids
treating one local model, template, or sampling run as a universal result.

## Current baseline

`TestAgentEvolutionSyntheticMatrix` drives the shared contract → executor →
tool → verifier kernel with the scripted action-class scenarios. It writes a
temporary JSONL report and checks the aggregate rather than trusting an
assistant or verifier claim of success.

The Phase 6 baseline is ten deterministic scenarios: ten of ten expected
first actions and outcomes, zero false-success cases, eight executed fixture
calls, and no new approval or unsafe-execution regression in the negative
fixtures. Those counts establish only the scripted controller baseline. They
do not measure a live model's reasoning quality, latency, or tool-format
reliability.

Run it directly with:

```bash
go test -count=1 ./internal/tui -run '^TestAgentEvolutionSyntheticMatrix$' -v
```

The report contains the scenario ID, expected and observed action class,
correctness, independent false-success flag, and number of executed calls. It
contains no prompt body, tool arguments, tool output, hidden reasoning, or
credentials.

## Assistance and recovery policy

The format-assistance tracker is **shadow-only**. It records repeated control
format failures per selected model and exposes a recommendation in `/debug`,
but it does not alter prompts, choose a provider, route requests, or change
approval policy. There is therefore no `auto` assistance default to roll out
or roll back. Removing the tracker is a local, process-only change and does
not affect run records, tool receipts, or integrity fixes.

Visible pseudo-tool recovery is different: it is an integrity-preserving
controller behavior, not assistance. A strict provider marker can trigger one
schema-bound reissue per execution cycle. The original visible envelope is
dropped, and any later structured call must pass normal validation and fresh
approval. This recovery must remain enabled independently of any future
assistance experiment.

## Live endpoint runs

The contract probe is opt-in and never runs in CI:

```bash
LLMTUI_EVAL_BASE_URL=http://127.0.0.1:1234/v1 \
LLMTUI_EVAL_MODEL=your-model \
go test -count=1 ./internal/agentverify -run '^TestContractStageAgainstRealEndpoint$' -v
```

`LLMTUI_EVAL_API_KEY` is optional. Use a disposable endpoint and workspace;
the probe must not target production accounts or enable tools merely to gather
measurements. Native embedded coverage additionally requires `LLMTUI_TEST_GGUF`,
`LLMTUI_TEST_CPU`, and `YZMA_LIB`; a skipped native test is **not** a pass.

For each model/protocol/settings combination, run at least five trials of the
same fixture set. Record the commit, model and quantization when known,
backend/runtime version, template, sampling and seed support, context window,
tool-schema hash, approval mode, assistance setting, warm/cold load state, and
whether the run was skipped. Keep cold-load latency separate from inference
latency.

The reusable live contract/conformance matrix is also opt-in and defaults to
five trials per fixture:

```bash
LLMTUI_EVAL_BASE_URL=http://127.0.0.1:1234/v1 \
LLMTUI_EVAL_MODEL=your-model \
LLMTUI_EVAL_ENDPOINT_TYPE=openai_compatible \
LLMTUI_EVAL_OUTPUT=/tmp/llmtui-evaluation.jsonl \
go test -count=1 ./internal/eval -run '^TestLiveEvaluationMatrix$' -v
```

Use `LLMTUI_EVAL_ENDPOINT_TYPE=ollama` for Ollama and its native API. Set
`LLMTUI_EVAL_TRIALS` to change repetition and `LLMTUI_EVAL_STREAM=false` to
probe non-streaming behavior. The report contains one metadata record, one
bounded record per contract/conformance trial, and denominator-preserving
contract and conformance summaries. It records repair/recovery counts, status fields, token
usage when the provider supplies it, and elapsed time; it does not record
fixture task text, raw responses, credentials, tool arguments/results, or
reasoning. A recovery probe is a fresh harmless observation and never executes
the provider's call.

The full controller loop has a separate opt-in matrix using the same endpoint
variables and disposable workspaces:

```bash
LLMTUI_EVAL_BASE_URL=http://127.0.0.1:1234/v1 \
LLMTUI_EVAL_MODEL=your-model \
LLMTUI_EVAL_ENDPOINT_TYPE=openai_compatible \
LLMTUI_EVAL_OUTPUT=/tmp/llmtui-live-agent.jsonl \
go test -count=1 ./internal/tui -run '^TestLiveAgentMatrix$' -v
```

It repeats synthetic read, missing-path/ask, and temporary-write/confirmation
fixtures through the real contract → executor → tool → verifier path. It
records first-action class, final result, verifier verdict, tool/provider
request counts, token use, recovery requests, cycles, and elapsed time. The
driver resolves only the fixture's synthetic answer/approval and stops at
unresolved user input; it never enables personal-app or production MCP
actions. Both live tests skip unless the endpoint and model are explicitly
configured.

Compare `off` and `shadow` first. A future `auto` mode is eligible only after
it improves its declared target without new unsafe execution, approval
regression, negative-case false success, or extra clean-path calls. Compare
raw denominators for completion, false success, action errors, semantic
disagreement, calls by purpose, token use, context omissions, recovery
attempts, and elapsed time. Do not infer statistical universality from five
trials.

## Saved-state compatibility

Agent-run persistence remains schema version 1. New receipts and recovery
metadata are additive or process-local: older records load with unknown
execution attribution rather than inferred proof, and no persisted approval is
restored. `TestFileStoreLoadsPreReceiptRunWithoutInventingAttribution` is the
compatibility gate. Evaluation changes must keep that test green before any
default changes are considered.
