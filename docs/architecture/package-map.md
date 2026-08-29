# Package map

A layer-by-layer tour of every Go package under `internal/`, plus a
cross-check for apparent duplication (the same noun — *skill*, *memory*,
*tool*, *runtime* — showing up in several packages).

Baseline: `master` @ `d2b3d80` (2026-08-28), ~35 packages / ~73k LOC.
Keep this in sync with the one-line list in the README's "Package layout"
section — this file is the long form of the same map.

## The dependency shape

```
cmd/llmtui ──> internal/cli ──> internal/app ──> internal/provider/{ollama,openai,embedded,mock}
                    │               │
                    └──> internal/tui ──────────> everything else
```

- `internal/tui` is the hub: it imports ~25 internal packages.
- `internal/provider` is the most-imported leaf (~58 references).
- Nothing imports `internal/tui` except `internal/cli`.
- Lower layers (`provider`, `config`, `history`, `tools`, `terminaltext`,
  `untrusted`) never import upward. The rule is enforced by package-doc
  contracts — e.g. `memoryindex` forbids `memory`/`rag` importing it back.

---

## 1. Entry point and wiring

| Package | Purpose | Files |
|---|---|---|
| `cmd/llmtui` (outside `internal`) | `main()` only. Holds the `-ldflags` version vars, calls `cli.NewRootCmd`. | `main.go` |
| `internal/cli` | The Cobra command tree: `chat`, `config`, `providers`, `models`, `runtime`, `doctor`, `history`, `stats`, `version`. `root.go` owns flag → viper → config precedence (only binds flags the user actually set). Each other file is one subcommand. | `root.go`, `chat.go`, `config.go`, `providers.go`, `models.go`, `runtime.go`, `doctor.go`, `history.go`, `stats.go`, `version.go` |
| `internal/app` | Config → concrete-provider factory. `factory.go` builds `provider.Provider` instances (embedded/ollama/openai/mock), HTTP clients, sampling defaults, `ActiveOverrides`. `skills.go` is two helpers translating config → `skill.Options` (shared by the TUI and `doctor` so they cannot drift). | `factory.go`, `skills.go` |
| `internal/config` | Viper-based loader. Precedence: flags > env (`LLMTUI_`) > YAML > defaults. One file with every config struct + validation + path resolution. | `config.go` |

---

## 2. Provider layer (`internal/provider/...`)

| Package | Purpose | Key files |
|---|---|---|
| `provider` | The abstraction everything else codes against: `Provider` interface, `ChatRequest`/`ChatEvent`, `ModelInfo`, tool specs. Plus cross-provider helpers: `capabilities.go` (supported / unknown / unsupported tri-state), `model_protocol.go` (`ModelFamily` — centralized model-name detection instead of scattered substring checks), `thinkfilter.go` (strip leaked `<think>` from content), `vision.go` (heuristic vision-model detection), `tools.go` (normalize JSON schemas for strict backends). | `provider.go`, `capabilities.go`, `model_protocol.go`, `thinkfilter.go`, `vision.go`, `tools.go` |
| `provider/ollama` | Native Ollama API (`/api/chat`), NDJSON streaming, tool calls. | `ollama.go` |
| `provider/openai` | Any OpenAI-compatible server (LM Studio, vLLM, llama.cpp, Unsloth). `stream.go` is the SSE parser including the `reasoning` / `reasoning_content` channels. | `openai.go`, `stream.go` |
| `provider/mock` | Offline demo provider so the TUI runs with no backend. | `mock.go` |
| `provider/embedded` | In-process llama.cpp provider — pure Go, zero cgo. The native engine sits behind the `Runtime` interface (`runtime.go`) so this layer builds and tests everywhere. `utf8.go` (rune-split reassembly), `stopscan.go` (stop-string detection across token boundaries), `options.go` (sampling config). | `embedded.go`, `runtime.go`, `options.go`, `utf8.go`, `stopscan.go` |
| `provider/embedded/llamart` | The one place native code lives: implements `embedded.Runtime` via `yzma`'s llama.cpp bindings (`purego`, no cgo). `harmony.go` (GPT-OSS Harmony protocol), `reasoning.go` (delimiter families), `tools.go` (native tool-call parsing), `vision.go` (mtmd image embeddings), `generate.go` (jinja chat templates), `logcapture.go` (surface native load failures), `recover.go` (FFI panic → error), `libraries.go` (shared-lib presence check). | `runtime.go`, `generate.go`, `tools.go`, `vision.go`, `harmony.go`, `reasoning.go`, `logcapture.go`, `libraries.go`, `recover.go` |
| `internal/runtime` (top-level, *not* Go's `runtime`) | Resolution / verification / install of the llama.cpp **shared libraries** for the embedded provider. Multi-tier lookup (config → env → exe-relative → user-data → legacy), hash verification against embedded trusted digests, `install.go` downloads and pins managed runtimes, `manifest.go`/`pin.go` track them. See also `docs/architecture/embedded-local-inference.md`. | `resolver.go`, `verify.go`, `install.go`, `manifest.go`, `pin.go`, `platform.go` |

---

## 3. Prompt / conversation / agent layer

| Package | Purpose | Files |
|---|---|---|
| `internal/chat` | Plain conversation state + usage stats. | `session.go` |
| `internal/prompt` | Builds provider-ready messages from inspectable **sections** (system, template, model hints, summary, memory, skills, RAG). Core rule: the raw user message is never rewritten. `Mode` = minimal / balanced / coding. See `docs/prompt-composition.md`. | `compose.go` |
| `internal/contextmgr` | Keeps the conversation inside the model's context window — token estimation, truncation, summarization. Special-cases `local_context` tool output as volatile (keeps a provenance marker, drops the payload). See `docs/context-management.md`. | `contextmgr.go` |
| `internal/modelprofile` | Per-model-family default tuning (context window, temperature, prompt style, JSON-mode support). Built-ins + config overrides. | `profile.go` |
| `internal/agent` | Provider- and UI-independent **state machine** for bounded, verified `/agent` runs: stages (trigger → executor → verifier → memory-write), `criteria.go` (acceptance criteria + evidence ledger), `deterministic.go` (infer mechanical criteria for narrow requests), `policy.go` (`Decide()` — budget/stop logic, no side effects), `store.go` (persist runs). See `docs/agent-loop.md` and `docs/architecture/v1-agent-runtime.md`. | `types.go`, `run.go`, `criteria.go`, `deterministic.go`, `policy.go`, `store.go`, `errors.go` |
| `internal/agentverify` | Adapts `provider` to a *fresh-context* verifier that grades an agent cycle against its criteria and returns structured JSON. Deliberately separate from `agent`, which stays provider-neutral. | `verifier.go` |

---

## 4. Retrieval and memory layer

| Package | Purpose | Files |
|---|---|---|
| `internal/memory` | Small user-curated preference snippets. Off by default, never auto-stores, must never hold secrets. See `docs/memory.md`. | `memory.go` |
| `internal/rag` | Optional local workspace index + keyword retrieval (BM25-lite, no embeddings, no vector DB). `index.go` (build), `store.go` (on-disk `index.json`), `context.go` (render snippets as a labeled reference block), `secrets.go` (skip the whole file on a credential match). See `docs/rag.md`. | `rag.go`, `index.go`, `store.go`, `context.go`, `secrets.go` |
| `internal/memoryindex` | The **aggregator / facade** above `memory` + `rag` + agent-runs + a project-fact store. `Retriever` fans a `Query` across pluggable `Source`s, dedups by content hash, sorts deterministically. `project_store.go` is its own persisted store of architecture / convention / decision facts. Powers `/memory search` and `/memory explain`. | `types.go`, `retriever.go`, `sources.go`, `user_source.go`, `rag_source.go`, `project_source.go`, `project_store.go` |
| `internal/history` | Session persistence + cumulative usage log (`usage.jsonl`) + episode records (which skills were active) + an append-only **operation journal** (`operations.go`) for crash-recovery / idempotency of non-idempotent `run_command`s. See `docs/architecture/v1-state-and-storage.md`. | `history.go`, `operations.go`, `usage.go` |
| `internal/cache` | File-based response cache keyed on *everything* that varies the request (history, composed system prompt, RAG/memory context) — never on API keys. Unrelated to the "memory" packages despite the theme. See `docs/cache.md`. | `cache.go` |

---

## 5. Tools and safety layer

| Package | Purpose | Notable files |
|---|---|---|
| `internal/tools` | The workspace tool engine. `tools.go` (fenced-block protocol + `Runner`, workspace confinement), `native.go` (same tools as OpenAI/Ollama function specs), `registry.go` (single capability catalog + `SafetyClass`), `guardrails.go` (the command classifier — auto vs ask vs deny), `file_edit.go` / ranged read (surgical `edit_file`, ranged `read_file`), `search.go` (shell-free glob/grep), `diff.go` (display-only write diffs), `local_context.go` (time, clipboard, env — volatile observations), `web.go` (thin wrapper over `internal/web`), `ask_user.go` (control-flow barrier tool), `tool_search.go` (the `tool_search` discovery tool + ranking). See `docs/tools-architecture.md` and the "Workspace Tool Safety Invariants" in `CLAUDE.md`. | `tools.go`, `guardrails.go`, `local_context.go`, `native.go`, `search.go` |
| `internal/toolapi` | Read-only HTTP server exposing the *active* tool catalog (name / description / safety / approval / schema) — for external inspection (e.g. an editor), not execution. See `docs/tool-registry.md`. | `server.go` |
| `internal/web` | The actual internet access: DuckDuckGo search (no key) + page fetch with readable-content extraction. `ssrf.go` blocks private / CGNAT / link-local / multicast / etc. ranges. | `web.go`, `fetch.go`, `search.go`, `ssrf.go` |
| `internal/mcp` | Model Context Protocol: config + `Client` interface + `registry.go` (server state tracking) + `stdio.go` (concrete subprocess transport, SIGTERM → SIGKILL reaping, frame-size bounds). Nothing starts a subprocess on its own. See `docs/mcp.md`. | `mcp.go`, `registry.go`, `stdio.go`, `mock.go` |
| `internal/skill` | Skills and Plugins engine: discover `SKILL.md` files, parse YAML front matter, validate, track per-run / per-session activation. Skills are instructions, not code — activating one executes nothing. `manager.go` is the stateful core; `plugin.go` / `discover.go` / `parse.go` support it. See `docs/skills.md`. | `skill.go`, `manager.go`, `discover.go`, `plugin.go`, `parse.go` |
| `internal/untrusted` | Deterministic structural delimiters wrapped around data a model may read but must not treat as instructions (used by `prompt`, `tools/web`, `mcp_tools`). | `frame.go` |
| `internal/terminaltext` | Strips C0/C1 + CSI/OSC/DCS escape sequences from provider / MCP / RAG / web text *before* it reaches the terminal renderer. One shared policy, deliberately below the TUI. | `sanitize.go` |
| `internal/terminalmath` | Display-only LaTeX-in-Markdown → terminal Unicode expansion (`ExpandMarkdown`), applied between `terminaltext.Sanitize` and Glamour in the TUI when `ui.math.enabled`. Wraps `github.com/doug/termtex`; guards Markdown tables against multi-row expansions. Opt-in, off by default. | `render.go` |
| `internal/clipboard` | Read images (and text) from the system clipboard via platform CLIs (`pbpaste`, `xclip`, `wl-paste`, PowerShell) — no cgo. | `clipboard.go`, `text.go`, `write.go` |
| `internal/procutil` | Subprocess-group management so wrapper commands cannot orphan grandchildren. Unix / Windows split. | `proc_unix.go`, `proc_windows.go` |

---

## 6. TUI layer (`internal/tui`)

One Bubble Tea `Model` split across files by concern. This is where most of
the size is (`app.go` ~3000 LOC, several siblings 1200–1800). See
`docs/tui-design.md`.

| File(s) | Concern |
|---|---|
| `app.go` | The `Model` struct, `Update`/`View`, key routing, message dispatch. |
| `pipeline.go` | Request assembly: cache key, prompt composition, RAG/memory injection, debug capture. |
| `turn_runtime.go`, `agent_loop.go`, `toolloop_*` | The turn state machine (idle → streaming → approval → tools → results), shared by ordinary chat and `/agent`. |
| `commands.go`, `commands_local.go`, `commands_memory.go`, `commands_skills.go` | Slash-command handlers, grouped by domain. See `docs/slash-commands.md`. |
| `skills.go`, `tool_search.go`, `tool_registry.go`, `mcp_tools.go` | Model-side wiring into the `skill` / `tools` / `toolapi` / `mcp` engines. |
| `approval_policy.go` | Narrowly-scoped temporary approval grants (per tool / path / content-hash, 15-minute TTL). |
| `activity.go`, `progress.go`, `context_status.go`, `usage.go`, `exitsummary.go` | Live status / diagnostics / usage rendering. `progress.go` is the repeated-call / no-progress ledger (ADR 0002). |
| `selection.go`, `keysmode.go`, `extkeys.go`, `ask_user.go`, `transcript_styles.go` | Input and rendering details: mouse text selection, `/keys` inspector, Shift+Enter decoding. |
| `composer_history.go` | Shell-like `↑`/`↓` recall of submitted prompts and slash commands. Bounded, session-local, in-memory, never persisted; arbitrates against pickers, suggestions, and multiline cursor navigation. |
| `tui/components` | Reusable widgets: `statusbar`, `barchart`, `sparkline`, `heatmap`, `usagepanel`, `status` (spinner), `button`. |
| `tui/styles` | The adaptive light / dark theme (`theme.go`). |

---

## 7. Shared test infrastructure

- `internal/testutil` — hermetic HTTP test helpers shared across packages (`http.go`).

---

## Cross-check: why the same nouns appear in many folders

There is repetition of *names* but no logic duplication. Each is a layered
pipeline where every layer has one job.

### `skill` — one engine, N thin adapters

| Location | Role |
|---|---|
| `internal/skill/` | The only engine: discovery, parsing, validation, activation state. |
| `internal/app/skills.go` | Two functions: config → `skill.Options`. Exists specifically to stop the TUI and `doctor` from each writing their own translation. |
| `internal/tui/skills.go` | `Model` methods: "is skill-loading available this turn?", render active skills for the prompt composer. |
| `internal/tui/commands_skills.go` | The `/skills` slash command + its pickers / overlays. |
| `internal/tools/*skill*` | The `skill_load` agent tool (model-driven activation) — rides the normal tool loop. |
| `internal/history` (imports `skill`) | Episode records store which skills were active. |

Engine → config adapter → prompt integration → UI → agent tool → audit trail.

### `memory` — five different things that share a word

| Package | What it actually is |
|---|---|
| `internal/memory` | User preference snippets (tiny flat store). |
| `internal/memoryindex` | Facade over `memory` + `rag` + agent-runs + project-facts for retrieval (`/memory search`). |
| `internal/rag` | Workspace file index (BM25-lite). |
| `internal/cache` | Response cache. Only the theme overlaps. |
| `internal/history` | Session / usage / journal persistence. |
| `internal/contextmgr` | Context-window trimming. |

The one that can confuse: `memoryindex` is an aggregator, not a peer store.
Its package doc spells out the layering rule (nothing it aggregates may
import it back).

### `tool_search` — engine vs. UI policy

- `internal/tools/tool_search.go` — the tool implementation + match ranking.
- `internal/tui/tool_search.go` — Model policy: the discovery threshold, when
  to hide tools from the catalog, what to disclose to the model.

### `runtime` / embedded inference — three packages, three reasons

| Package | Why separate |
|---|---|
| `internal/runtime` | Finds / verifies / installs the llama.cpp `.so` / `.dylib` / `.dll` files. Pure Go, no native deps. |
| `internal/provider/embedded` | The `provider.Provider` implementation. Pure Go — native engine hidden behind an interface so the rest builds and tests on any machine. |
| `internal/provider/embedded/llamart` | The only package that touches native code (via `yzma` / `purego`). Isolated so a machine without the libs still compiles everything else. |

Name clash with Go's stdlib `runtime` is handled by aliasing it `goruntime`
where both are needed (e.g. `llamart/vision.go`).

---

## Size hot-spots (not duplication, but worth knowing)

1. `internal/tui` carries most of the complexity: `app.go` (~3000),
   `commands_local.go` (~1800), `agent_loop.go` (~1200),
   `pipeline.go` (~1500). `app.go`'s `Update` switch is the natural next
   extraction target (delegate more to `turn_runtime` / `commands`).
2. `internal/tools/tools.go` (~1200) and `local_context.go` (~800) each mix
   protocol parsing, execution, and validation in one file.
3. `internal/config/config.go` (~1300) is one file for the whole config
   surface — a natural place to split structs from loading logic.

Everything else is appropriately sized and single-purpose. The package
boundaries and upward-import bans are documented in the package doc-comments
themselves.
