# Harness experiment implementation report

Validated on 2026-10-21 (macOS arm64, Go 1.27.2), branch
`codex/harness-experiments`, based on
`c1b508198883055763496ac345021665cbc47d00`. Automated validation and
user-run embedded smoke-test evidence are distinguished below.

## Delivered architecture

`internal/harness` is a lower-level, opt-in experiment/review subsystem:

- `NewSnapshot`, `Snapshot.Validate`, `GuidanceText` and `Diff`: versioned,
  content-addressed, parented overlays, including catalog text in identity;
  protected instructions and permissions are outside the edit vocabulary.
- `Proposer.propose`, `ParseProposal`: at most two fresh, tool-free requests
  through the configured provider. Existing capability reports, streaming,
  JSON Schema/GBNF and cancellation are reused. Unknown backends receive
  strict post-generation decoding; invalid proposals preserve the baseline.
- `runFixture`, `Run`, `compare`, `Report.Validate`: sequential k=1 fixture
  execution, independent artifact/coverage oracles, structured evidence,
  conservative selection and optional independently tested composition.
- `Store.Update`, `State.Adopt`, `Revert`, `Disable`: atomic, bounded private
  persistence and explicit selection lifecycle. No config/source rewriting.
- `Reflect`, `Relevant`: explicit, evidence-bound, expiring reflection;
  selective domain/model/provider/runtime retrieval and re-probing.
- CLI `harness run/inspect/adopt/revert/disable/reflect/delete-experience/reject`.
  Only `chat --experimental-harness` can pass adopted guidance into existing
  agent executor prompts through `experimentalHarnessGuidance` and
  `prompt.Compose`. Ordinary chat, contract, semantic verifier and tool policy
  retain their existing behavior and authority.

Reused components: `tools.Parse`, `tools.CallsFromNative`, `Runner` file tools,
`ResultMeta`, `agent.ToolCallRecord`, `AppendReadObservation`, `ReadCoverage`,
`EvaluateDeterministic`, the provider abstraction/capabilities/cancellation,
`prompt.Compose`/`StaticSystem`, `untrusted.Frame`, `terminaltext.Sanitize`,
`redact.Secrets` and `atomicfile.WriteFile`. No new dependency was added.
The eight-surface capability/ownership audit and operating guide are in
[harness-experiments.md](harness-experiments.md).

## Concrete demonstration

The built CLI was run twice with `--provider mock --model harness-fixture`
and a fresh private `/tmp/llmtui-harness-validation-20261021` store:

```sh
llmtui --provider mock --model harness-fixture harness run tool-use
llmtui --provider mock --model harness-fixture harness run tool-use --protocol fenced
```

| Protocol | Experiment ID | Baseline | Invariant | Domain | Combined | Recommendation |
|---|---|---|---|---|---|---|
| Native | `fcb38dc740597c4ad2cb5b0e` | 0 calls, artifact absent, oracle fails | 1 executed write, valid contents | 1 executed write, valid contents | 1 executed write, valid contents | COMPOSE_SUPPORTED_EDITS |
| Fenced | `bf74b4cf8ad88e662758c710` | 0 calls, artifact absent, oracle fails | 1 executed write, valid contents | 1 executed write, valid contents | 1 executed write, valid contents | COMPOSE_SUPPORTED_EDITS |

Raw JSON reports are `/tmp/llmtui-harness-native.json` and
`/tmp/llmtui-harness-fenced.json`. Neither recommendation was activated.
These are real fixture file operations and oracle checks driven by scripted
trajectories. They establish plumbing/conformance, **not measured real-model
improvement**. Both runs made zero provider requests. Tool-count changes are
reported as observations, not efficiency gains. No latency or statistical
performance improvement is claimed.

The automated matrix additionally verifies missing artifacts, one corrected
attempt after typed errors, 1/3 versus 3/3 versioned read coverage,
deterministic failure overriding semantic success, all five selection outcomes,
selective experience reuse, rejection, rollback, cancellation, invalid JSON,
corrupt/unsupported schema handling and recovery from an incomplete write.

## Validation results

All completed successfully:

- `go list ./...`: 44 packages.
- `go test -count=1 ./...`: full suite.
- `go vet ./...`.
- `gofmt -l .`: no output.
- `golangci-lint run ./...`: explicitly run, zero issues.
- `go test -race -count=1 ./internal/agent/... ./internal/agentverify/... ./internal/mcp/... ./internal/provider/... ./internal/tools/... ./internal/tui/...`.
- `go test -race -count=1 ./internal/harness`.
- `make check`: formatting, vet, installed linter and full race suite.
- Targeted harness/CLI/prompt/TUI opt-in tests; harness statement coverage 83.5%.
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ... ./cmd/llmtui`.
- `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ... ./cmd/llmtui`.
- `git diff --check`.

The sandbox initially blocked the default Go cache and an existing httptest
localhost listener. Validation used `GOCACHE=/tmp/llmtui-gocache` and the full
suite/race/vet/lint gates were rerun with the required test permissions. The
cross-builds succeeded; module stat-cache write warnings in their sandboxed
runs did not prevent binary generation. These are compile checks, not native
Linux/Windows execution tests.

The automated validation did not exercise a paid/live endpoint, real-model
proposal quality, embedded GGUF/native inference or native GBNF compilation.
The separate user-run embedded smoke test below failed correctness. Existing GGUF/MLX tests
remain environment-gated; a green ordinary suite does not validate them.
No Apple application or Xcode build was run or changed.

## User-run embedded smoke test

The user exercised the ordinary agent against an embedded GGUF using
`v1.0.54-dirty` (`main.commit=c1b5081`). The exact GGUF identity and final
inference/context settings were not supplied. Neither transcript establishes
that an experimental overlay was activated, so these runs are not a controlled
baseline-versus-candidate evaluation.

- The initial run stopped after reading TASK.md and two dataset windows. A
  subsequent sequential_thinking request was truncated by max_tokens and was
  not executed. The run reported zero of five criteria met. A larger context
  was used for the second attempt; the evidence does not establish that
  context size alone caused or resolved the first output-token truncation.
- The second run discovered dataset.txt, paginated its reads, wrote report.json
  and read it back. Its artifact was incorrect: sale_count was 5 and total_amount
  was 181, omitting the SALE amount=19 record at line 499. Independent inspection
  of the unchanged 620-line fixture confirms six sales at lines 20, 121, 244,
  499, 550 and 618, with amounts 17, 23, 41, 19, 29 and 71 (total 200).
  The source filename and marker sequence were correct.
- The second run explicitly reported `agent done · not verified`, with five
  criteria accepted unchecked because `agent.verifier.mode` was deterministic.
  The model's read-back and repeated correctness claim did not independently
  validate its extraction. Full read delivery is not proof of correct counting
  or arithmetic. This run is a correctness failure, not a verified success.

These observations confirm remaining real-model limits and motivate future
contained evaluations with independent output oracles. They do not measure an
improvement from the harness optimizer or validate model-driven proposals or
native proposal-grammar compilation. No runtime behavior was changed in
response to these transcripts in this implementation.

## Security boundary, deferred work and practical limits

Only reviewed catalog edits can reach production prompts. Free-form model
rationales never enter prompts or persisted experience. Actual permissions,
confinement, approval, MCP admission, deterministic criteria and semantic
verification remain authoritative. Ordinary startup opens no harness state.
State is atomic and bounded, with restrictive Unix modes and explicit locking.
Original task prompts, tool bodies, raw provider errors and reasoning are
excluded even when general prompt storage is enabled.

Default probes execute only trusted file-fixture scripts, without shell,
MCP, environment changes, personal data or external services. Separate
directories are not claimed to provide process isolation. Existing
`Runner.runCommandContext` runs host shells with a working directory and
sanitized environment; this does not establish complete filesystem/network
containment for arbitrary agents. The optimizer therefore does not expose it.
A proposer endpoint is contacted only with `--model-proposals`.

Deferred: contained live-agent/user-task replay, real-model efficacy,
arbitrary instruction generation, skill-selection edits, executable
scripts/plugins/subagents, broader domain retrieval and deeper/parallel
recursion. The concrete missing prerequisite is verified containment around
the existing executor. Simulation-only experience must be revalidated; it is
not authoritative task knowledge or globally promoted guidance.

A selected overlay stays fixed for a running session. Disable/revert affects
future opted-in sessions; restart without the flag to stop using it immediately.
A crashed writer's lock requires confirming no writer remains before manual
removal. Initial schema v1 rejects unknown versions rather than inventing a
migration; corrupted files must be backed up/moved explicitly. These limits
are documented in the operating guide.

## Exact file manifest

- [`CLAUDE.md`](../CLAUDE.md)
- [`docs/agent-loop.md`](../docs/agent-loop.md)
- [`docs/architecture/README.md`](../docs/architecture/README.md)
- [`docs/architecture/package-map.md`](../docs/architecture/package-map.md)
- [`docs/configuration.md`](../docs/configuration.md)
- [`docs/harness-experiments-validation.md`](../docs/harness-experiments-validation.md)
- [`docs/harness-experiments.md`](../docs/harness-experiments.md)
- [`docs/prompt-composition.md`](../docs/prompt-composition.md)
- [`internal/cli/chat.go`](../internal/cli/chat.go)
- [`internal/cli/harness.go`](../internal/cli/harness.go)
- [`internal/cli/harness_test.go`](../internal/cli/harness_test.go)
- [`internal/cli/root.go`](../internal/cli/root.go)
- [`internal/harness/doc.go`](../internal/harness/doc.go)
- [`internal/harness/experiment.go`](../internal/harness/experiment.go)
- [`internal/harness/fixture.go`](../internal/harness/fixture.go)
- [`internal/harness/harness_test.go`](../internal/harness/harness_test.go)
- [`internal/harness/proposal.go`](../internal/harness/proposal.go)
- [`internal/harness/snapshot.go`](../internal/harness/snapshot.go)
- [`internal/harness/store.go`](../internal/harness/store.go)
- [`internal/harness/store_test.go`](../internal/harness/store_test.go)
- [`internal/prompt/compose.go`](../internal/prompt/compose.go)
- [`internal/prompt/harness_test.go`](../internal/prompt/harness_test.go)
- [`internal/tui/app.go`](../internal/tui/app.go)
- [`internal/tui/harness.go`](../internal/tui/harness.go)
- [`internal/tui/harness_test.go`](../internal/tui/harness_test.go)
- [`internal/tui/pipeline.go`](../internal/tui/pipeline.go)
