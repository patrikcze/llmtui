# Structured decision engine

`internal/decision` provides optional typed local decisions separately from
text-generating providers. Laya returns `choice`, ordinal `score`, and boolean
`noul` probabilities. The existing agent loop, verifier, and tool approval
policy remain authoritative. A prediction is a signal, never authorization
to execute a tool. Every observation this package makes is shadow-only
(recorded, never acted on) with one narrow, structurally bounded exception:
`decision_engine.mode: guarded_assist` may force one additional semantic
verification an adaptive policy would otherwise have skipped — see "Guarded
verifier escalation (Phase 1)" below. That mode ships with zero behavioral
effect in this codebase (no approved calibration profile exists yet), and
Laya can never skip, delay, or replace a verifier, satisfy a criterion, or
otherwise author a status this package doesn't already compute
deterministically.

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
manager. `Router.Close` never waits for an in-flight prediction or a cold
model load (Phase 0c, below): a busy loaded entry is marked for its
releasing caller to close later, and a load still in progress is cancelled
and left for its own goroutine to unwind and discard.

Each engine serializes an entire exchange using a cancellable gate. A queued
cancellation leaves the active exchange alone. Cancellation during I/O kills
the worker and renders that engine unusable; its late response cannot be
consumed by a later request. Close is idempotent, closes pipes, kills the
contained process group, and joins the sole child waiter. Stderr is continuously
drained into a bounded 16 KiB tail, available via `Diagnostics` for explicit
debugging; it is never copied into a decision or ordinary error message.

### Non-blocking reload/close during a cold load (Phase 0c)

`Router.acquire` previously held the Router mutex across the entire
`LoadRuntime` call — disk verification, spawning the Python subprocess, and
its `ready` handshake. `Router.Close` (invoked synchronously from
`/config reload` and application shutdown, see `agent_decision_shadow.go`)
takes the same mutex, so a config reload or shutdown while any alias was
cold-loading would block behind that load for as long as it took, up to
`startMLX`'s own two-minute import/init bound. This is fixed, still purely a
lifecycle change: no loading, eviction, or `MaxLoaded` policy is different.

- **acquire reserves, then releases the lock before I/O.** The first caller
  to observe no cached entry for an alias inserts a placeholder entry and
  launches exactly one background goroutine (`runLoad`) to perform the
  actual `Installation` lookup and `LoadRuntime` call, then returns to
  waiting like every other caller. No goroutine ever runs that I/O while
  holding the mutex.
- **Every caller for that alias waits on the same load**, including the one
  that started it, via a channel closed once `runLoad` publishes a result —
  never by polling or holding the lock. Concurrently calling `Predict` many
  times for a brand-new alias still triggers exactly one `Load` call
  (`TestRouterConcurrentAcquireLoadsOnce`).
- **A cancelled waiter's ctx never cancels the shared load.** The load's own
  context is independent of any individual caller's; one caller giving up
  only releases that caller's own reservation, and a second, still-waiting
  caller's `Predict` still succeeds from the same load
  (`TestRouterAcquireWaiterCancellationDoesNotAbortLoadForOthers`).
- **`Close` cancels pending loads and returns immediately** — it never waits
  for a load's goroutine to actually unwind
  (`TestRouterCloseDoesNotBlockOnInFlightLoad`). A load that fails from that
  cancellation is removed without ever being cached
  (`TestRouterLoadFailsAfterCloseNeverPublishes`); a load that finishes
  successfully anyway (a loader that doesn't check `ctx` promptly) is closed
  immediately instead of published
  (`TestRouterLateSuccessfulLoadClosesEngineAfterClose`).
- **Eviction never touches a still-loading entry.** An idle, fully loaded
  entry beyond `MaxLoaded` is still evicted as before; an entry with a load
  in flight has no engine yet and is skipped regardless of map iteration
  order (`TestRouterEvictionSkipsEntryStillLoading`).
- **No Engine.Close call — eviction's, a busy release's, or Close's own —
  ever runs while `r.mu` is held.** Every closed engine is collected under
  the lock and closed only after unlocking, so a slow worker teardown can
  never block an unrelated `acquire`/`Predict`/`Close` either
  (`TestRouterReleaseAfterCloseClosesBusyEngine`, `TestRouterCloseIsIdempotent`).

## Protocol and result contract

The embedded worker uses version 2 JSON lines over stdin/stdout (bumped from
1 by Phase 0b, see below). `init` contains `model_path`; `ready` includes
package/Python versions and timing. `probe` checks platform, imports, pinned
package version, and Metal without loading a model. `predict` contains
`state`, `questions`, a monotonically increasing ID, and an optional `strict`
flag; `result` must echo the protocol and ID. Both sides bound frames to 8
MiB. Native and Python diagnostic stdout are redirected to stderr before MLX
imports, preserving the protocol channel.

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

### Token-capacity and provenance diagnostics (Phase 0b)

Two gaps existed before any active-influence phase could be trusted: a
question's head/options/state could be silently truncated by the pinned
tokenizer's fixed budget with no way to tell, and a prediction's `Routing`
carried no binding to the exact installation revision that actually produced
it (`Router.Predict` re-derived `Repository` from a fresh catalog query,
which can drift if a newer installation appears while the loaded engine is
still cached). Both are fixed, still 100% diagnostic — neither changes any
answer or agent behavior on its own:

- **Exact token usage, not a Go-side estimate.** `mlx_worker.py` measures
  every question's admitted head/option/state token counts and whether raw
  (untruncated) content exceeded them, using the exact pinned tokenizer
  (`agent.tok`) and the exact upstream sequence-construction functions
  (`laya_mlx.common.build_prefix`/`render_options`/`serialize_state`) that
  `agent.predict()` itself uses — never a second, independent tokenizer, and
  never estimated from Go byte counts. This runs before every predict call
  (cheap, tokenizer-only, no GPU forward pass) and is reported in the
  `result.input_usage` field, decoded into `decision.Result.InputUsage`
  (`map[string]decision.QuestionInputUsage`). A missing entry for a question
  means the backend could not measure it — never a confident zero-truncation
  claim.
- **`PredictOptions.RequireCompleteInput`** (wire: `strict`) requests
  admission control: if any question's input would lose content, the worker
  rejects with `code="capacity"` — mapped to `ErrInvalid` — *before* running
  the GPU forward pass, so a strict caller can never receive an answer
  computed from truncated input. Rejection is a verdict about that specific
  request, not the worker's health: the same engine stays usable for the
  next request (mirrors the existing `code="request"` handling). Default
  `RequireCompleteInput` is `false`; every existing caller is unaffected
  except that `Result.InputUsage` now also comes back populated.
- **Exact loaded-installation provenance.** `Router.acquire` now binds the
  selected installation's `Manifest.Source.Revision` onto the cached
  `routerEntry` at load time and returns it alongside the engine;
  `Router.Predict` sets `Routing.Revision` from that bound value, never from
  a later catalog re-query — so it cannot silently relabel predictions if a
  newer installation appears while the currently-loaded engine is still in
  use (`TestRouterBindsLoadedRevisionNotNewestCatalog`).
- **`validateQuestionInputUsage`** rejects a usage map that names an unknown
  question, has non-positive `max_len`, negative counts, or admits more
  tokens than its own budget — a corrupted or hand-crafted worker response
  is never silently trusted for capacity accounting.

Real-tokenizer boundary behavior (exact-fit, one-token overflow, head/option/
state truncation independently, multilingual/non-ASCII content, and every
question type) is verified against the real pinned tokenizer and checkpoint
in `TestMLXIntegration`'s strict-mode assertions — protocol mocks alone
cannot prove a real tokenizer boundary is measured correctly.

## Validation and reproducibility

Normal tests use the Go test executable as a fake worker, without Python or
models. They cover framing, mapping, reuse, concurrent requests, cancellation,
crashes, stderr flooding, broken pipes, startup errors, close/reap, source
integrity, router recovery/eviction, platform gating, strict-mode capacity
rejection without killing the worker, and rejection of a malformed/corrupted
`input_usage` response.

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
one first and five warm predictions per case in the same worker, then (Phase
0b) one strict-mode predict on that same small fixture state (must succeed
unchanged) and one on a deliberately oversized state (must reject with
`ErrInvalid` and leave the engine usable for the next request). It reports
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

### Guarded verifier escalation (Phase 1) — shipped inert

Laya's one active capability — the first and, so far, only exception to
this package's shadow-only rule above — is a **one-way escalation** to
semantic verification, using the pre-verifier shadow's
`semantic_verifier_needed` signal specifically (not the post-cycle
`cycle_action` choice). See
`docs/architecture/decisions/0012-laya-guarded-verifier-escalation.md` for
the full authority decision and `internal/tui/agent_decision_policy.go` for
the implementation.

**Allowed, as the only active behavior:**

```text
adaptive policy WOULD SKIP the semantic verifier
        +
Laya semantic_verifier_needed >= a calibrated threshold
        ↓
RUN the semantic verifier anyway
```

**Forbidden, permanently, never any active mode:**

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
future phase — escalation-only, one direction, is the constraint this
implementation preserves structurally: `dispatchGuardedAssist` is only ever
called from the one branch of `startAgentVerification` where
`planAgentVerification` marked the cycle's route `GuardEligible`, and that
flag is never set for a route that was already going to dispatch a real
verifier (see `TestPlanAgentVerificationBranches` and
`TestGuardedAssistNeverDispatchedForSemanticRoute`).

**`decision_engine.mode: guarded_assist` ships with zero behavioral effect
in this codebase.** Activating it additionally requires a resolved
`decisionCalibrationProfile` for the loaded model — a versioned Go value in
`decisionCalibrationProfiles`, never user-editable config, never a public
threshold knob, never defaulted — and that map is **empty**. No confidence
threshold has been calibrated; ADR 0012 was accepted without a completed G1
report (an explicit, informed exception — see the ADR's own Context
section), so until a future, separately reviewed change adds an approved
profile backed by real evidence, setting `mode: guarded_assist` is
observationally identical to `shadow`. `TestGuardedAssistWithNoProfileNeverDispatches`
and `TestGuardedAssistDisabledEngineNeverDispatches` prove this directly.

When a profile does exist for the loaded model, eligible cycles get exactly
one Laya call — the guarded predict result also feeds the ordinary
pre-verifier correlation bookkeeping, so a guard-eligible cycle is never
double-dispatched (shadow plus assist) for the same stage
(`TestGuardedAssistFeedsPreVerifierCorrelationExactlyOnce`). The call is
**strict** (`PredictOptions.RequireCompleteInput`, Phase 0b): a truncated
input can never produce a successful guarded answer, only an "unavailable"
fallback to the original synthetic result — never a confident decision
made from incomplete evidence. Its context derives from the run's own
(unlike every shadow call, which is deliberately independent so it can
still record a late-arriving actual outcome after the run ends), and its
deadline is `min(profile.MaxWait, run's own remaining elapsed budget)` — a
guarded wait can never itself cause a run to exceed
`agent.Limits.MaxElapsed`. `/debug last`'s `laya guarded assist` line and
`eval.AgentTrial`'s `LayaGuardedAssist*` fields report the eligible
profile, probability, threshold, whether it escalated, and a bounded
reason code (`escalated`, `below_threshold`, `unavailable`, `timeout`,
`cancelled`, `malformed_result`).

### Pinned criterion assessment metadata (Phase 2)

Contract callers may explicitly request assessment metadata version 1 for
evaluation fixtures through `ContractInput.AssessmentVersion`. Ordinary
contracting leaves this field zero, so its response schema and prompt remain
unchanged. The optional extension attaches at most one bounded neutral
proposition to each existing criterion by its response index. It accepts only
`receipts` or `local_read` evidence, and a `local_read` target must be one
literal workspace-relative path. Globs, selectors, URLs, URIs, shell text and
read instructions are rejected.

The metadata is pinned inside the existing `AgentRun.Criteria` records, with
the existing criterion ID and text, in one atomic contract transition. It is
not a second criteria registry, question store, evidence ledger or Laya result
cache. Invalid optional metadata is discarded as a whole while valid core
criteria remain usable; clarification contracts and ask-user delegation never
retain provisional assessments. The existing TUI contract path only passes
validated attachments to the existing owner.

Phase 2 is evaluation-only. The metadata does not affect deterministic
criteria, verifier input, tool selection, approval, completion, or normal chat
behavior. Persistence keeps only validated claims; if shared secret redaction
would change a proposition or target, the attachment is omitted rather than
persisted in altered form. Resume validates the optional field again and strips
invalid historical attachments while keeping the schema-v1 run loadable. No
Laya inference or new runtime mode is introduced by this phase.

### Criterion assessment shadow (Phase 3)

`decision_engine.mode: criterion_shadow` is an explicit evaluation mode. New
task-contract requests in this mode opt into assessment metadata version 1;
ordinary `shadow` and `guarded_assist` contracts keep the legacy schema. The
metadata remains optional: a contract can be valid without any assessment
attachment, and a clarification or ask-user delegation never carries one
forward.

After deterministic criteria have been applied and before verifier routing,
the TUI builds at most one bounded batch from pinned semantic assessments. A
`receipts` assessment uses exactly one successful, executed current-cycle
receipt. A `local_read` assessment additionally requires exactly one matching
`read_file`, one complete current-cycle observation-cache excerpt, and no later
write/edit/command that could invalidate it. Missing, stale, truncated,
evicted, or ambiguous evidence abstains before inference; the path never
rereads the workspace or calls a tool. Each admitted state contains one
framed/redacted proposition and at most one 512-byte excerpt, with a 2 KiB
serialized-state ceiling and a five-second total batch budget.

Each admitted criterion receives two fixed `noul` questions: direct support and
direct contradiction. The result is recorded only as a content-free
measurement bound to the criterion/spec/evidence fingerprints and model
revision. Support, contradiction, and ambiguous probabilities are advisory
signals; low support is not criterion failure, and no signal changes
`Criterion.Status`, `Evidence`, verifier input/routing, tool approval, stop
decisions, or persisted run authority. The mode is therefore safe to disable:
removing the mode stops assessment requests while the existing agent loop and
verifier behavior remain unchanged.

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
