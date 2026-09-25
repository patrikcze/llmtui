# Structured decision engine

`internal/decision` provides optional typed local decisions separately from
text-generating providers. Laya returns `choice`, ordinal `score`, and boolean
`noul` probabilities. The existing agent loop, verifier, and tool approval
policy remain authoritative; this runtime does not yet use decisions to alter
them. A prediction is a signal, never authorization to execute a tool.

## Execute on Apple Silicon

The first executable backend uses **laya-mlx 0.2.0**, Python 3.11+ running
natively on macOS arm64, and Metal. No Go dependency or HTTP service is needed.
Explicitly install the Python dependency into an isolated environment:

```sh
python3 -m venv ~/.local/share/llmtui/runtimes/laya-mlx-0.2.0
~/.local/share/llmtui/runtimes/laya-mlx-0.2.0/bin/python -m pip install 'laya-mlx==0.2.0'
```

Configure the executable, or pass `--python /absolute/path/to/python` to the
commands below. A command string with flags is not accepted. Empty uses
`python3` from PATH; there is no automatic environment search or installation.

```yaml
decision_engine:
  enabled: false # agent policy integration remains a separate step
  provider: laya
  laya:
    default_model: english-mlx
    model_dir: "" # or an absolute path to the Laya model store
    max_loaded: 1
    mlx_python: /absolute/path/to/venv/bin/python
```

The environment override is `LLMTUI_DECISION_ENGINE_LAYA_MLX_PYTHON`.
`decision_engine.laya.model_dir` selects the exact shared Laya store root for
`models`, `pull`, `inspect`, `verify`, `remove`, and `predict`. For example,
`/Volumes/Models/laya` stores revisions under
`/Volumes/Models/laya/english-mlx/<revision>/`. Use an absolute path; shell
expansions such as `~` and `$HOME` are not performed. Empty retains the default:
`$XDG_DATA_HOME/llmtui/models/laya` or `~/.local/share/llmtui/models/laya` on
macOS/Linux, and `%LOCALAPPDATA%/llmtui/models/laya` on Windows.
`LLMTUI_DECISION_ENGINE_LAYA_MODEL_DIR` overrides YAML. Changing the setting
does not move existing installations; pull models into the selected store.
This is separate from the Python environment selected by `mlx_python`.

Explicit CLI commands opt in independently of the future chat-policy switch.
Normal startup, `doctor`, and model inspection never launch Python or pip.

```sh
llmtui decision runtime status mlx
llmtui decision pull laya:english-mlx --revision 047678560251f28113ee8f5df4be82102c7bf336
llmtui decision predict laya:english-mlx --input internal/decision/testdata/mlx/request.json
```

`predict` reads `{"state": ..., "questions": {...}}` from a file or stdin and
prints normalized JSON. It defaults to a two-minute total timeout; `--timeout`
can override it. One CLI invocation makes one prediction. Long-lived callers
use `NewRouter` with `MLXRuntimeLoader` and keep the router/service alive to
reuse the worker across predictions.

All three aliases use the same backend, selected solely by the installed path:

| Alias | Published checkpoint revision |
| --- | --- |
| `laya:english-mlx` | `047678560251f28113ee8f5df4be82102c7bf336` |
| `laya:multilingual-mlx` | `ba40c87fcb357f1643d04d71323af9cdc3b9e591` |
| `laya:typed-decisions-mlx` | `28416e78cb26a239a4eabaa2e084904ec5e6cacb` |

Use `decision pull` explicitly for each alias. Non-Apple platforms may still
download, inspect, and verify the checkpoints, but execution fails clearly.
Source aliases without `-mlx` still require a future compatible backend.

## Architecture and lifecycle

```text
Service -> Router -> LoadRuntime -> MLXRuntimeLoader -> MLXEngine
                         |                             |
                  verify manifest/files       persistent Python worker
                                                       |
                                              laya_mlx.Agent.predict
                                                       |
                                                  Metal GPU
```

`RuntimeLoader` remains the execution boundary. Its optional
`SourceRuntimeLoader` capability permits a verified MLX bundle to execute
without changing `runtime.ready=false` in the installation manifest. That
field still describes a standalone executable model artifact, not Python
availability. Ready ONNX artifacts retain their size/hash/exporter checks.

The model manager owns explicit downloads, pinned revisions, staging, and
SHA-256 verification. Immediately before launch, the MLX boundary checks all
manifest file hashes, required inputs, symlinks, and unexpected tokenizer or
encoder files. The worker receives an absolute existing directory as a
`pathlib.Path`, sets `HF_HUB_OFFLINE=1` before importing Laya, and never receives
a Hub model identifier. Python does not download the checkpoint again.
The worker environment excludes API tokens and Python injection variables.

One engine owns one process and one loaded Agent. The Go Router owns reuse,
references, MaxLoaded/LRU eviction, and closing. Busy entries may temporarily
exceed MaxLoaded; release evicts idle surplus entries. Failed workers retire
and are recreated on a subsequent request, without retrying a failed prediction.
The Python Laya Router is deliberately unused: there is only one residency
manager. `Router.Close` waits for in-flight references before closing engines.

Each engine serializes an entire exchange using a cancellable gate. A queued
cancellation leaves the active exchange alone. Cancellation during I/O kills
the worker and renders that engine unusable; its late response cannot be
consumed by a later request. Close is idempotent, closes pipes, kills the
contained process group, and joins the sole child waiter. Stderr is continuously
drained into a bounded 16 KiB tail, available via `Diagnostics` for explicit
debugging; it is never copied into a decision or ordinary error message.

## Protocol and result contract

The embedded worker uses version 1 JSON lines over stdin/stdout. `init`
contains `model_path`; `ready` includes package/Python versions and timing.
`probe` checks platform, imports, pinned package version, and Metal without
loading a model. `predict` contains `state`, `questions`, and a monotonically
increasing ID; `result` must echo the protocol and ID. Both sides bound frames
to 8 MiB. Native and Python diagnostic stdout are redirected to stderr before
MLX imports, preserving the protocol channel.

Loads use FP16, GPU, batch_size=16, compile=false, cache_prompts=false. These
match the baseline reference; compilation and prompt caching are deliberately
not exposed until measured separately. Upstream batches questions internally.

The bridge maps upstream `noul` to `Answer.Probability` and preserves choice,
score, confidence, and probability values. Missing values, unexpected labels,
invalid types, non-finite/out-of-range values, and out-of-rubric scores fail.
`ValidateResult` runs before returning. Upstream `action.act_probability`,
usage, and legends have no fields in the existing public result and are not
used for policy. Go never calls `DecodeLogits` on already calibrated MLX
answers. `BuildSequence` and `DecodeLogits` remain for future native backends.
Map keys are sorted by Go JSON encoding; ordered choice lists preserve order.

## Validation and reproducibility

Normal tests use the Go test executable as a fake worker, without Python or
models. They cover framing, mapping, reuse, concurrent requests, cancellation,
crashes, stderr flooding, broken pipes, startup errors, close/reap, source
integrity, router recovery/eviction, and platform gating.

Real integration is explicit and performs no downloads:

```sh
LLMTUI_TEST_LAYA_MLX=1 \
LLMTUI_TEST_LAYA_MODEL_ROOT=/absolute/path/to/llmtui/models/laya \
LLMTUI_TEST_LAYA_PYTHON=/absolute/path/to/python \
go test -count=1 -v ./internal/decision -run '^TestMLXIntegration$'
```

The existing `GoldenFixture` schema is unchanged. The accompanying provenance
files record runtime version, checkpoint revision, FP16, platform, and 0.002
probability tolerance. Regenerate only with all three pinned installations:

```sh
python internal/decision/testdata/mlx/reference.py \
  --store /absolute/path/to/llmtui/models/laya \
  --output internal/decision/testdata/mlx
```

This generator calls **direct** `Agent.predict`, without the bridge, with
socket connections disabled. English, multilingual (including French input),
and typed-decisions fixtures cover choice, score, and noul. Integration runs
one first and five warm predictions per case in the same worker. It reports
process/bootstrap, Python import, model load, first, and warm timings
separately. Process/bootstrap is measured handshake time less reported import
and load, so includes interpreter and protocol overhead. Hash verification is
outside these timings. These are conversion/bridge fidelity checks, not proof
that Laya is an accurate verifier or a safe tool-selection policy.

## Future backends and agent integration

[Apple MLX](https://github.com/mizorewww/laya-mlx) and
[GoMLX](https://github.com/gomlx/gomlx) are different frameworks.
[onnx-gomlx](https://github.com/gomlx/onnx-gomlx) consumes ONNX graphs, not the
existing MLX SafeTensors bundle. Reimplementing Laya to avoid Python would
introduce another unvalidated architecture. A verified ONNX export can use a
separate loader and the existing Go preprocessing/decoding contracts.

[Laya Core ML](https://github.com/mizorewww/laya-coreml) is a promising separate
backend. Its reported 4.98 ms P50 on M3 Max uses the multilingual ANE FP16
96-token model; that bound includes state and question. General-purpose
1024-token models differ, and the short benchmark does not establish verifier
latency for longer inputs. A future Swift/Objective-C Core ML helper could
avoid Python, but would need its own verified compiled-model assets,
tokenization, host-side tensors, capacity checks, decoding, and parity tests.
It can implement the same Engine/RuntimeLoader boundary without changing
provider behavior.

After evaluating task-specific accuracy, tool shortlisting and verifier hints
can consume `Service` results conservatively. They must preserve deterministic
checks, tool approvals, and the existing generative fallback. Those policy
changes are intentionally outside this runtime implementation.

### Shadow advisor (this phase)

`internal/tui` now owns a long-lived `decision.Service`/`decision.Router`
(`internal/tui/agent_decision_shadow.go`), constructed in `rebuildFromConfig`
only when `decision_engine.enabled` is true and closed on `/config reload`
and application shutdown. Nothing about model loading changes: the Router's
own lazy `acquire` still only spawns a worker on the first `Predict` for a
model alias, so enabling the flag alone launches nothing.

After `agent.Decide()` resolves each agent-loop cycle (`internal/tui`'s
`handleAgentVerification`, immediately after `ApplyStop`), a bounded snapshot
of the cycle — task and objective (truncated), cycle number, acceptance
criteria and their statuses, succeeded/failed tool names, permission-denied
and needs-user-input flags, changed files, test pass/fail counts,
deterministic error kinds, and the unresolved-criteria counts — is sent to
Laya in one batched `Predict` call asking three questions: `cycle_action`
(choice: `finish`/`semantic_verify`/`continue`/`ask_user`/`blocked`),
`goal_complete` (noul), and `semantic_verifier_needed` (noul). The prediction
is recorded in `/debug last` under `laya shadow`, alongside the cycle's
actual verifier path and decision, and in a small in-memory rolling-metrics
counter (cycle-action agreement, false-finish count — the priority metric —
ask-user agreement, verifier-needed agreement/false-negative rate).

This is **shadow-only**: the call happens after `stop.Decision` is already
final, its result is never read by `agent.Decide()`, the verifier, tool
approval, or any criteria-update path, and a decision-runtime error (engine
disabled, unavailable, worker crash, timeout, malformed answer) only ever
records an unavailable reason. Its purpose is calibration evidence — see the
metrics above — not behavior change.

### Pre-verifier counterfactual shadow (second observation)

The shadow above always fires after `agent.Decide()` — useful for "did Laya
predict the cycle's final outcome," but it can't cleanly isolate "is Laya
good at judging whether semantic verification was needed" from "is Laya
good at predicting the final action," since by the time it's asked a
semantic verifier (if one ran) has already happened. A future
`guarded_assist` phase (below) would need exactly that narrower judgment
*before* the verifier runs. A second shadow call is dispatched inside
`startAgentVerification`, immediately after `run.ApplyDeterministicCriteria`
and before any verifier-mode branching — reusing the same compact
snapshot builder, so the state is provably deterministic-only (no verifier
`CriteriaUpdates` have landed yet at that point). It asks a narrower,
2-question set — `semantic_verifier_needed` and `evidence_sufficient`
(both noul) — never the 5-way `cycle_action` choice, since there is no
"final action" to predict yet.

The prediction and the cycle's later-arriving actual outcome (whether a
semantic verifier actually ran, its verdict, and the final decision) are
reconciled by a small, bounded, either-order-safe correlation record —
either half may arrive first, since the prediction resolves asynchronously.
Once both halves are present, the pair folds into a rolling, diagnostic-only
0.5-threshold confusion matrix (`/debug last`'s `laya pre-verifier` line)
and a bounded raw-sample history a threshold-sweep report can read later
(`internal/tui/agent_decision_calibration_test.go`, opt-in via
`LLMTUI_TEST_LAYA_MLX=1` like the real MLX integration test above). The
priority metric is **false negative**: Laya says verification is
unnecessary but the authoritative pipeline required one anyway — exactly
the case a future active gate must never produce. No threshold is selected
or acted on anywhere in this phase.

`/debug last`'s `laya shadow` line also now shows the full `cycle_action`
probability distribution, not just the winning choice — manual calibration
found the single winning probability (and `Confidence`) both close to
uninterpretable in isolation; seeing every option's probability is what
actually answers whether a losing option was ever seriously considered.

### Measurement integrity (Phase 0a)

The pre-verifier correlation's bookkeeping had several defects that would
have silently understated or overstated calibration evidence for any future
`guarded_assist` gate, without changing agent behavior itself. All are fixed
in `internal/tui/agent_decision_shadow.go`, still 100% shadow-only:

- **Dispatch-time registration.** `dispatchAgentDecisionPreVerifierShadow`
  now registers the correlation entry (and the generation that dispatch
  used) synchronously, before returning its async command — not on first
  arrival of either half. A later arrival is matched against that entry's
  own recorded dispatch, not against whatever cycle happens to be live when
  it shows up.
- **Late vs. dropped vs. cancelled vs. unavailable, counted separately**
  (`preVerifierShadowMetrics`). A prediction that finally arrives after a
  fast multi-cycle run has already moved on is `Late`: it still finalizes
  into the confusion matrix and the raw-sample history, but it can no
  longer overwrite `/debug last`'s fields for whatever cycle is actually
  live — only a genuinely current (same run, same cycle, same dispatch
  generation) arrival may do that. An arrival that can't be matched to any
  registered dispatch at all (never dispatched, or matched a generation a
  later same-cycle dispatch superseded) is `Dropped`. A pending entry
  invalidated by run cancellation or a decision-engine config reload —
  because the missing half can now never arrive through the normal path —
  is `Cancelled`. A `Predict` error is `Unavailable`, and is never treated
  as a confident probability of zero; unavailable/dropped/cancelled
  observations are counted but never appended to the raw-sample history.
- **Deterministic bounded eviction.** The 16-entry correlation map now
  evicts by a true insertion sequence, not Go's randomized map iteration
  order, so a full map behaves reproducibly under test and in a real
  calibration run.
- **Duplicate arrivals are idempotent.** A second arrival for a half already
  recorded is counted (`Duplicate`) and otherwise ignored — it can never
  double-count `Total`/`Available`/the confusion matrix.
- **Ground truth is a distinct axis from policy agreement.**
  `preVerifierSample` now additionally carries `Cycle`, `BaselineRoute`
  (which verifier path the deployed policy actually took), and three
  fields an evaluation harness — never production code — attaches
  afterward: `IndependentNeed` (an optional externally supplied label for
  whether verification was *actually* necessary), `LabelSource`, and
  `Availability`. The existing `computeThresholdSweep` stays exactly what
  it always was — a policy-agreement sweep against `ActualRan` — and a new
  `computeNeedThresholdSweep` computes the ground-truth sweep against
  `IndependentNeed` instead, explicitly excluding any sample with an
  unknown label or without a legitimate, available probability rather than
  silently counting it as a known negative. `eval.AgentTrial` gained the
  matching additive fields (`LayaPreVerifierAvailability`,
  `LayaIndependentNeed`, `LayaLabelSource`, `LayaCensorReason`).

None of this changes what any run does — every fix is either accounting
(what gets counted, and how) or a correction to what was already supposed
to be shadow-only bookkeeping. `decision_engine.enabled=false` still means
zero prediction/worker/model-load activity, exactly as before.

### The Snake-demo analogy

The design mirrors [laya-mlx's Snake
demo](https://github.com/mizorewww/laya-mlx/blob/main/docs/SNAKE_DEMO.md):

```text
deterministic planner  → bounded facts / admissible actions → Laya decision → deterministic safety shield
```

mapped onto this integration as:

```text
deterministic controller state → bounded controller actions → Laya shadow recommendation → existing deterministic/verifier policy remains authoritative
```

The compact snapshot above is the "bounded facts"; the fixed five-choice
`cycle_action` vocabulary is the "admissible actions" set; the shadow
prediction is currently observed only, never applied; and `agent.Decide()`
together with the existing verifier/criteria machinery is the safety shield
that never moves.

### Future phase (doc-only, not implemented here): `guarded_assist`

A possible next phase, not built in this PR, would give Laya exactly one
active capability: a **one-way escalation** to semantic verification, using
the pre-verifier shadow's `semantic_verifier_needed` signal specifically
(not the post-cycle `cycle_action` choice).

**Allowed, as the only initial active behavior:**

```text
adaptive policy WOULD SKIP the semantic verifier
        +
Laya semantic_verifier_needed >= a calibrated threshold
        ↓
RUN the semantic verifier anyway
```

**Forbidden, permanently, never the initial active mode:**

```text
adaptive policy WOULD RUN the semantic verifier
        +
Laya says verification is unnecessary
        ↓
SKIP the verifier
```

Laya must never be allowed to skip a verifier that would otherwise run,
mark a run done, authorize a tool, bypass an approval, override a
deterministic failure, or satisfy a criterion directly, in this or any
future phase — escalation-only, one direction, is the constraint any
implementation of `guarded_assist` must preserve. No confidence threshold
exists yet; selecting one requires calibration evidence from both shadow
observations above, with the pre-verifier shadow's false-negative rate as
the metric that would actually justify (or rule out) a specific threshold.

## Measured bridge validation (2026-09-23)

Real Metal integration passed on this development machine with Python 3.14.3
and laya-mlx 0.2.0. All three pinned checkpoints selected `billing`; all answer
values passed the direct-Python golden comparison (0.002 tolerance). These
four-question measurements are observations, not latency guarantees:

| Model/case | Process/bootstrap | Import | Model load | First prediction | Warm mean (5) |
| --- | ---: | ---: | ---: | ---: | ---: |
| English / duplicate charge | 21 ms | 154 ms | 68 ms | 51 ms | 32 ms |
| Multilingual / duplicate charge | 45 ms | 319 ms | 480 ms | 29 ms | 14 ms |
| Multilingual / French refund | same worker | — | — | 23 ms | 13 ms |
| Typed decisions / duplicate charge | 19 ms | 158 ms | 69 ms | 45 ms | 33 ms |

This establishes executable inference and bridge parity for the recorded
cases. It does not establish production readiness or verifier accuracy.
