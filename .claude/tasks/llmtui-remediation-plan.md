# LLMTUI remediation and simplification plan

This is an implementation plan, not an authorization to change production code. Preserve backward compatibility where possible and land each phase independently.

## Implementation status — 2026-08-23

Status is recorded against local Git history. The completed P0, P1, P2, and P3 work listed below is merged into local `master`. The separate P2 `TurnRuntime` extraction remains open.

### Completed

- [x] P0 — Restore the tool approval boundary (`a61ad10`).
- [x] P0 — Separate verifier availability from executor retry (`c0f50fa`, `e81904d`).
- [x] P0 — Enforce the verifier control contract locally (`358b3fa`).
- [x] P1 — Make run budget admission prospective (`78133fd`).
- [x] P1 — Preserve a run-start context snapshot (`7f6230d`).
- [x] P1 — Harden provider channel cancellation (`ddc3374`).
- [x] P1 — Bound file reads at I/O (`38ad9ca`).
- [x] P2 — Strengthen no-progress operation identity (`3ad4c32`).
- [x] P2 — Make tests hermetic and uncached in critical CI jobs (`23f36f2`).
- [x] P2 — Simplify Agent prompts and deterministic acceptance (`6d4cb73`, `6fd3314`). The verifier now emits an eight-field required contract, accepts legacy 16-field responses, keeps `user_options` and criterion notes optional, preserves the establishing-cycle safety invariant, and has prompt-budget, deterministic-only, mixed-criteria, user-input, and weak-model regression coverage.
- [x] P3 — Defense-in-depth hardening (`519c82f`). This includes descriptor-relative file operations, archive extraction limits and duplicate rejection, UTF-8-safe truncation, continuously bounded/redacted MCP stderr, and security-sensitive cleanup/close error handling.

### Remaining

- [ ] P2 — Extract an explicit turn execution runtime. No implementation has started. The complete goal, conceptual changes, and required state-machine tests remain as specified below.
- [ ] Run final repository-wide verification after P2 is complete: fresh `go test -count=1 ./...`, `go vet ./...`, available `govulncheck`, and targeted race tests.
- [ ] Review the final combined diff after `TurnRuntime` implementation and final verification.

### Verification notes

- The completed Agent prompt/verifier work passed fresh targeted tests for `internal/agent`, `internal/agentverify`, and `internal/tui`.
- The hermetic-test, no-progress, and P3 groups passed their targeted package and race tests when implemented.
- A final fresh repository-wide test/vet/vulnerability run has not yet been performed on the combined local `master`; completion of individual groups must not be interpreted as final acceptance.

## P0 — Restore the tool approval boundary

### Goal

Ensure every auto-approved command is provably non-mutating and does not execute repository-controlled code.

### Why

`go test`, `go fmt`, and unrestricted `go env` currently bypass default confirmation despite executing code or mutating files/config. This contradicts the documented security boundary.

### Affected components

- `internal/tools/guardrails.go`
- `internal/tools/tools.go`
- command classifier/approval tests
- `docs/security.md`, README, `/tools check` descriptions

### Exact conceptual changes

1. Remove `go test`, `go fmt`, and unrestricted `go env` from automatic approval.
2. Treat `go test`/`vet` as code execution and require approval. If a later sandboxed test capability exists, expose it as a separate typed tool.
3. Permit only genuinely observational `go env` forms after complete flag validation; reject `-w`, `-u`, response files, and unknown forms.
4. Audit each allowlisted program/subcommand for writing/exec flags (`git --output`, `find -fls`, etc.); default unknown forms to ask.
5. Keep the exact-command temporary grant so common approved test loops remain usable.

### Tests required

- table-driven classifier tests for every allowlisted program and mutating flag;
- marker-file fixture proving repository test code never executes without approval;
- `go fmt` and `go env -w/-u` must ask;
- property/fuzz test: inserting an unknown flag cannot change ask to auto;
- cross-platform quoting/path cases.

### Security considerations

Authorization is based on semantic capability, not labels like “check.” Do not rely on `cmd.Dir` as sandboxing. Sanitized environment remains defense in depth, not confinement.

### Compatibility considerations

Users will see more prompts for test/vet/fmt. Explain the change in release notes and keep narrow 15-minute exact-operation grants.

### Migration concerns

No persisted schema change. Existing `tools.approve: auto` remains an explicit trusted override.

### Risk

Medium usability risk; low implementation risk.

### Definition of Done

No command capable of executing repository code or mutating workspace/user configuration receives `VerdictAuto`; docs and tests state the same boundary.

## P0 — Separate verifier availability from executor retry

### Goal

Never repeat executor work because the verifier provider, timeout, or output format failed.

### Why

This is the principal Agent cost/reliability defect and can repeat side effects under fresh tool-call IDs.

### Affected components

- `internal/agent/types.go`, `run.go`, `policy.go`, persistence schema
- `internal/agentverify/verifier.go`
- `internal/tui/agent_loop.go`
- Agent status/debug/UI/docs/tests

### Exact conceptual changes

1. Add a verifier-attempt controller outcome distinct from `VerificationResult`.
2. Keep format repair and transient verifier retries inside the same executor cycle with a small attempt/token/time budget.
3. On verifier exhaustion, preserve the completed `ExecutionResult` and stop/park as `verification_unavailable` (or an explicit compatible representation).
4. Offer user actions: accept unverified result, retry verifier, choose verifier model, or resume executor only by explicit choice.
5. Only a valid verifier verdict that identifies unresolved criteria or a failed executor result may create a new executor cycle.
6. Account every verifier attempt without changing executor cycle/failure fingerprints.

### Tests required

- successful executor + verifier timeout: one executor request;
- two malformed verifier replies: one executor request, bounded verifier requests;
- cancellation while verifying: no executor retry;
- persistence/resume of unavailable verification;
- regenerated tool-call IDs cannot repeat the first cycle's effect;
- exact usage/cycle/failure counters.

### Security considerations

Never infer executor authorization from verifier retry. Preserve operation-journal ambiguity rules. Accepting unverified output must be explicit and visibly labelled.

### Compatibility considerations

Old persisted runs need a schema migration or interpretation of existing inconclusive failures. Default behavior should stop safely rather than replay.

### Migration concerns

Bump Agent schema version if adding a status/attempt state. Add read compatibility and migration fixtures for current snapshots.

### Risk

Medium/high because stop/resume/UI/persistence intersect.

### Definition of Done

Every verifier-infrastructure failure path is proven incapable of scheduling `startNextAgentCycle` without a valid executor-related retry decision.

## P0 — Enforce the verifier control contract locally

### Goal

Make completion independent of backend schema compliance and weak-model omissions.

### Why

Sparse JSON currently zero-fills required fields and can pass cycle one without criteria.

### Affected components

- `internal/agentverify/verifier.go`
- verifier wire types and tests
- Agent criteria establishment policy

### Exact conceptual changes

1. Decode a dedicated wire struct using pointers/presence flags.
2. Use `json.Decoder.DisallowUnknownFields`, require EOF, and validate one object.
3. Require every protocol field or deliberately reduce the protocol to a smaller required schema.
4. Add cycle-aware validation: an establishing pass must produce valid criteria and status updates, or an explicit controller-recognized atomic-task form.
5. Validate lengths/counts before mapping to durable `VerificationResult`.
6. Keep one format-repair attempt with an error message naming missing/invalid fields.

### Tests required

- omit each required field individually;
- unknown fields, duplicate objects, wrong types, nulls, non-finite/invalid confidence;
- passed first cycle with no criteria must fail validation;
- strict and non-structured provider paths behave identically;
- corpus tests from representative weak local-model outputs.

### Security considerations

Control data is untrusted even when generated under grammar/schema constraints. Never let model zero values become controller authority implicitly.

### Compatibility considerations

Weak models may fail more visibly. Counter this by shrinking the schema and improving deterministic verification, not by accepting ambiguity.

### Migration concerns

No stored schema change if mapped result remains stable.

### Risk

Medium.

### Definition of Done

Application validation alone guarantees every invariant consumed by `CompleteVerification` and `Decide`.

## P1 — Make run budget admission prospective

### Goal

Prevent executor, continuation, and verifier requests that cannot fit the remaining run token/elapsed budget.

### Why

The current “hard” token limit can overshoot by complete requests.

### Affected components

- request preparation/estimation in `internal/tui/pipeline.go`
- Agent budget policy/state
- verifier request construction
- debug/usage UI

### Exact conceptual changes

1. Introduce a shared `RunAdmission` check for every model request type.
2. Reserve estimated prompt plus configured maximum completion, with an explicit conservative/optimistic policy.
3. Use actual provider usage when returned; retain estimates otherwise.
4. Give verifier attempts a separate sub-budget inside the run total.
5. Stop with a precise reason before dispatch when insufficient.

### Tests required

- budget edge before first request, continuation, verifier, and format repair;
- actual usage less/greater than estimate;
- unknown usage fallback;
- elapsed deadline racing admission;
- no provider call after rejection.

### Security considerations

Budgets are DoS controls. Integer overflow and negative usage must remain clamped.

### Compatibility considerations

Conservative reservation can stop tasks sooner; expose estimates in `/agent status` and allow explicit configuration.

### Migration concerns

Clarify semantics in docs; no mandatory data migration.

### Risk

Medium.

### Definition of Done

Tests prove no request begins when its declared reserve would exceed the configured run ceiling.

## P1 — Preserve a run-start context snapshot

### Goal

Make Agent cycle one at least as context-aware as Chat while keeping later cycles bounded.

### Why

The existing session summary is dropped, breaking follow-up meaning after compression.

### Affected components

- `internal/tui/pipeline.go` `requestHistory`
- Agent start/persistence state
- prompt composition and context tests

### Exact conceptual changes

1. Snapshot the session summary and selected prior final conversational turns at run start.
2. Include the framed snapshot in cycle one.
3. Use pinned criteria/evidence/memory for later cycles; do not reintroduce old tool protocol.
4. Persist enough provenance for resume without persisting secrets/raw tool output.

### Tests required

- fact available only in session summary survives cycle one;
- old tool results/controller turns remain excluded;
- cycle two does not regain unrelated conversation;
- resume produces equivalent bounded context;
- context-budget fitting and compression remain valid.

### Security considerations

Prior summary is untrusted conversational data, not controller authority. Apply persistence redaction/bounds.

### Compatibility considerations

Cycle-one prompt grows modestly and changes outputs; Agent cache is already bypassed.

### Migration concerns

If persisted, bump Agent schema with optional field and backward-compatible empty default.

### Risk

Low/medium.

### Definition of Done

Characterization tests show every first-cycle Chat-relevant summary fact remains available to Agent without old protocol noise.

## P1 — Harden provider channel cancellation

### Goal

Make cancellation reclaim orchestration even when a provider adapter violates channel closure expectations.

### Why

The first-event receive and drain goroutine can block indefinitely.

### Affected components

- `internal/tui/pipeline.go` `startRequest`
- `internal/tui/app.go` `drainStream`
- provider interface contract and adapter tests

### Exact conceptual changes

1. Select on first event versus request context.
2. Bound drain by a context/deadline; abandon consumer after the bound.
3. Specify provider obligation: producer must stop and close on context cancellation.
4. Add adapter contract tests for pre-header, pre-first-event, mid-stream, and abandoned-consumer cancellation.

### Tests required

- never-emitting channel;
- never-closing channel after cancellation;
- stale first message after new generation;
- repeated cancel/retry with goroutine leak detector.

### Security considerations

This is a DoS/resource-leak boundary; do not wait synchronously in the UI update path.

### Compatibility considerations

None for compliant providers.

### Migration concerns

None.

### Risk

Low.

### Definition of Done

Cancellation completes within a bounded time for both compliant and intentionally broken mock providers, with no state adoption or persistent goroutine growth.

## P1 — Bound file reads at I/O

### Goal

Honor `max_file_kb` without allocating the full file.

### Why

Current `os.ReadFile` permits workspace-file memory exhaustion.

### Affected components

- `internal/tools/tools.go` `readFile`
- file-tool tests/security docs

### Exact conceptual changes

Open the file, reject inappropriate non-regular types, read `limit+1`, truncate at a valid UTF-8/display boundary, and use stat size for the notice.

### Tests required

- sparse/large file bounded read;
- exactly-at-limit and limit+1;
- invalid UTF-8 truncation;
- FIFO/device handling per platform;
- cancellation if the API is made context-aware.

### Security considerations

Keep symlink confinement; consider descriptor-relative `openat`/no-follow in a later hardening phase.

### Compatibility considerations

Output text remains equivalent except safer truncation.

### Migration concerns

None.

### Risk

Low.

### Definition of Done

Memory and bytes read are O(configured cap), independent of file size.

## P2 — Extract an explicit turn execution runtime

### Goal

Clarify state ownership without rewriting the working shared kernel.

### Why

Provider/tool/approval/cancellation state is spread across a 2,900-line UI model and adjacent files.

### Affected components

- `internal/tui/app.go`, `pipeline.go`, `mcp_tools.go`, `progress.go`
- potentially a new internal execution package

### Exact conceptual changes

1. Characterize existing behavior first.
2. Extract one `TurnRuntime` state object: request generation, stream/cancel/watchdog, retry flags, tool plan/depth, activity, batch generation/cancel, progress ledger.
3. Define typed engine outcomes: final answer, needs approval, tool continuation, execution failure, cancelled.
4. Keep Bubble Tea commands/messages as an adapter.
5. Let Chat and Agent consume outcomes through small policies; do not duplicate provider/tool code.

### Tests required

- state-machine transition table;
- stale event permutations;
- at most one active request/batch;
- exact tool-call/result correlation;
- cancellation/terminal cleanup invariants;
- golden characterization of current Chat flows.

### Security considerations

Approval and operation journal stay inside the engine boundary; policy cannot bypass them.

### Compatibility considerations

No intentional visible change. Preserve provider/fenced protocol quirks.

### Migration concerns

Land as small moves with tests, not a branch-long rewrite.

### Risk

High if broad; medium if incremental.

### Definition of Done

UI state no longer directly coordinates provider and tool lifecycles, and both modes still use one engine.

## P2 — Simplify Agent prompts and deterministic acceptance

### Goal

Reduce the cognitive/JSON burden on local models and semantic verifier calls.

### Why

Controller metadata and a large verifier contract make small-model performance format-dependent.

### Affected components

- `agentDirective`
- verifier prompt/schema
- criteria/evidence model
- provider capability profiles

### Exact conceptual changes

1. Keep run IDs/stages/failure fingerprints in UI/debug, not executor instructions.
2. Executor receives original goal, current unresolved criterion(s), constraints, and compact relevant evidence only.
3. Model explicit deterministic criterion types where tools can prove them (command exit, file state/hash, test result).
4. Invoke semantic verifier only for unresolved semantic criteria.
5. Route user-dependent criteria directly to the user.
6. Shrink verifier response to the minimum fields the controller actually needs.

### Tests required

- prompt token snapshots by model class;
- deterministic task completes without semantic verifier;
- mixed deterministic/semantic task verifies only unresolved semantic part;
- user-dependent outcome stops once;
- weak-model JSON corpus.

### Security considerations

Deterministic evidence must come from runtime observations, never model claims. Prompt framing remains defense in depth.

### Compatibility considerations

Outputs may change. Keep verifier modes as compatibility flags through a deprecation window.

### Migration concerns

Map existing criteria to generic semantic criteria; new typed criteria optional.

### Risk

Medium.

### Definition of Done

Common mechanical tasks require no verifier inference, and semantic verifier prompts are materially smaller while preserving acceptance integrity.

## P2 — Strengthen no-progress operation identity

### Goal

Recognize semantically equivalent repeated calls while preserving legitimate freshness/polling.

### Why

Equivalent path spellings and harmless command variations evade current fingerprints.

### Affected components

- `internal/tui/progress.go`
- tools call parsing/resolution
- progress tests/docs

### Exact conceptual changes

1. Use cleaned/resolved workspace-relative identities for file tools.
2. Define typed identities per tool rather than raw argument strings.
3. For commands, conservatively parse supported auto/read operations; leave opaque approved commands under exact identity plus hard budgets.
4. Model volatile calls with explicit freshness/poll tokens or TTL, not accidental argument variation.

### Tests required

All cases requested by the audit: same/changed result, path aliases, URL normalization, pagination, polling, transient failure, corrected input, writes, commands, search, MCP canonical JSON, and mixed batches.

### Security considerations

Never transform one approved target into another. Approval identity and progress identity may share normalization but have different purposes.

### Compatibility considerations

False positives are the main risk; keep an observable block reason and configurable threshold.

### Migration concerns

Ledger is process-local, so none.

### Risk

Medium.

### Definition of Done

Equivalent resource operations converge, changed observations reset correctly, and explicit polling remains possible.

## P2 — Make tests hermetic and uncached in critical CI jobs

### Goal

Ensure results do not depend on user config, available ports, or stale Go cache.

### Why

The doctor test failed due real user skills/plugins; normal cached tests masked fresh execution in some provider packages.

### Affected components

- CLI/skill tests
- CI configuration
- provider HTTP tests

### Exact conceptual changes

Inject platform paths, isolate XDG/user config with `t.Setenv`, and provide listener seams/fallback to IPv4 for restricted test environments. Add a critical `-count=1` job and targeted race job.

### Tests required

Run under an environment containing unrelated skills/plugins; run with IPv6 unavailable; run twice with distinct config roots.

### Security considerations

Security tests should fail closed if their fixture isolation is missing.

### Compatibility considerations

Test-only.

### Migration concerns

None.

### Risk

Low.

### Definition of Done

Fresh `go test ./...` has the same result on a clean CI runner and a developer machine with populated LLMTUI configuration.

## P3 — Defense-in-depth hardening

### Goal

Reduce residual local filesystem/runtime risks after blockers are fixed.

### Why

Current protections are strong but path check/use races and extraction resource limits remain.

### Affected components

- file tools
- runtime installer
- MCP result truncation
- terminal/display truncation helpers

### Exact conceptual changes

- evaluate descriptor-relative/no-follow file I/O per supported OS;
- add per-entry decompressed-size limits before runtime archive writes;
- make every byte truncation UTF-8 safe;
- cap/redact MCP stderr continuously rather than only at display;
- resolve existing linter errcheck findings on security-sensitive cleanup/close paths.

### Tests required

Symlink-swap adversarial tests, archive bombs/duplicate entries, multibyte truncation, large stderr, and cleanup failure injection.

### Security considerations

Avoid portability regressions and do not claim process/filesystem sandboxing unless actually enforced by the OS.

### Compatibility considerations

Platform-specific implementation required.

### Migration concerns

None.

### Risk

Medium.

### Definition of Done

Security-critical I/O operates on validated handles where practical, all resource limits apply before allocation/write, and relevant linter errors are resolved.
