# llmtui architecture

The single current-state reference for how llmtui is built. It consolidates
the material previously spread across the `v1-*` planning documents in this
directory; those are retained as a dated audit trail and are cited by code
comments, but this file is authoritative.

- **Package-by-package detail:** [`package-map.md`](package-map.md)
- **Accepted decisions:** [`decisions/`](decisions/) (ADRs)
- **Per-topic guides:** `docs/*.md` (agent loop, tools, prompt composition,
  context management, security, MCP, RAG, memory, skills, embedded inference,
  self-management, …)

Baseline: `master`, Go 1.27, ~36 internal packages. Verify version-sensitive
details (the embedded runtime pin, dependency versions) against the source —
`internal/runtime/pin.json`, `go.mod` — not this document.

---

## 1. What llmtui is, and the principles that shape it

A keyboard-first terminal UI and bounded agent runtime for **local** LLMs
(Ollama, LM Studio, vLLM, llama.cpp, any OpenAI-compatible server, or a GGUF
run in-process).

Every design choice traces back to a small set of principles:

- **Local-first.** No telemetry. No network call the user did not configure.
  Optional subsystems (tools, web, RAG, MCP, memory, `/agent`) are **off by
  default**; a broken or disabled one must never block normal chat startup.
- **User control is explicit and visible.** Mutating workspace actions, risky
  commands, web fetches, and external MCP tool calls are approval-gated unless
  the user chose auto mode. Approval in one context never carries to the next.
- **The raw user message is never rewritten.** Memory, RAG, skills, summaries
  and model hints are separate labelled prompt sections; the user's text goes
  in last, verbatim (`internal/prompt/compose.go`,
  [`../prompt-composition.md`](../prompt-composition.md)).
- **Layered, one-directional dependencies.** Provider code is independent of
  the TUI; config is independent of providers; execution guardrails are
  independent of UI rendering. Lower layers never import upward — several
  package docs encode the ban.
- **Determinism and small vertical changes.** Prefer a focused change that
  reuses the current package boundaries over a broad rewrite. Tests are
  stdlib-only, table-driven, deterministic (fakes and temp dirs, not sleeps).
- **Strong verification at trust boundaries.** Downloaded runtimes and
  binaries are size- and SHA-256-checked before use; untrusted text (provider,
  MCP, web, RAG output) is sanitised and structurally framed before it can
  reach the terminal or a prompt.

---

## 2. Repository and dependency shape

```text
cmd/llmtui ──> internal/cli ──> internal/app ──> internal/provider/{ollama,openai,embedded,mock}
                    │               │
                    └──> internal/tui ──────────> everything else
```

- `internal/tui` is the hub (~25 internal imports). Nothing imports it except
  `internal/cli`.
- `internal/provider` is the most-imported leaf.
- `internal/runtime` is **not** Go's `runtime` — it resolves, verifies and
  installs the llama.cpp shared libraries. Where both are needed, stdlib is
  aliased `goruntime`.

The seven layers, top to bottom:

| Layer | Packages | Job |
| --- | --- | --- |
| Entry / wiring | `cmd/llmtui`, `cli`, `app`, `config` | Cobra command tree; flag→env→YAML→default precedence; config → concrete provider factory |
| Providers | `provider`, `provider/{ollama,openai,embedded,mock}`, `provider/embedded/llamart` | The `Provider` contract and its implementations; `llamart` is the only package that touches native code |
| Runtime / self-management | `runtime`, `selfupdate` | llama.cpp library install/verify; llmtui binary self-update |
| Prompt / conversation / agent | `chat`, `prompt`, `contextmgr`, `modelprofile`, `agent`, `agentverify` | Compose requests, keep them inside the context window, and run bounded verified `/agent` cycles |
| Retrieval / memory / storage | `memory`, `rag`, `memoryindex`, `history`, `cache` | User preferences, workspace keyword index, the retrieval facade, session/usage/journal persistence, the response cache |
| Tools / safety | `tools`, `toolapi`, `web`, `mcp`, `skill`, `untrusted`, `terminaltext`, `terminalmath`, `clipboard`, `procutil` | The workspace tool engine and every guardrail around it |
| TUI | `tui`, `tui/components`, `tui/styles` | One Bubble Tea `Model` split by concern; most of the codebase's size lives here |

Full package descriptions, the "why do `skill` / `memory` / `runtime` appear
in several folders" cross-check, and the known size hot-spots are in
[`package-map.md`](package-map.md).

---

## 3. One orchestration kernel, three run modes

There is a **single** execution path to the model and tools
([ADR 0001](decisions/0001-single-orchestration-kernel.md)). Ordinary chat,
tool-enabled chat, and `/agent on` all drive the same
`dispatch` → `startRequest` → `handleStreamEvent` → `startToolBatch` →
`sendToolResults` primitives.

- **Ordinary chat** — one request/answer turn.
- **Tool-enabled chat** — the same turn plus an inline tool loop, bounded by
  `tools.max_iterations` per user message; when the budget is spent the *user*
  decides whether to grant more rounds.
- **`/agent on`** — a state machine (`internal/agent`) that wraps the same
  primitives into bounded, self-verifying *cycles*. It adds no second way to
  reach the model or execute a tool; any future mode that did would be a
  regression against ADR 0001.

`internal/tui/turn_runtime.go` (`turnRuntime`) is the explicit shared state
machine: it owns provider request generation, cancellation, the inactivity
watchdog, tool depth and retry flags, pending approval plans, tool-batch
generation/cancellation, live activity, and the progress ledger. `tui.Model`
is the Bubble Tea adapter — rendering, transcript mutation, and Chat/Agent
policy. Durable `/agent` acceptance state lives in `agent.AgentRun`.

### Shared state vocabulary

| State | Ordinary / tool chat | `/agent on` |
| --- | --- | --- |
| Idle | no request in flight | no active run |
| Contracting | — | fresh, tool-free request pins acceptance criteria before cycle 1 |
| Preparing | `prepareRequest` / prompt composition | same, plus the agent directive after the contract |
| Model streaming | `startRequest` | same |
| Waiting for approval | `pendingCalls` non-empty | same |
| Executing tools | `runToolPlan` | same |
| Processing results | `sendToolResults` | same |
| Verifying | — | `startAgentVerification` |
| Compacting context | `contextmgr.Decide/Split` | same |
| Waiting for user input | — (a denied approval just ends the batch) | `DecisionNeedsUserInput` |
| Completed | final answer rendered | `DecisionDone` |
| Cancelled | `Esc` / `Ctrl+C` | `/agent cancel` |
| Failed / budget exhausted | provider or tool error; `max_iterations` renewal declined | `DecisionFailed` / `DecisionBudgetExhausted` |

Ordinary chat deliberately has no `needs_user_input` or `parked` *terminal*
state — that is `/agent`-only, and adding it to ordinary chat would be scope
creep.

### The progress ledger and live budgets

Per [ADR 0002](decisions/0002-live-progress-ledger-and-budget-enforcement.md),
`internal/tui/progress.go`:

- **Repeated-call detection.** Each tool call is fingerprinted as
  `(tool_name, canonical_args, resource_key)` — normalised args, and a
  tool-specific resource key (normalised query, post-redirect URL, resolved
  workspace path, command + args). Evidence of progress (a changed result
  digest, an advanced cursor, a declared freshness policy, a retry after a
  transient failure) resets the counter. After `tools.no_progress.threshold`
  (default 3) repeats with no new evidence, the call is **not executed**; a
  tool-result-shaped block entry is fed back so the model sees it as normal
  flow. A second block after a strategy-change window terminates the run
  `no_progress` (a dedicated outcome, `agent.DecisionNoProgress`, distinct
  from `DecisionFailed`). Ledger lifetime is per-run and spans `/agent`
  cycles — it is a longer-lived counter than the per-cycle budget.
- **Live budget enforcement.** `startToolBatch` / `runToolPlan` short-circuit
  before executing a batch that would cross an already-known hard ceiling
  (`AgentRun.ToolCalls`, prompt+completion tokens, elapsed time), using
  run-level running totals rather than the per-cycle counter. `agent.Decide()`
  remains the sole authority for cycle-level `continue`/`retry`/`done`; the
  live check only reports `budget_exhausted` earlier. Both mechanisms have
  independent config toggles; `agent.enforce_budgets_live` can be disabled to
  reproduce the original defect.
- A truncated executor reply (cut off by `max_tokens` mid-tool-call) is
  rejected as success in both ordinary chat and `/agent` (`ErrorTruncated`).

---

## 4. The `/agent` verified-run pipeline

`internal/agent` is a provider- and UI-independent state machine;
`internal/agentverify` adapts a `provider.Provider` to the two fresh-context,
tool-free control requests it needs. See [`../agent-loop.md`](../agent-loop.md)
for the user-facing view.

```text
trigger → contract → (rules_load → executor → verifier → memory_write → stop_check)*  → terminal
```

1. **Contract** ([ADR 0003](decisions/0003-contract-before-agent-execution.md)).
   Before cycle 1, one bounded fresh-context request with no tool schemas
   decomposes the task into a small acceptance-criteria envelope, or asks the
   user for missing information. The result pins into `AgentRun.Criteria` with
   controller-assigned IDs; the original request stays immutable; contract
   content cannot grant tools, permissions, or instruction precedence. A
   `needs_user_input` contract surfaces as a clarifying question and
   re-establishes the contract with the answer; if the model's control
   envelope carries a valid question, any accidental provisional criteria are
   discarded rather than parking the run. A genuine parse failure gets one
   repair, then parks — and the bounded raw model output is recorded as a
   `contract_raw_output` event and shown in `/debug` so the park is
   diagnosable. Older persisted runs with no criteria take the contract stage
   on their next `/agent resume`.
2. **Executor.** The bounded objective runs through the shared kernel — the
   same tool approval, execution, result and continuation loop as ordinary
   chat. No agent-specific tool executor exists.
3. **Verifier.** A fresh two-message, tool-free request evaluates observable
   evidence against the pinned criteria. **Deterministic evidence decides
   first** (`agent.EvaluateDeterministic`): a failed test, a failed/denied
   trailing tool call, or a typed execution error is conclusive and clamps any
   optimistic semantic verdict (`ApplyDeterministicEvidence`). A tool failure
   the executor recovered from later in the same cycle is not counted — that
   exemption now also applies to what the semantic verifier sees
   (`agent.PruneRecoveredToolErrors`), so a normal recover-and-proceed
   sequence does not read as a failed cycle. Under the default `adaptive`
   policy the semantic model call is skipped when mechanical evidence already
   settles the cycle (never on cycle 1). `agent.verifier.mode: always`
   restores full rigour; `deterministic` and `off` are also available.
4. **Memory write.** One concise cycle entry — objective, execution summary,
   verdict, bounded per-call lines, changed files. Never raw prompts, tool
   output, or reasoning. A no-op write (a file rewritten with its own bytes)
   is not recorded as a change.
5. **Stop check.** `agent.Decide()` — pure function, no side effects — maps
   the cycle's execution + verification + run budgets to a terminal or
   `continue`/`retry` decision. Permission denial and `needs_user_input`
   outrank a verifier verdict; a retry with no changed objective, strategy,
   context, or new evidence is rejected.

Promotion of a verified outcome to project memory happens only after a
verifier-passed completion and an explicit user category selection (the picker
defaults to skip).

Run limits (`agent.max_cycles`, `max_tool_calls`, `max_tokens`,
`max_elapsed`, `max_repeated_failures`) are in `agent.Limits`; runs persist to
`agent.path` via `agent.NewFileStore` with on-disk redaction.

---

## 5. Providers and the capability model

`Provider.Chat(ctx, req)` returns a channel that emits `EventDelta` /
`EventReasoning` deltas and finishes with **exactly one** `EventDone` or
`EventError`, then closes; it must honour context cancellation. Providers
holding resources implement `Closer`; callers `CloseProvider` on switch and
exit. **Providers do not own timeouts** — no global `http.Client.Timeout`, so
a stream can take minutes. The connect timeout lives in
`internal/app/factory.go`; the inactivity watchdog lives in
`internal/tui/pipeline.go` (`startRequest`).

`internal/provider/capabilities.go` distinguishes transport-wide booleans
(`SupportsStreaming`, `SupportsTokenUsage`, `SupportsJSONMode`, …) from
model-dependent tri-state support (`NativeTools`, `ParallelToolCalls`,
`ReasoningEvents`, `StructuredOutput`): `unknown` / `unsupported` /
`supported`. Unknown is optimistic-but-recoverable — the TUI tries the native
path once and, on a `toolsRejectedError`, remembers the rejection for that
exact provider/model pair and falls back to the fenced prompt-based tool
protocol. Config can override any tri-state field per provider. `/doctor` and
`/debug` report the resolved values.

`internal/provider/model_protocol.go` centralises model-family detection
(GPT-OSS Harmony, Gemma, Qwen, GLM, …) so it is not scattered as substring
checks. GPT-OSS is a distinct native path end to end: Harmony control tokens,
`reasoning_effort`, and the GGUF's own Jinja template, never the generic tool
grammar or leaked-thinking filter.

---

## 6. Prompt composition and context management

`internal/prompt/compose.go` assembles provider-ready messages from
inspectable **sections** (system prompt, chat template, model hints, session
summary, memory, active skills, RAG context) with the user's raw message
appended last, verbatim. `Mode` is `minimal` / `balanced` / `coding`. Details:
[`../prompt-composition.md`](../prompt-composition.md).

`internal/contextmgr` keeps the conversation inside the model's window —
token estimation, truncation, summarisation — invoked from
`pipeline.prepareRequest`. `local_context` tool output (time, clipboard, env)
is treated as volatile: a provenance marker is kept, the payload is dropped.
Correlated tool results are not split across a compaction boundary. Details:
[`../context-management.md`](../context-management.md).

`internal/modelprofile` supplies per-family default tuning (context window,
temperature, prompt style, JSON-mode support) from built-ins plus config
overrides.

---

## 7. Tools and the safety layer

`internal/tools` is the workspace tool engine: a fenced-block protocol and a
matching set of native function specs (`native.go`), one capability catalog
with a `SafetyClass` per tool (`registry.go`), and the command classifier
(`guardrails.go` — auto / ask / deny). Tools available: `list_dir`,
`read_file` (ranged), `glob`, `grep` (shell-free), `write_file`, `edit_file`
(surgical exact-match), `run_command`, `web_search`, `web_fetch`,
`local_context`, `tool_search`, `ask_user`, `skill_load`. There is **no
delete tool**.

The **safety invariants** (each traces to a confirmed bug —
[`v1-security-review.md`](v1-security-review.md), [`v1-audit.md`](v1-audit.md),
and the "Workspace Tool Safety Invariants" in `CLAUDE.md`):

- Confinement is enforced on the **resolved, symlink-evaluated** path, never
  checked-and-discarded because the target does not exist yet.
- `run_command` confines filesystem access exactly like the file tools —
  `cmd.Dir` alone does not stop an allowlisted read command taking an absolute
  or `../` path argument.
- Command classification inspects enough of the line that a "read-only"
  verdict cannot come from a destructive subcommand or flag.
- `IsSecretPath` and friends classify the *logical* path — quoting and
  normalisation tricks must not defeat detection.
- A pending tool approval is always what the next keypress resolves; if any
  other input-owning UI state is open, the approval takes visible precedence.
- The response cache key covers everything that varies the request (history,
  the fully composed system prompt including tool instructions, RAG/memory
  context) and never an API key.

Around the engine: `internal/web` (DuckDuckGo search + readable-content fetch,
with `ssrf.go` blocking private / CGNAT / link-local / multicast ranges);
`internal/mcp` (Model Context Protocol — config, `Client`, server-state
registry, stdio subprocess transport with SIGTERM→SIGKILL reaping; **declaring
a server starts nothing**, only an explicit connect launches a subprocess);
`internal/skill` (discover `SKILL.md`, parse front matter, track per-run /
per-session activation — **activating a skill executes nothing**, and does not
authorise its tools); `internal/toolapi` (read-only HTTP mirror of the active
tool catalog for external inspection, never execution);
`internal/untrusted.Frame` (structural delimiters around model-readable data);
`internal/terminaltext.Sanitize` (strip C0/C1 + CSI/OSC/DCS before any
provider/MCP/web/RAG text reaches the renderer — one shared policy below the
TUI).

---

## 8. State ownership and storage

| Concern | Owner |
| --- | --- |
| Conversation / message state | `internal/chat.Session.Messages` — single instance shared by all modes |
| `/agent` run state | `agent.AgentRun` (Stage/Status/Cycle), held live in `tui.agentLoopState.run` |
| In-flight tool batch | `tui.Model.toolDepth`, `pendingCalls`, batch generation, activity — UI-owned even for `/agent` (ADR 0001) |
| Approval / permissions | `tui.Model.approvalPolicy` (session grants, per tool/path/content-hash, 15-min TTL) + `tools.Runner`/guardrails (path/command) + MCP per-server `approve` config — three disjoint concerns |
| Context budgeting | `internal/contextmgr`, invoked from `pipeline.prepareRequest` |
| Response cache | `internal/cache` — final-response only, keyed on request-shaping fields |
| Session persistence | `internal/history` — sessions, cumulative `usage.jsonl`, episode records, and an append-only operation journal for crash-recovery / idempotency of non-idempotent `run_command`s |
| Agent-run persistence | `agent.NewFileStore` (`agent.path`) |
| User preference memory | `internal/memory` — global user-authored YAML snippets, off by default, never auto-stored, never secrets |
| Typed project records | `internal/memoryindex.ProjectStore` — versioned, workspace-hashed, owner-only JSON; the current workspace is the isolation boundary |
| Retrieval | `internal/memoryindex.Retriever` — a facade fanning a query across `memory` + `rag` + agent-runs + project-facts, deduped by content hash; nothing it aggregates may import it back |
| Stale-event identity | per-generation counters: `streamGen`, `mcpBatchGen`, `agentLoop.verifyGen` |

Config and state are written `0o600`, dirs `0o755`, via temp-file + `Chmod` +
rename. Secrets never appear in logs, cache keys, command env, `--debug`, or
`config show`. There is **no logger** — diagnostics surface through the TUI,
`llmtui doctor`, and `--debug`.

---

## 9. Embedded local inference

Full design and history: [`embedded-local-inference.md`](embedded-local-inference.md)
and [`../embedded.md`](../embedded.md). In brief:

```text
embedded provider → llamart → yzma → dynamically loaded llama.cpp/ggml libraries → CPU / Metal / Vulkan / CUDA
```

- No application cgo and no `import "C"` anywhere; `llamart` binds llama.cpp
  through `purego`/`yzma`. macOS release builds set `CGO_ENABLED=1` only so
  Metal's native threads get Go's real cgo runtime; Linux and Windows stay at
  `0`. All native contact is confined to `internal/provider/embedded/llamart`
  so every other package builds and tests with no llama.cpp installed.
- The runtime (llama.cpp shared libraries + yzma) is pinned once in
  `internal/runtime/pin.json`. `internal/runtime` resolves it across tiers
  (explicit `library_path` → `YZMA_LIB` → executable-relative bundle →
  managed user-data `runtime/<tag>` → legacy dir), verifying every managed
  tier against embedded trusted digests. `llmtui runtime install` is the only
  network path and downloads an exact pinned URL, size- and SHA-256-checked
  before extraction. Self-contained release archives ship the trimmed runtime
  at `lib/llmtui/runtime`.
- Packaged acceleration: Metal (macOS arm64) and an optional pinned Vulkan
  pack (Linux/Windows amd64/arm64). NVIDIA CUDA on Linux is a manually
  validated, administrator-supplied `library_path` runtime —
  [`../embedded-cuda-linux.md`](../embedded-cuda-linux.md).
- `llmtui` exposes one GPU control, `gpu_layers`; multi-GPU uses llama.cpp's
  default split. Vision loads `libmtmd` lazily only when a projector is
  configured.

---

## 10. Self-update

`internal/selfupdate` manages the **llmtui binary**, distinct from
`internal/runtime` which manages the llama.cpp libraries. `llmtui self
check/update/install/path` — Cobra-free core so the transaction is
unit-testable. Updates come only from the official GitHub Releases of
`patrikcze/llmtui`, verified against the release `checksums.txt` before
extraction, extracted into a private staging directory with hardened
tar.gz/zip handling, and installed with a staged transactional replacement
that never destroys the working binary until the replacement is downloaded,
verified, extracted and validated. The binary and its bundled runtime are
replaced together. Full contract: [`../self-management.md`](../self-management.md).

---

## 11. Security model

[`../security.md`](../security.md) is the user-facing summary;
[`v1-security-review.md`](v1-security-review.md) is the v1-baseline delta over
the 2026-07-19 full review (all prior findings remediated). Highlights:

- The model never touches disk or network directly — it emits text and,
  optionally, structured tool calls; Go code decides whether and how to carry
  them out, through the approval/guardrail layer.
- Untrusted text (provider / MCP / web / RAG output) is sanitised and framed
  before it can reach the terminal or a prompt.
- Downloaded runtimes and binaries are digest-verified before use;
  administrator-supplied `library_path` runtimes are a documented trusted
  override that is *not* manifest-hashed.
- No unauthenticated model API is exposed beyond loopback by default.
- Budget exhaustion is treated as a resource-exhaustion risk, not just a UX
  concern — hence the live budget check.

---

## 12. Testing approach

- **Stdlib `testing` only** — no testify. Table-driven with named subtests.
  Deterministic: temp dirs and fakes, `httptest` via `internal/testutil`, not
  sleeps. Test files sit beside the code in the same package.
- **The scripted agent provider** (`internal/tui/agent_loop_test.go`) drives
  the full contract → executor → verifier → stop pipeline with no real model;
  `internal/tui/agent_action_class_test.go` labels those scenarios by action
  class (`TOOL_CALL` / `ASK` / `CONFIRM` / `REFUSE` / `OTHER`) for the
  diagnostics work in [`local-llm-evaluation-plan.md`](local-llm-evaluation-plan.md).
- **Opt-in, real-endpoint probes** are env-gated and skip cleanly:
  `internal/provider/embedded/llamart` integration tests
  (`LLMTUI_TEST_GGUF` / `YZMA_LIB` / `LLMTUI_TEST_CPU`);
  `internal/agentverify/contract_eval_test.go`
  (`LLMTUI_EVAL_BASE_URL` / `LLMTUI_EVAL_MODEL`).
- CI gates on `gofmt` (zero output), `go vet`, `golangci-lint` (0 issues),
  `govulncheck`, and a race-detector run over the agent / MCP / provider /
  tools / TUI subset. A green `go test ./...` does **not** exercise native
  inference or the release archive — both are CI-only paths.
- Historical coverage checklist: [`v1-test-matrix.md`](v1-test-matrix.md).

---

## 13. Where the design is recorded

### Accepted decisions (ADRs — read these first)

| ADR | Decision |
| --- | --- |
| [0001](decisions/0001-single-orchestration-kernel.md) | Retain the single orchestration kernel; no second execution path |
| [0002](decisions/0002-live-progress-ledger-and-budget-enforcement.md) | Live progress ledger + live budget enforcement |
| [0003](decisions/0003-contract-before-agent-execution.md) | Establish the task contract before agent execution |
| [`embedded-local-inference.md`](embedded-local-inference.md) | Embedded in-process inference via yzma/purego, no sidecar, no cgo — with dated addenda for verified runtime distribution, multimodal/tools, and each pinned-runtime bump |

### Supporting current-state references

- [`package-map.md`](package-map.md) — every internal package, its job, its files.
- [`v1-provider-capabilities.md`](v1-provider-capabilities.md) — the tri-state capability model in full.
- [`v1-state-and-storage.md`](v1-state-and-storage.md) — the source-of-truth and cache/persistence tables.
- [`v1-agent-runtime.md`](v1-agent-runtime.md) — the `turnRuntime` state machine, progress ledger and live budgets in detail (implemented; written forward-looking).
- [`../tool-call-diagnostics.md`](../tool-call-diagnostics.md) — native tool-call lifecycle observations, censoring diagnostics, and the harmless provider conformance probe.

### Historical audit trail (retained; cited by code comments)

These were the v1.0.0 stabilisation planning artifacts. Their designs shipped;
they are kept because code comments and tests cite specific sections as the
rationale for specific behaviour.

- [`v1-audit.md`](v1-audit.md) — the evidence-first architecture audit that identified the confirmed defects.
- [`v1-migration-plan.md`](v1-migration-plan.md) — the migration/rollback plan (implemented).
- [`v1-security-review.md`](v1-security-review.md) — security status at the v1 baseline.

### Active and resolved task plans

- [`local-llm-evaluation-plan.md`](local-llm-evaluation-plan.md) — **active.** Local-model action-choice diagnostics. Slices 1–2 shipped; Slice 3 (real-model matrix) in progress.
- [`agent-mode-reliability-fixes.md`](agent-mode-reliability-fixes.md) — **resolved** (PRs #59–#62, 2026-09-06): contract-clarification recovery, recovered-tool-error pruning, no-op-write accounting. Kept as the investigation record.
