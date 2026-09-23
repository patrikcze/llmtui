# Next-generation tool runtime: Phase 8 calibration report

Recorded 2026-09-23 on `feat/next-gen-tool-runtime`.

This report is content-safe. It contains fixture identifiers, aggregate
counts, timings, and gate results only; it does not contain prompts, tool
arguments, file contents, model responses, credentials, or hidden reasoning.

## Measurement binding

| Field | Value |
| --- | --- |
| Baseline commit | `b6c57a75e85260895efe3a296d47d5ddc2603789` |
| Candidate under measurement | `63525ec78eb2f4b77526e94554f755a510c712c1` |
| Fixture manifest | `internal/tui/agent_action_class_test.go`, `internal/tui/agent_evolution_report_test.go`, `internal/tui/live_agent_eval_test.go`, `internal/eval/live.go`, `internal/eval/live_test.go` |
| Fixture archive SHA-256 | `57a983ff8be40cee81b0f8d88f89b01f99bb56353319c98ccfab7d410572a39b` |
| Host | macOS, darwin/arm64, Apple M5, Go 1.27.1 |

The opt-in JSONL evaluators accept `LLMTUI_EVAL_BASELINE_SHA`,
`LLMTUI_EVAL_CANDIDATE_SHA`, and `LLMTUI_EVAL_FIXTURE_HASH`; these values are
copied into every record so a future local-model run cannot lose its A/B
binding. The fields are optional for existing synthetic callers.

## Deterministic evidence

`TestAgentEvolutionSyntheticMatrix` passed with 10 scenarios, 10 correct
outcomes, zero false-success outcomes, and 8 executed fixture calls. The
focused evaluation/report and tool safety suites passed. The complete gate
results are recorded below after the final documentation commit.

The deterministic fixtures cover the negative approval, missing-path,
resource/version, cancellation, bounded-read/search, freshness, MCP, and disk
retention paths. Their result is controller behavior evidence; it is not a
claim about model reasoning quality.

## Live model matrix

No endpoint or model was configured in this run. Both opt-in matrices were
therefore skipped with their required explicit configuration message:

* `internal/eval/TestLiveEvaluationMatrix` (contract and native/fenced
  conformance)
* `internal/tui/TestLiveAgentMatrix` (read, ask, and confirmed-write loop)

There are no live token, call, completion, false-absence, stale-edit, or
failed/censored trial denominators to compare. Hosted comparison was not run.
This is an intentional censored result, not a success or a non-regression
claim. Native embedded-model coverage is also open because no GGUF/runtime
fixture was configured.

## Defaults and compatibility decisions

The evidence does not justify changing defaults. `tools.read.default_lines`
remains 200, the hard read limit remains 500 lines, and
`tools.max_file_kb` remains 512 KiB. No accepted file or network write cap was
increased. The memory body store remains the default; disk backing is explicit
through `entities.output_storage: disk`.

The session-bound resource adapter remains an intentional typed seam between
the TUI and entity registry. The combined fenced personal-app form remains in
place as the documented external compatibility window. Neither is obsolete
until every external producer is typed and the compatibility period is closed.

## Benchmark snapshot

The bounded benchmark run reported these representative values (ns/op,
bytes/op, allocations/op):

* small/ranged/resource reads: 35,221/11,552/123; 285,165/45,231/135;
  259,760/196,865/7
* small write/edit and versioned edit: 42,988/29,734/161;
  7,450,031/50,434/316; 8,275,196/33,871/387
* Go search small/large and optional rg: 469,315/1,174,009/1,493;
  19,714,932/111,332,880/131,025; 5,382,330/11,952/30
* cached web/search capture: 1,439/3,369/34 and 1,754/3,612/47
* memory/disk body open/read: 849.8/112/2 and 13,739/42,786/11
* small web fetch: 32,787/10,407/106

These values are a local calibration snapshot, not a cross-machine promise.

## Final gate evidence

The final tree passed `go test -count=1 ./...`, `go vet ./...`, `make build`,
`golangci-lint run ./...`, `govulncheck ./...`, the required package race
subset, and the exact `make check` target (including full race coverage).
Linux and Windows `internal/entity` compile checks also passed. The localhost
backed web tests and benchmark were run with the repository's approved
escalation because the sandbox cannot bind test listeners.

Windows ACL execution and embedded native-model hardware remain platform/model
gates on this macOS host. A future model-configured run should retain this
report's baseline and fixture hash, add at least five trials per model/protocol
setting, and may then revise defaults only if safety and local-model evidence
remain clean.
