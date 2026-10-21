# Harness experiments

`llmtui harness` implements one bounded baseline → propose → probe → compare →
compose → review step, plus a separate experience library. It is disabled by
omission: ordinary startup never opens its state, creates a provider for it,
retrieves experiences, or changes the agent. No Swift, iOS or GUI runtime is
involved.

**This version runs restricted fixture simulations. It does not benchmark a
real model executing a user task.** A scripted trajectory deliberately reacts
to a catalog edit. Passing an independent fixture oracle proves the comparison
and integration plumbing, not that a real model will follow that guidance.
Model-generated proposals can be tested against the same simulations, with
exactly the same limitation. There is no live-workspace probe option.

## Discovery and integration audit

The reference implementation inspected was Google Research Harness² commit
`1cfe254e8d25b15f37048c3730134686abe60335`:

- [Paper](https://arxiv.org/html/2610.14224v1), task-level contrastive improvement
  and cross-task experience.
- [`core/tree.py`](https://github.com/google-research/harness2/blob/1cfe254e8d25b15f37048c3730134686abe60335/harness2/core/tree.py), component surfaces, inherited edits, validation and context caps.
- [`core/recursion.py`](https://github.com/google-research/harness2/blob/1cfe254e8d25b15f37048c3730134686abe60335/harness2/core/recursion.py), baseline, invariant/domain candidates, composition and combined execution.
- [`core/prompts.py`](https://github.com/google-research/harness2/blob/1cfe254e8d25b15f37048c3730134686abe60335/harness2/core/prompts.py), earliest observed failure, product comparison, KEEP_BASE and selective reuse.
- [`core/proposer.py`](https://github.com/google-research/harness2/blob/1cfe254e8d25b15f37048c3730134686abe60335/harness2/core/proposer.py) and [`core/library.py`](https://github.com/google-research/harness2/blob/1cfe254e8d25b15f37048c3730134686abe60335/harness2/core/library.py), bounded improvement sessions, per-domain experience and provenance.
- [`EXPERIMENTS.md`](https://github.com/google-research/harness2/blob/1cfe254e8d25b15f37048c3730134686abe60335/docs/EXPERIMENTS.md), comparable settings and sampling limitations.

No Python code or agent framework was ported. In particular, upstream's
unrestricted executor access and permission-skipping flags are not adopted.

| Harness² surface | Existing llmtui capability | Initial optimizer support |
|---|---|---|
| System prompt / procedure | `prompt.Compose`, templates, agent directives | Partial: additive, catalog-owned procedure guidance; original system rules cannot be edited |
| Guardrails | `tools` classification/confinement, TUI admission, MCP approvals | Existing immutable authority; no optimizer edits |
| Memory / context | `contextmgr`, `memory`, `memoryindex`, RAG, retained entities | Partial: bounded catalog read-coverage hints; experience is a separate store, never ordinary memory |
| Tool guidance | Tool catalog/instructions; `tools.Parse` and `CallsFromNative` | Implemented: reviewed advisory tool-use edits; no parsing or tool permissions changed |
| Skills | `skill.Manager`, `prompt.SkillPrompt`, approved `skill_load` | Existing support; automatic selection edits deferred until skill availability and content are bound to each experiment |
| Scripts | Approval-gated `run_command` | Execution/editing unsupported in experiments |
| Subagents | No independent delegated executor in the bounded agent | Not applicable to this slice; no new scheduler |
| Middleware / plugins | Declarative skill plugins, existing turn runtime | Optimizer edits unsupported; no executable middleware added |

Existing ownership remains unchanged: `agent` owns policy, mechanical failure
checks and exact-read proof; `agentverify` owns semantic controller requests;
`turnRuntime` and `agent_loop.go` own execution, approvals, recovery and stale
completion handling. `agent_yield.go` and `agent_observer.go` are unchanged.
The new lower-level `harness` package imports no TUI code. It calls the existing
file runner only for application-owned fixtures. It does not reproduce the
agent or semantic verifier loop.

New code fills only these gaps: overlay identity/validation, restricted fixture
comparison, proposal selection, atomic review state and selective reflection.
`cli/harness.go` exposes them. `tui/harness.go` supplies validated catalog text
to the existing composer during an explicitly opted-in active `/agent` run.
Ordinary chat receives no overlay even in an opted-in session. The contract
and semantic verifier do not receive it. `/prompt composed` exposes the section
while applicable. Cache identity and native continuation prefixes continue to
use the composed static system; the raw user message remains verbatim.

## Run, inspect and review

No configuration keys are required. These are explicit commands and flags,
not another YAML configuration hierarchy:

```sh
llmtui harness run tool-use
llmtui harness run coverage --protocol fenced
llmtui harness inspect
llmtui harness inspect EXPERIMENT_ID
```

`run` prints a bounded machine-readable JSON report and stores it privately.
`inspect` with no ID includes the active selection, retained experiments,
rollback stack and experiences. With an ID it includes component additions
against the unchanged baseline, oracle criteria, receipts and recommendation
reasons referencing concrete evidence IDs. Use the IDs returned in JSON.
Available scenarios are `tool-use`, `premature`, `recovery`, `coverage`,
`misleading`, `unknown` and `equivalent`.

To ask the configured provider for two proposals explicitly:

```sh
llmtui harness run tool-use --model-proposals
```

This constructs one provider and reuses it sequentially. It uses the effective
provider/model and chat temperature/top-p; output is capped at 1024 tokens per
request. Capability negotiation follows existing provider reports. Supported
structured output uses JSON Schema or embedded GBNF. Other backends get strict
post-generation decoding. Unsupported requests, invalid JSON, extra/duplicate
keys, oversized responses, wrong scopes/evidence IDs and noncatalog edits are
rejected without replacing the baseline. There is no format-repair retry.
The provider receives only fixture evidence, catalog metadata and at most
three applicable experience records, never user conversations or host tools.
`ChatRequest.Isolated` preserves the backend's ongoing chat cache where
supported; it is a scheduling hint, **not** security containment.

The baseline is always the unmodified fixture harness, not an execution of the
current user workspace or a currently active production overlay. Reports bind
fixture version, initial-content digest, tool protocol, provider/model target
and available runtime fingerprint. All probe sampling is scripted; no provider
inference takes place in probes. Proposer sampling has no controlled seed and
may vary even at the same settings. No statistical performance claim follows
from a single experiment.

Review first, then explicitly select a recommendation:

```sh
llmtui harness adopt EXPERIMENT_ID
llmtui chat --experimental-harness
# In that session: /agent on, and /tools on separately if needed.
```

Adoption changes a private selection pointer, never user-authored config or
project files. The chat flag is required each session. It selects advice for
the existing agent executor, grants no permissions and changes no verifier or
stop decision. Provider/model/runtime must match exactly. A switch suppresses
incompatible advice. Simulation provenance is visible in the prompt and report;
manual adoption is an experiment by the user, not certified model improvement.

```sh
llmtui harness revert                 # previous selection, including disabled
llmtui harness disable                # clear selection and rollback stack
llmtui harness reject EXPERIMENT_ID   # remove an unselected report and lessons
```

These affect future opted-in sessions. A running session holds an immutable
selection; restart without `--experimental-harness` to stop using it. `revert`
requires an earlier selection; `disable` also prevents later rollback from
silently restoring an old selection. Referenced active/rollback reports cannot
be rejected until released.

## Comparison and evidence

The unchanged baseline and invariant/domain candidates each start from newly
generated identical files. Candidates must pass **every** oracle criterion,
retain criterion identity and avoid deterministic failures. The composer uses
`KEEP_BASE`, `ADOPT_INVARIANT`, `ADOPT_DOMAIN`, `COMPOSE_SUPPORTED_EDITS` or
`INCONCLUSIVE`. Equal behavior prefers the baseline. Missing correctness
oracles remain inconclusive, including agreement on an unsupported claim.
Lower tool counts alone never establish improvement.

Composition requires both candidates to pass independently, disjoint modified
components and a fourth fresh combined probe that passes all criteria. Overlap
prefers one passing candidate rather than concatenating both. Each retained
component has a reason linked to independent and combined evidence. These
are fixture-only recommendations, not automatic global replacements.

Receipts distinguish actual actions from narration. Artifact contents are
checked independently. Typed recovery permits one corrected attempt rather
than blind repetition. Read coverage uses actual `ResultMeta` source digests,
windows and totals through `agent.ReadCoverage`. `agent.EvaluateDeterministic`
vetoes simulated semantic success on failed tests or unrecovered tool errors.
Tool counts/errors, invalid calls, repetition, recovery, read coverage,
artifacts, failed tests, criteria, stop reason and elapsed time are reported.
Provider requests and estimated/reported tokens are recorded for proposals;
fixture probes consume no inference tokens. Raw tool bodies are not recorded.

Example expected deterministic matrix (both protocols; no model-quality claim):

| Scenario | Baseline | Corrected invariant probe | Decision |
|---|---|---|---|
| Tool use | 0 actions, missing artifact | 1 executed write, valid contents | Compose only after independent domain and combined probes pass |
| Premature completion | Claim only, missing artifact | Artifact oracle passes | Adopt invariant; overlapping procedure edits are not combined |
| Recovery | 2 failures, 1 repeated call | 1 typed failure, 1 corrected successful read | Adopt invariant |
| Exact read | 1/3 delivered lines | 3/3 lines, consistent source version | Adopt invariant |
| Misleading evaluation | Semantic pass, failed deterministic test | Deterministic failure still vetoes | Keep base |
| Unknown correctness | No independent oracle | Semantic agreement remains insufficient | Inconclusive |
| Equivalent | Oracle passes | Same correctness | Keep base |

## Experience and persistence

```sh
llmtui harness reflect EXPERIMENT_ID
llmtui harness inspect
llmtui harness delete-experience EXPERIENCE_ID
```

Reflection is explicit and deterministic. Only a passing recommended correction
can become a structured `EXPERIENCE.md` equivalent: domain/trigger, observed
oracle failure, tested catalog edit/component, experiment/baseline/candidate/
evidence IDs, provider/model/runtime, validation provenance and simulation
caveat. Rejected or inconclusive hypotheses are not retained as successful
lessons. No unrestricted reflection prose, chain-of-thought, raw task prompt,
conversation, provider errors or tool output enters the library. This is true
even when `privacy.store_prompts` is enabled; disabling it never leaks the task
through a parallel store. Compatibility metadata rejects secret-shaped text
with the existing `redact` helper and terminal controls with `terminaltext`.

Future experiments retrieve at most three entries with exact domain/trigger
and compatibility matches. Entries expire after 30 days and remain bound to
retained reports and fixture-v1 provenance. Changed fixture versions or reviewed catalog text invalidate
old reports/snapshots rather than implicitly reinterpreting them. Snapshot
content fingerprints include the current catalog text. Reuse supplies evidence
to proposals and always re-probes the selected correction; it never injects a
library into production prompts or assumes old workspace facts still hold.

State lives in `harness/state.json` beside the selected config, or under the
default config directory. `--harness-state-dir DIR` overrides this for commands
and opted-in chat. The directory is `0700`, the file `0600` on Unix; replacement
uses `atomicfile.WriteFile`. There are at most 24 reports, 32 lessons, eight
rollback selections and 2 MiB total serialized state. Eviction protects active
and rollback reports and removes lessons whose report was evicted. Unknown
schema versions and corrupt/incomplete files fail closed without overwriting
existing data. This is initial schema v1; no older schema migration exists.

An exclusive `write.lock` rejects concurrent writers. Readers see the previous
or next complete atomic file. After a writer crash, confirm no writer remains
before removing `write.lock` manually. Back up/move a corrupt `state.json`
explicitly before starting fresh; automatic recovery never discards it.
Cancellation before commit writes nothing; there are no asynchronous adoption
callbacks. Once an atomic commit begins it is the transaction's commit point.

## Budgets, containment and deferred work

Each run has a two-minute deadline, up to two 45-second proposer requests, no
retries, three probes or four with composition, six fixture file calls per
probe, at most three edits, 8192 response bytes per proposal (including reported
reasoning activity), 10,000 estimated input-plus-reserved-output tokens per
request and 24,000 estimated/reported experiment tokens. Provider max_tokens is
advisory; byte/activity limits and cancellation also bound inbound output.
Token estimates cannot establish exact hidden backend compute. There is no
parallel or deeper recursion.

Fixtures use fresh private temporary directories and only compiled-in input.
Only trusted, bounded `read_file`/`write_file` scripts with exact fixture paths
are admitted. The existing runner and protocol normalizers are reused. Cleanup
failure fails the experiment. There is no caller-supplied executor, source
workspace, symlink tree, shell, writable home, cache, environment mutation,
package install, network tool, personal data access or MCP operation. No
process isolation is claimed: the safe boundary is refusing untrusted action
execution altogether. An explicitly configured proposer may contact its model
endpoint; default runs never construct one.

Deferred: contained live-agent replay, custom/user tasks and fixture imports,
real-model efficacy measurement, arbitrary generated instructions, automatic
skill selection, executable scripts/plugins/subagents, multi-step/parallel
recursion and broad domain similarity retrieval. Live replay needs an audited
process/filesystem/network containment adapter around the **existing** executor;
a separate directory or `cmd.Dir` does not meet that requirement. Production
instruction efficacy must be re-evaluated with real models before promotion.

## Validation

```sh
go test -count=1 ./internal/harness ./internal/cli ./internal/prompt
go test -count=1 ./internal/tui -run '^TestExperimentalHarness'
go test -race -count=1 ./internal/harness
go test -count=1 ./...
go vet ./...
golangci-lint run ./...
make check
```

The fixture tests require no model download, paid API or live network. They
cover scenarios A–J through `harness_test.go`, `store_test.go`, CLI lifecycle,
prompt framing and TUI opt-in tests. Existing agent/tool tests retain the full
read-version, approval, yield and native/fenced correlation regression suite.
Neither synthetic probes nor ordinary Go tests validate a real GGUF, local
endpoint, native grammar compilation, or real-provider proposal quality.
