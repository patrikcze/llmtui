# llmtui Next-Generation Tool Runtime

Architecture investigation and implementation plan. Originally published with **no runtime implementation included**; implementation began 2026-09-22 (see Implementation status below) — the rest of this document is still the planning artifact as originally written and should be read as such, with the status section as the one place tracking what has actually landed.

Reviewed 2026-09-22. Local `master`: `b6c57a75e85260895efe3a296d47d5ddc2603789`. OMP upstream `main`, fetched for this investigation: `0f9139f546e1dcb0e63ce35f1236509c4cca66c1` (commit timestamp `2026-09-22T15:50:21+02:00`). Recheck these revisions before implementation; do not interpret older plans as unimplemented requirements.

**Reading convention:** `CURRENT:` is observed llmtui behavior; `REFERENCE:` is observed OMP behavior; `PROPOSED:` is a decision for future implementation. In proposed sections, code, defaults, schemas, tests, and checklists are specifications, not claims about existing behavior. `KEEP`, `REFINE`, `EXTEND`, `REPLACE`, and `DEFER` classify changes; priority is independent of classification.

## Implementation status

Source of truth for exact commits/dispatch history: `.superpowers/sdd/next-generation-tool-runtime/progress.md` (gitignored SDD ledger). This section is a human-readable summary kept in sync with it; if the two disagree, trust the ledger and git log, then fix this section.

**Scope update for the 2026-09-23 implementation session:** Phases 0-7 and the deterministic portion of Phase 8 are implemented on `feat/next-gen-tool-runtime` as reviewable commit sets. Phase 8 records the absence of a configured local model as a censored/open gate; no default is promoted on missing evidence. Windows ACL execution and embedded native-model hardware remain platform/model gates on this macOS host. The rows below and the SDD ledger are the status sources; the original proposed sections remain the architectural specification.

| Phase | Status | Commits (`feat/next-gen-tool-runtime`) | Notes |
| --- | --- | --- | --- |
| 0 — characterization | **Done**, reviewed clean | `4262bbb`, `aa94430` | 7 fixtures + 2 benchmarks documenting current gaps (§30 table); additive `internal/eval` report fields. Independent task-reviewer subagent review: Approved, 0 Critical/Important. |
| 1a — typed result metadata | **Done** | `4122d0f`..`0dddb03` (10 commits) | `tools.ResultMeta`/`Outcome`/`ErrorInfo`/`Coverage`/`Window` (§13, `internal/tools/result.go`); populated across all §6 inventory producers including personal-apps domain-status mapping; shared Meta-blind formatter (byte-identical output, proven by snapshot test); `progressDigest` now keys off typed content, not formatted text; nine-states-observable Done-criterion test. `ResourceID`/`Observation` fields deliberately deferred to Phase 2b (no producer for them yet). **Not independently task-reviewed** — see caveat below. |
| 1b — confined atomic writes | **Done** | `d1d28cb`..`4827775` (4 commits) | `write_file`/`edit_file`'s shared publish primitive (`internal/tools/file_write.go`) now stages content in a confined, owner-only sibling and publishes with `(*os.Root).Rename` instead of `O_TRUNC`; new write-only symlink/hard-link admission checks (`symlink_write_unsupported`); full §33 fault-injection coverage; platform split (`file_replace_{unix,windows}.go`) verified against Go 1.27 stdlib source (Windows not executed, this env is macOS). Two doc passages overclaiming "never clobbered" corrected. Controller review caught and fixed a real issue: two intermediate commits didn't compile standalone (test seam landed one commit late) — history rebased so every commit in the range now builds/vets/tests individually. Known plan-mandated cost: ~80-110x write latency increase on small files from the two required fsync calls (durability guarantee, not a bug) — flagged for Phase 8 calibration. **Not independently task-reviewed** (same spend-limit constraint as 1a) — controller did a full manual read of the core logic instead. |
| 2a — bounded command capture | **Done** | `046dd14`..`a7f6af1` (4 commits) | `internal/tools/output_capture.go` replaces `run_command`'s unbounded `bytes.Buffer` with a bounded writer (retention capped at `r.maxKB*1024` regardless of output size) plus a full-stream SHA-256 digest into `ResultMeta.ContentDigest`. Headline number: 256 MiB benchmark case went from 1,342,278,229 B/op (old) to 403,338 B/op (new). Controller review found the same lint-hygiene issue as 1b (intermediate commits not fully gate-clean standalone) and fixed it the same way (rebase/squash). **Not independently task-reviewed** (spend-limit constraint) — controller did a full manual read instead. |
| 2b — entity bodies + shared finalization | **In progress** (split into sub-parts — see below) | — | |
| 2b-i — entity body substrate | **Done** | `5fda84a`..`dcb3a1f` (4 commits) | New `internal/entity` files (`resource.go`, `body_registry.go`, `body_memory.go`, `id_random.go`): `ResourceMetadata`/`FileVersion`/`BodyLease`/`ResourceView` types, `Registry.Publish`/`OpenBody` (random 128-bit base32 IDs alongside the existing legacy sequential `ent_00001` IDs, disjoint by construction), a memory-only body backend, a separate byte budget/quota/eviction path from the existing small-entity payload accounting, and `tool_output`/`search_result` kinds. Implementer added (unprompted) a `bodyGeneration` counter guarding `Publish` against a concurrent `Reset()` — controller hand-traced this and confirmed it's correct, matching plan §33's "session reset while body publication finishes" row. Self-contained, no `internal/tools`/`internal/tui` changes. **Not independently task-reviewed** — thorough controller read instead. |
| 2b-ii — resource adapter + minimal finalization | **Done**, reviewed clean (2026-09-23) | `ea1091e`..`d514663` (6 commits) | Wires the 2b-i substrate into the actual tool-calling path for exactly one producer (`run_command`) and one consumer (`read_file`'s new `resource_id` selector), with `entities.output_storage` validation and TUI publication. Deliberately excludes `web_fetch`/MCP wiring, progress-ledger integration, and the fuller test matrix — planned as a follow-up "2b-iii" once this lands. Independent standalone gates passed for every original commit; full gate G and race checks passed at the reviewed tip. Review report: `.superpowers/sdd/next-generation-tool-runtime/phase-2b-ii-report.md`. |
| 3 — file reading and context refs | **Done** (2026-09-23) | `53ff92d`, `267b78e`, `57ae3b2`, `7df43e4` | Complete editable snapshots and stable file versions, bounded default/line/byte windows, exact/unknown totals and source coordinates, encoding/digest metadata, resource paging/expiry codes, delivery-time observed-version binding with approval pinning, ephemeral references through compaction, mutation/cancellation/secret-read coverage, and benchmark coverage are implemented. Full gate G and race subset pass. |
| 4 — composable search | **Done** (2026-09-23) | `4619962`, `d9fcf52`, `ffb8089` | Shared native/fenced search options, confined bounded scans, retained result sets, resource search, glob/list pagination, and optional rg parity benchmarks; no runtime external backend. |
| 5 — read/version/edit coupling | **Done** (2026-09-23) | `5e9eaa6`, `5a1a69e`, `4c295ec`, `0b81733`, `3f387ee` | Expected version selectors now guard edits and structured overwrites, with execution-time resource validation, stale digest rejection, approval/progress identity, no-op effect classification, delivery-time version recording, and focused stale/resource/protocol/agent tests. Full gate G, race subset, and vulnerability scan pass. |
| 6 — web freshness + MCP bodies | **Done** (2026-09-23) | `55fff71`, `871d6b7`, `29cf7b6` | Bounded web freshness/revalidation, retained session bodies, redirect-safe validators, controller refresh epochs, web result-set captures, and bounded MCP structured captures. Full tests, vet, build, lint, vulnerability scan, and race checks pass. |
| 7 — optional disk backing | **Done** (2026-09-23) | `49a48d5`, `63525ec` | Optional private disk body backend with memory default, redaction, atomic publication, quotas, stale-spool safety, config fallback, diagnostics, benchmarks, and cross-platform compilation checks. Full gate G and race checks pass. |
| 8 — calibration/evaluation | **Done** (2026-09-23) | `docs/architecture/next-generation-tool-runtime-phase8-report.md`, `internal/eval/live_test.go`, `internal/tui/live_agent_eval_test.go` | Deterministic matrix and benchmarks recorded with baseline/fixture hashes; live local-model matrices are explicitly skipped/censored because no endpoint/model was configured. Defaults remain unchanged; Windows ACL and embedded native-model gates remain open. |

**Phase 1a review caveat:** the implementer subagent for Phase 1a hit an account-level API spend limit mid-task (after piece 5 of 8) and was restarted/completed by the controller session directly, including a rebase to fix an unrelated worktree-creation base mismatch (the interrupted work had forked from before Phase 0 instead of after it — a tooling artifact, not implementer error; no Phase 0 content was actually lost). Given the same spend constraint, the phase's usual independent task-reviewer subagent pass was replaced with a manual controller self-review (full diff read of the safety-relevant files, confirmed `internal/tools/guardrails.go` untouched, confirmed no new upward imports into `internal/tui`, confirmed `internal/provider` untouched, confirmed path-resolution/approval call sites unchanged). All of gate G (`go test -count=1 ./...`, `go vet`, `gofmt -l`, `golangci-lint run`, `make build`) plus the required race subset passed. A future session should still get Phase 1a a normal independent review pass if one becomes cheap to run, before treating it as fully closed out the way Phase 0 is.

**PR:** [#110](https://github.com/patrikcze/llmtui/pull/110) (`feat/next-gen-tool-runtime` → `master`), opened 2026-09-22, updated same day with Phase 1b. Verified zero file overlap with the concurrent `feat/laya-decision-engine`/PR #109 (that PR only touches `internal/decision/`, `internal/cli/{root,decision}.go`, `internal/config/config.go`, `docs/decision-engine.md`) before each push; `origin/master` had not moved (still `b6c57a7`) so no rebase was needed either. Per the user (2026-09-22), PR #109/laya is intentionally parked for now — do not touch it or its branch.

## 1. Executive summary

CURRENT: llmtui already has one orchestration kernel, bounded agent cycles, task contracts, a separate verifier, resource-attributed receipts/recovery, a repeated-call ledger, bounded observations, session-local entities, entity keyword lookup, ranged reads, exact-match edits, progressive MCP disclosure, provider conformance diagnostics, and native/fenced tool correlation. Rebuilding any of these would be the wrong starting point.

CURRENT: the main missing capability is **retrieval of an adequately retained result body through the existing entity identity**, with reliable completeness, version, and freshness metadata. Today a fetch is truncated in `web.Client.Fetch` before its entity candidate is created; an entity retains at most 64 KiB; a command buffers all output before clipping it; MCP content is flattened and clipped; and a ranged read only slices the initial byte-limited file prefix. These are distinct loss points. A new wrapper around already-truncated strings would not repair them.

PROPOSED — EXTEND: make the entity registry the sole owner of model-addressable retained evidence. Give an entity an optional bounded, immutable body and typed resource metadata. The body's in-memory or optional temporary-file backing is private storage inside `internal/entity`, not another public artifact registry. Keep the existing entity lookup tool for discovery. Extend `read_file` and `grep` with an explicit `resource_id` selector. URLs still require `web_fetch`; resource reads never trigger network activity.

PROPOSED — REFINE: give `tools.Result` typed operation outcomes and completeness metadata, separate from controller execution status. Preserve call IDs and the current provider interfaces. Compute progress from stable content/coverage, not formatted previews, timestamps, or newly allocated IDs. Carry lightweight references in observations and summaries without replacing the evidence ledger.

PROPOSED — REFINE/REPLACE narrowly: keep exact unique replacement; bind edits to a previously observed full-file version when available. Replace the shared `O_TRUNC` write with a confined staged replacement. Be explicit: optimistic content checking plus atomic publication reduces stale edits and torn writes, but is **not** a portable filesystem compare-and-swap against an uncooperative external writer.

PROPOSED: implement measurements and typed outcomes first, then retained bodies plus bounded command capture, reliable reads/search, edit versioning, and explicit web reuse. Every new resource feature must include its ledger, scope, and cancellation integration in the same phase. Do not postpone those invariants until a final orchestration rewrite.

## 2. Goals

PROPOSED:

- Reuse previously retrieved evidence across ordinary turns and agent cycles, and after a completed agent task, without automatically repeating the original side effect or network request.
- Distinguish preview omission, retained-body loss, incomplete source coverage, and unavailable evidence.
- Let a model inspect a result by reference, search its retained body, and continue a range without pasting output back into arguments.
- Detect stale edit inputs while keeping the current simple replacement vocabulary.
- Preserve local-first, cross-platform operation, bounded resource use, and all existing authorization boundaries.
- Measure successful tasks and total cost, including failure/recovery calls and context growth; do not equate richer metadata with better model behavior.

## 3. Non-goals

PROPOSED — DEFER or exclude:

- No OMP UI clone, source port, prompt copy, or adoption of its tool names/selector grammar.
- No TypeScript/Rust runtime dependency, mandatory cloud service, hosted-model requirement, or new package dependency.
- No second orchestrator, verification loop, repetition detector, entity namespace, general retrieval service, or durable operation journal.
- No mandatory ripgrep, shell pipeline, embedding index, database, tree-sitter, language-server farm, or automatic helper-model repair.
- No automatic execution of formatters/tests from `edit_file`, no deletion tool, no automatic git checkpoint/rewind.
- No automatic durable memory promotion of raw resources; no persistence of images, clipboard data, personal-app content, or secret-file bodies through the new layer.
- No arbitrary MCP URI dereference, authenticated web browsing, cookie store, or general HTTP tool in this program.
- No change to native library pins, vendored FFI, licenses, release workflows, or provider timeout ownership.

## 4. Methodology and repositories reviewed

CURRENT: investigation used the supplied working tree, source/tests/docs and selected git history. Initial `git status --short` was empty and local branch was `master`. Relevant history includes `6656487` (entity registry), `cfc3d87` (keyword lookup), `56a9024` (visual observations), `4c5d4b0` (resource-attributed recovery), and `f865c98` (security fixes). These explain why neither entities nor receipts should be replaced.

CURRENT: reviewed `CLAUDE.md`, the architecture README/package map, ADRs 0001–0003, entity and local-model evaluation architecture, `docs/agent-loop.md`, `tools-architecture.md`, `tool-registry.md`, `context-management.md`, `prompt-composition.md`, `security.md`, `configuration.md`, and `agent-evaluation.md`. Compared relevant prior plans: entity context, visual observations, agent evolution, measured reliability, and better-agent. Source wins where these documents disagree.

CURRENT: inspected the execution paths and relevant tests in `internal/tools`, `web`, `entity`, `agent`, `agentverify`, `tui`, `provider` and its adapters, `prompt`, `contextmgr`, `mcp`, `history`, `chat`, `redact`, and `eval`; also reviewed configuration and safety integration. The Go design-patterns skill informed ownership/bounds review; repository conventions take precedence over its generic recommendations.

REFERENCE: downloaded upstream `main` into `/tmp/llmtui-omp-architecture-reference` without installing dependencies or executing OMP. Reviewed tool implementations, native edit-store implementation, prompts, docs, artifact/read/search tests, benchmark verification and runner code. [OMP repository](https://github.com/can1357/oh-my-pi) and [OMP website](https://omp.sh/) were checked for project context, not used as evidence of measured superiority. All OMP links below pin the reviewed commit.

CURRENT — validation performed:

| Check | Observed result | Limit of inference |
| --- | --- | --- |
| `go version` | `go1.27.1 darwin/arm64` | One platform, not cross-platform execution |
| `go test -count=1 ./...` in sandbox | Failed in `selfupdate.TestDownloadAssetSuccess`: localhost listener bind denied | Environment failure, not a discovered code regression |
| Same full command with localhost allowed | Passed | Does not imply native inference, live services, race checks, or release archive coverage |
| Targeted `TestLiveEvaluationMatrix` and `TestLiveAgentMatrix` in `internal/eval` and `internal/tui`, with `go test -count=1 -v` | Both live tests explicitly skipped: endpoint/model not configured | No actual-model success-rate measurement was obtained |
| Tool microbench inventory | No existing `Benchmark...` functions in `internal/tools` or `internal/web`; TUI transcript benchmarks exist | No claimed before/after timings; Phase 0 supplies missing workload baselines |
| OMP tests/benchmarks | Source inspected, not executed | No imported benchmark scores or claim of reproduced weather conversation |

PROPOSED: do not make model downloads or new paid calls a prerequisite for implementation. Use the already opt-in evaluation seams when configured. All numerical budgets and acceptance targets below are proposed bounds/targets, not measured improvements.

## 5. Current llmtui architecture

CURRENT — actual flow:

```text
human input / agent executor objective
  -> tui dispatch / continueChat
  -> pipeline.go: compositionBase, prepareRequest, activeToolSpecs
  -> prompt.Compose + contextmgr.Decide/Split + provider.ChatRequest
  -> provider.Provider.Chat (OpenAI-compatible / Ollama / embedded)
  -> handleStreamEvent; EnsureToolCallIDs + CallsFromNative
     or tools.Parse for fenced calls
  -> Model.startToolBatch
     controller capabilities: ask_user / get_entity_details / tool_search
     ordinary batch: progressLedger.planBatch + budgets + approval
  -> runToolPlan / runPlannedToolBatch (async tea.Cmd)
  -> Runner.ExecuteContext or executeMCPCall
     executeDurableCall around journaled operations
  -> mcpToolResultsMsg, generation validation
  -> recordAgentToolResultsCount + progress.observeResults(real results)
  -> sendToolResults -> registerResultEntities
  -> tools.NativeResults (role:tool) or tools.FormatResults (fenced continuation)
  -> continueChat / dispatch -> next provider request
  -> for /agent: deterministic evidence + agentverify verifier
     -> cycle memory -> agent.Decide -> next cycle / terminal state
```

CURRENT — evidence index for later sections:

| ID | Path and symbol | What it establishes |
| --- | --- | --- |
| L1 | `internal/tui/turn_runtime.go`: `turnRuntime`; `app.go`: `startToolBatch`, `sendToolResults`; ADR 0001 | One shared lifecycle, generation/cancellation ownership, one continuation path |
| L2 | `internal/tools/tools.go`: `Call`, `Result`, `Runner.ExecuteContext`; `native.go`: `Specs`, `CallsFromNative`, `NativeResults` | Provider-independent execution values; result currently contains `Output`, `Diff`, `Err`, `Entities` |
| L3 | `internal/tools/tools.go`: `readFile`, `renderLineRange`, `CanonicalReadRange` | `os.Root` read confinement; whole-prefix byte cap precedes range selection; explicit range 200 default/500 ceiling |
| L4 | Same file: `editFile`, `writeFileChecked`, `readRootFileLimited`; `edit_file_test.go`: `TestEditFileStaleContentGuard` | Exact unique replacement; internal read/recheck; `O_TRUNC`; test directly checks supplied stale bytes, not model-read version or final race |
| L5 | `internal/tools/search.go`: `globFiles`, `grepFiles`, `searchBase` | Shell-free search, 200 results/10,000 eligible-file cap, sorting, skipped large/unreadable/binary files, no cursor |
| L6 | `internal/tools/tools.go`: `runCommandContext`; `guardrails.go`; security review tests | Sanitized environment, command classification/confinement, process-tree control; `bytes.Buffer` before result truncation |
| L7 | `internal/web/fetch.go`: `Fetch`, `capContent`, `fetchResponse`; `web.go`: `Page`, `NewClient`; `ssrf.go`: `guardedDial` | 4 MiB raw read cap, 128 KiB default model content cap, HTTP/1 fallback under shared deadline, direct vetted-IP dialing, no generic cache |
| L8 | `internal/tools/web.go`: `webSearch`, `webFetch`, `safeWebURL` | Entity candidates are created from already-reduced page content; safe URL metadata strips query/userinfo; model output still includes page URL |
| L9 | `internal/entity/entity.go`: `Candidate`, `View`, `Limits`; `registry.go`: `Put`, `Resolve`, `Reset`; `search.go`: `SearchWithOptions` | Bounded transient registry, trust/scope/digest, pinning, deterministic eviction, keyword discovery; truncated payload storage |
| L10 | `internal/tui/entity_context.go`: `registerResultEntities`, `handleEntityDetailsBatch`, `resolveEntityDetails` | Successful results registered after agent receipt recording; blanket agent-run scoping; lookup can encode per-item errors while outer `Err` is nil |
| L11 | `internal/agent/receipts.go`: `ActionStatus`, `resourceKeyFor`; `types.go`: `ToolCallRecord`, `ExecutionResult` | Executed/denied/blocked/unknown already exist; outcome distinct from execution admission |
| L12 | `internal/agent/observations.go`: `ObservationCache.Put`; `recovered.go`, `proof.go`, `criteria.go` | 32 × 512-byte excerpts, omission tracking, resource-aware recovery/proof; no reusable full-output store |
| L13 | `internal/tui/progress.go`: `progressFingerprintAtRoot`, `progressDigest`, `planBatch`; `agent_loop.go`: `recordAgentToolResultsCount` | Canonical calls/ranges/freshness; digests of formatted output/errors; pre-execution repeat blocks; real execution currently sets `NewEvidence` broadly |
| L14 | `internal/mcp/stdio.go`: `CallTool`, `readBoundedLine`; `internal/tui/mcp_tools.go`: `executeMCPCall` | MCP text flattening, `isError` mapping, 4 MiB frame cap, per-server timeout, text cap before candidate |
| L15 | `internal/tui/tool_search.go`: `toolDiscoveryActive`, `discloseTools`; `pipeline.go`: `activeToolSpecs`; `internal/toolapi` | MCP progressive disclosure already context-budgeted; HTTP endpoint mirrors active native definitions only |
| L16 | `internal/provider/provider.go`: `ToolSpec`, `ToolCall`, `MessageReference`, `ValidateToolCalls`; `tools.go`: `NormalizeToolParameters` | Canonical schema/call boundary and limits, ephemeral references, no resource ownership in providers |
| L17 | `internal/provider/openai/openai.go`, `stream.go`; `ollama/ollama.go`; `embedded/llamart/tools.go`, `harmony.go`; `model_protocol.go` | Wire adaptation, streaming reconstruction, model/template-specific native grammar already exist |
| L18 | `internal/prompt/compose.go`: `Compose`; `contextmgr/contextmgr.go`: `Split`, `HeuristicSummarizer`; `tui/pipeline.go`: `prepareRequest` | Labeled prompt sections, schema accounting, complete tool-group retention, raw-user invariants, summaries are projections |
| L19 | `internal/history/operations.go`: `IsDurableSideEffect`, `Begin`, `Complete`; `tui/mcp_tools.go`: `executeDurableCall` | Existing crash/idempotency journal, including MCP; ambiguous operations cannot be replayed |
| L20 | `internal/tools/personal_apps.go`: `personalApps`; `internal/personalapps/personalapps.go`: `Result`, statuses/operations | Domain outcomes live inside JSON while outer Go error is nil after domain evaluation; exact-plan approval remains separate |
| L21 | `internal/eval/live.go`: `RunContractMatrix`, `RunConformanceMatrix`; `tui/live_agent_eval_test.go`; `agentverify/contract_eval_test.go` | Existing opt-in evaluations to extend, not replace |

CURRENT — documentation discrepancies to correct during implementation, not silently perpetuate:

1. `docs/tools-architecture.md` contains old simplified signatures/schema examples; `readFile` now accepts offset/limit and `ToolSpec` has `Strict`.
2. Strong prose claiming concurrent edits are never clobbered exceeds L4's check-then-`O_TRUNC` guarantee. Internal recheck is useful but has a final race window and no prior model-read binding.
3. Architecture prose mentions post-redirect resource fingerprints; L13 fingerprints the requested `Call.Path`, not `Page.URL`. A different eventual response cannot rescue a call already blocked before execution.
4. Entity `View.Truncated` is set if **either** payload or preview is shortened in `Registry.Put`; it does not mean the full retained payload is necessarily incomplete. Separate those facts.
5. Docs describing output size caps do not prove bounded capture memory. L6 is unbounded until the command exits.

## 6. Current tool inventory

CURRENT: all workspace schemas below are owned by `internal/tools/native.go:Specs`; web by `WebSpecs`; skills by `SkillSpecs`; personal-app schemas by `PersonalAppsSpecs`/`personalAppsOperationSchema`. Safety/approval metadata comes from `registry.go`, actual admission from controller and runner. “Reusable” means available through the entity registry, not merely present in transcript text.

| Tool | Implementation | Safety / approval | Output shape | Pagination | Reusable result | Truncation / main weakness |
| --- | --- | --- | --- | --- | --- | --- |
| `list_dir` | `tools.go:listDir` | read-only | text names | None | No | 200 entries then explicit remaining count; initial `ReadDir` materializes directory |
| `read_file` | `readFile`, `renderLineRange` | read-only; secret reads ask | text; optional range header | offset/limit | `file`, except secret paths | Initial byte prefix only; later lines unreachable; no raw version |
| `glob` | `search.go:globFiles` | read-only | sorted paths | None | No | 200-match note; no reusable result set or scan cursor |
| `grep` | `grepFiles` | read-only; explicit secret file asks | `path:line:text` | None | No | 200 matches, byte cap, 10k files; large/unreadable/binary skips not fully reported; lines clipped to 500 |
| `write_file` | `writeFile`, `writeFileChecked` | workspace-write, asks | completion text; display-only diff | N/A | No | Size cap; direct truncating write; no old-version contract |
| `edit_file` | `editFile` | workspace-write, asks | one-replacement confirmation + diff | N/A | No | Exact unique fragment; internal recheck only; non-UTF8 rejection; no-op rejected |
| `run_command` | `runCommandContext` | command; classify then ask as needed | combined stdout/stderr plus Go error | None | No | Post-execution text cap; full stream not retained; unbounded buffer |
| `web_search` | `tools/web.go:webSearch`, `web/search.go:Search` | network; ordinary queries allowed, opaque/bulk ask | title/URL/snippet list, framed | max_results only | one `web_result` per hit | No whole query-set identity/coverage/freshness contract; empty valid result is text |
| `web_fetch` | `tools/web.go:webFetch`, `web/fetch.go:Fetch` | network, asks | status/head + framed text | None | `web_page` | Raw and rendered caps discard body; one combined truncated flag; no conditional reuse |
| `local_context` | `local_context.go` collectors | read-only; clipboard asks | bounded JSON/text | limit for lists | No | Volatile; summaries intentionally discard timestamp/clipboard payload; do not turn into generic cached facts |
| `ask_user` | `ask_user.go`, TUI handler | controller pause, never approval | JSON answer | N/A | Evidence of answer, not blob | Must be alone; cancellation not equivalent to answer |
| `tool_search` | `tool_search.go`, TUI handler | controller discovery | structured matches/count/truncated/hint | bounded query, no cursor | disclosed schemas in task state | Existing bounded disclosure; not content search |
| `get_entity_details` | `entity_details.go`, `tui/entity_context.go` | controller read-only | JSON per-ID statuses/views | levels/query, no body ranges | Yes, bounded payload | Can return error statuses inside successful outer result; cannot search omitted body |
| `skill_load` | `tools.go:skillLoad`, skill adapter | prompt-state change; workspace policy applies | confirmation/error | N/A | active skill state | Not execution or authorization; body is prompt material, not result artifact |

CURRENT — optional personal-app tools (schema owner above; all dispatch through `ToolPersonalApps` to the existing domain service):

| Tool | Safety / output | Pagination/reuse and required preservation |
| --- | --- | --- |
| `status` | personal-app capability metadata | No content; no launch/permission prompt |
| `mail_accounts` | bounded account metadata | Domain handles, not entity bodies |
| `mail_mailboxes` | bounded mailbox metadata | Domain hierarchy selection |
| `mail_search` | bounded metadata results/coverage | Existing domain limits/continuation; no body retrieval implied |
| `mail_read` | selected bounded message bodies | Sensitive domain result; exclude from new generic capture |
| `calendar_list` | calendar metadata/writability | Domain handles |
| `calendar_events` | bounded time-window occurrences | Preserve partial coverage |
| `calendar_event` | selected event and observed version | Existing version precondition is independent of file snapshots |
| `calendar_free_slots` | local computed availability | Valid only for observed selected calendars/coverage |
| `change_prepare` | immutable change-plan preview | No external mutation; retain exact-plan semantics |
| `change_apply` | status/code including uncertain outcome | Always exact human approval; never generic auto or resource replay |
| `open_item` | app navigation outcome | Not a general path/URL opener |

CURRENT: personal-app evaluated failures/partial/unsupported outcomes are represented by domain `Status`/`Code`; generic `Err == nil` accounting alone is insufficient (L20). Adapt these statuses without changing that domain's policy.

CURRENT — MCP separately: server-owned arbitrary JSON schemas, names `mcp__<server>__<tool>`, `SafetyExternalMCP`, per-server approval and timeout. `stdio.CallTool` extracts text blocks, drops unsupported block details, and preserves `IsError`. `executeMCPCall` frames/caps text, then creates a successful `mcp_result` candidate. No generic MCP pagination or caching is safe to infer. All MCP operations remain potentially side-effecting for the journal.

CURRENT: there is no separate built-in git, general HTTP, or generated-artifact tool. Git output arrives through `run_command`; future producers should use the same result contract.

## 7. Existing strengths that must be preserved

PROPOSED — KEEP:

| Mechanism | Preservation requirement |
| --- | --- |
| Shared `turnRuntime`/kernel | Every new read/search dispatch uses existing batch lifecycle, cancellation generation, approval and continuation |
| Bounded runs/contracts | No resource lookup bypasses live call/token/time admission; criterion IDs remain controller-owned |
| Executor/verifier separation | Verifier remains tool-free, receives selected proof with provenance; entity existence is not proof of task completion |
| Resource recovery/cycle memory | Keep resource attribution and omission manifests; add references, not another recovery cache |
| Repetition ledger | Extend canonical identity/content digest rules in `progress.go`; no parallel loop detector |
| Durable operation journal | Never rerun commands/MCP to reconstruct missing output; cache hits are not side-effect receipts |
| Approval policy | Exact tool/target/content grants; pending approval owns input; `ask_user` cannot authorize |
| Workspace/command/web confinement | Existing regression suites remain release gates; no resource handle becomes a filesystem or shell escape |
| Vision entities | Preserve `vision_model_derived` trust and kind filters; never infer screenshot evidence from web/file output |
| Discovery/tool API | Retain one active schema snapshot and context-budget checks; no hidden executable tools |
| Providers/fallback | Canonical calls and one result per accepted call; existing Harmony/Gemma/native/fenced handling stays |
| Context/cache invariants | Complete tool groups, immutable raw user message, fully composed cache keys, no cache reply in place of live continuation |

## 8. OMP architectural findings

REFERENCE: the following are implementation observations, not benchmark claims. Links pin exact source; accompanying symbols locate the behavior.

| Evidence | Finding, problem solved, and llmtui relevance |
| --- | --- |
| O1 — [session/artifacts.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/session/artifacts.ts), `ArtifactManager`, `writeArtifact` | Session artifact IDs and staged verified publication retain output beyond inline limits. `sanitizeToolType` prevents external tool names becoming paths. Adopt bounded publish-after-success semantics, not a second artifact namespace. |
| O2 — [internal-urls/artifact-protocol.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/internal-urls/artifact-protocol.ts), `resolveArtifactFile`, `ArtifactProtocolHandler` | Path-only resolution avoids loading a large artifact for search; reads can page it. Resolver prioritizes caller's directory but then searches active-session directories. Do not copy this cross-session fallback into llmtui. |
| O3 — [tools/fetch.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/fetch.ts), `fetchReadUrl`, `executeReadUrl`, `ensureReadUrlArtifact`, `materializeReadUrlToFile` | Rendered URL output can be spilled before the inline preview is returned and materialized for search. At this revision `fetchReadUrl` calls `renderUrl`; these functions do not establish a universal session HTTP-cache hit. `persistReadUrlArtifact` directly uses `Bun.write`, unlike O1's verified publication helper. Copy the capability, not assumptions of uniform integrity or freshness. |
| O4 — [tools/read.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/read.ts), `ReadTool`; [read-summary.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/read-summary.ts) | Ranged file-backed artifact reads and structural summaries with omitted-range recovery reduce prompt volume. Unified path selectors also encompass URLs, archives, SQLite, images, SSH and more. llmtui needs explicit file/resource targets, not this entire grammar. |
| O5 — [tools/grep.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/grep.ts), `GrepTool`, `searchSchema` | Small public schema hides internal grouping/limits; searches internal URLs and multiple scopes, has file skip pagination and per-file caps. Native 4 MiB file window is still a coverage limit. llmtui should report every coverage limit, not assume optimized search is exhaustive. |
| O6 — [tools/glob.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/glob.ts), `GlobTool`; [tools/jfind/index.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/jfind/index.ts), `FindTool` | Glob is filename discovery; current `find` is semantic search with a judge-model cascade, not a synonym for shell find. Keep deterministic llmtui glob/grep; defer an extra model-dependent search tool. |
| O7 — [tools/tool-result.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/tool-result.ts), `ToolResultBuilder`; [tools/output-meta.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/output-meta.ts), `OutputMetaBuilder` | Content, error flags, display metadata, limits and continuation are distinct. Partial long lines suppress unsafe continuation hints. Adopt typed metadata and one formatting boundary; fluent builders are unnecessary in Go. |
| O8 — [tui streaming-output.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/tui/src/tools/streaming-output.ts), `OutputSink`, `enforceInlineByteCap`; [tools/bash.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/bash.ts), `BashTool` | Bounded inline capture can preserve a separate output artifact and report artifact-write failures. Artifact head/tail caps can also omit content: an artifact is not automatically lossless. Adopt bounded capture plus explicit retained coverage. |
| O9 — [edit/store.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/edit/store.ts), `getEditStore`; [crates/pi-edit/src/store.rs](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/crates/pi-edit/src/store.rs), `EditStore.record`, `record_seen_lines`, `by_hash`, `record_noop` | Session snapshots record version and seen lines with bounded history/no-op state. Adopt read-version coupling. Do not transplant clipboard registers, hashline language or a second no-op guard. |
| O10 — [edit/index.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/edit/index.ts), `EditTool`; [utils/edit-mode.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/utils/edit-mode.ts), `resolveEditMode` | Several edit protocols coexist; default hashline can resolve to replacement for specific model families. This is evidence against treating one protocol as universally optimal. llmtui should first evaluate exact replacement plus versions. |
| O11 — [edit/auto-repair.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/edit/auto-repair.ts), `attemptEditAutoRepair` | Optional model-assisted syntax repair can add a second change after an edit. Defer: another local inference, new unintended-change risk, and approval implications exceed this task. |
| O12 — [tools/checkpoint.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/checkpoint.ts), `CheckpointTool`, `RewindTool`; [session/checkpoint-entries.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/session/checkpoint-entries.ts) | Explicit checkpoint/rewind state and retained rewind reports coordinate history restoration. llmtui's journal and bounded run state solve different problems; do not interpret them as missing git rewind. |
| O13 — [agent/agent-loop.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/agent/src/agent-loop.ts), `normalizeTools`; [agent/append-only-context.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/agent/src/append-only-context.ts) | Wire schemas have stable identities and provider-facing normalization; context tracks tool results/error flags. Preserve llmtui's canonical schema boundary and stable prompt prefix; avoid importing OMP-specific intent-field/dialect machinery. |
| O14 — [tools/approval.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/coding-agent/src/tools/approval.ts), `resolveApproval`, `resolveApprovalFromContext` | Approval is an execution-time policy concern, independent of descriptions. llmtui already has this separation; no policy replacement justified. |
| O15 — [metaharness edit runner](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/metaharness/adapters/edit/runner.ts), `BenchmarkConfig`; [benchmark verify.ts](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/typescript-edit-benchmark/src/verify.ts), `verifyExpectedFiles` | Repeated tasks, variant controls, independent file verification, retry/time budgets and token counts. Verification may normalize formatting/blank lines and runner may early-stop on a matching fixture. llmtui must report exact-byte and semantic outcomes separately and not hide failures in retries. |
| O16 — [native grep benchmark](https://github.com/can1357/oh-my-pi/blob/0f9139f546e1dcb0e63ce35f1236509c4cca66c1/packages/natives/bench/grep.ts) | Benchmarks multiple repository sizes/output modes against `rg`, warming native root state. Adopt parity-checked workloads and distinguish warm/cold, not those timing numbers. |

REFERENCE: tests inspected include `test/artifacts-integrity.test.ts` (short writes/unpublished files/prior artifact preserved), `test/tools/read-truncation-metadata.test.ts` (partial-line honesty and no duplicate serialized body), and `test/tools/grep-internal-urls.test.ts` (internal-resource search). Also located large-artifact, read/seen-lines, multirange, artifact-sanitization/concurrency and edit-mode regression suites. These are useful test categories; their existence is not proof of llmtui behavior.

REFERENCE: tool prompts explicitly teach recovery from omitted ranges, prefer specialized search over shell grep, and distinguish URL reads from web discovery. Reuse these principles in original short instructions. Current OMP's rich path grammar and model-assisted `find` are complexity costs as well as capabilities. `docs/tools/read.md` mentions a URL cache, but inspected fetch entry points do not justify promising cache hits; pin source-based claims narrowly.

## 9. Concept translation matrix

PROPOSED: classifications below apply to llmtui changes.

| Capability/concept | OMP approach | Current llmtui | Gap | llmtui-native decision | Priority |
| --- | --- | --- | --- | --- | --- |
| Addressable evidence | Artifact/internal URLs (O1–3) | Entity IDs and lookup (L9–10) | Bodies already clipped, no range/search target | EXTEND entities with private body backing; one `resource_id` field using entity IDs | P1 |
| Large output | OutputSink + artifact (O8) | String cap after capture (L6/14) | Unbounded command RAM/lost tails | REPLACE command buffer; EXTEND common capture metadata | P0 for RAM, P1 reuse |
| Error/result metadata | Content + details + isError (O7) | Err + text + domain statuses (L2/20) | Inconsistent accounting | REFINE Result; KEEP controller status distinction | P1 |
| Read pagination | Bounded selectors/recovery (O4/7) | Prefix then range (L3) | Later ranges unreachable | REFINE streaming bounded range reads; explicit EOF/next cursor | P1 |
| Search composition | Internal targets/grouped caps (O5) | Workspace regexp only (L5) | No result targets/coverage | EXTEND Go grep with body targets and durable-within-session result sets | P1 |
| Read/edit version | EditStore snapshots (O9) | Exact text + internal recheck (L4) | Earlier full version unknown | EXTEND optional `expected_resource_id`, runtime-owned observed-version selection | P1 |
| Write publication | Staged write concepts | O_TRUNC shared core (L4) | Torn writes/race overclaim | REPLACE publication primitive only; explicit optimistic contract | P0 correctness |
| Hashline/fuzzy | Multiple modes (O10) | One exact mode | No measured need for alternatives | KEEP exact mode; DEFER anchors/patch/fuzzy behind comparative evidence | P3 |
| HTTP reuse | Retained fetch output (O3) | No generic cache (L7) | No age/validation contract | EXTEND session metadata and conditional requests, conservative default | P1 |
| No-progress | Several OMP guards | Existing canonical shared ledger (L13) | Content vs wrapper digest; refresh admission | REFINE existing ledger only | P1 |
| Model adaptation | Variant/density/wire normalization (O10/13) | Native adapters, fallback, conformance (L16–17) | Contract reliability needs measurements | KEEP representation; EXTEND fixtures, progressive instructions | P2 |
| Semantic find | Judge cascade (O6) | RAG/entity lookup + grep | No proven retrieval gap requiring another model | DEFER | P3 |
| Checkpoint/rewind | Dedicated history/git state (O12) | Run state + journal (L19) | Different objective | KEEP journal; DEFER rewind | P3 |
| Compaction | Tool-aware history machinery | Complete groups + bounded summaries (L18) | Resource reference survival | EXTEND ephemeral message references; KEEP summarizer | P1 |

## 10. Gap analysis

CURRENT: source-level findings, with consequences distinguished from demonstrated exploits:

| Priority | Finding | Consequence / confidence | Required response |
| --- | --- | --- | --- |
| P0 | L6 accumulates all command output in `bytes.Buffer` | Confirmed allocation path; memory exhaustion magnitude not experimentally measured | Bounded writer before running large-output evaluations |
| P0 | L4 checks then opens with `O_TRUNC` | Confirmed torn-write/final-race exposure; no exploit claimed | Confined staged replacement; truthful concurrency guarantee and fault tests |
| P1 | L3 range is inside fixed prefix | Confirmed inability to reach arbitrary later lines | Separate scan budget from output budget |
| P1 | L7/L8/L9/L14 discard output at multiple stages | Confirmed unretrievable omission | Capture before display truncation; explicit source/body/preview completeness |
| P1 | L5 skips large/unreadable files without full coverage metadata | Confirmed incomplete-search ambiguity | Structured coverage even for zero matches |
| P1 | L10 agent-run scoping releases read entities at run end | Confirmed lifecycle; impedes post-run follow-up reuse | Session-retain safe published evidence, retain run provenance separately |
| P1 | L13 formatted-output digest; L20 domain failures with nil Err | Confirmed contract mismatch; future IDs/timestamps would worsen it | Typed outcomes and stable observation digests before resource rollout |
| P1 | L4 lacks model-read version | Confirmed API gap; unique old text can still exist in a changed file | Bind full-file digest to prior observed read when available |
| P2 | L9 preview/payload truncation merged, no explicit full-body provenance | Confirmed ambiguity | Separate metadata, preserve exact known/unknown counts |
| P2 | L14 ignores non-text/structured MCP parts | Confirmed flattening | Retain bounded JSON/text and explicit unsupported-part counts; no URI auto-fetch |
| P2 | L7 `checkURL` does not reject userinfo | Confirmed validation scope; not an SSRF bypass claim | Reject credential-bearing URLs; redact query display independently of identity |
| P3 | No AST summaries, multi-range edits, external optimized search | Opportunity, not defect | Benchmark first; do not bundle into foundational work |

## 11. Design principles

PROPOSED:

1. One result identity visible to models: an entity ID. Call IDs identify attempts; receipt/observation IDs identify proof; neither is a second content addressing scheme.
2. Never promise retained bytes before publication succeeds. A useful preview may survive failed retention, with `retention=unavailable`.
3. Separate source state, captured body, model window, and semantic claims. A complete retained prefix of an incomplete source is still incomplete source evidence.
4. Immutable bodies; mutable indexes contain only references. Freshness is an observation property, not a rewritten content payload.
5. No hidden I/O on lookup. Reading an entity never fetches a URL, reads the current workspace file, reconnects MCP, or reruns a command.
6. Keep model schemas flat and shared. Runtime validation enforces mutually exclusive selectors without depending on `oneOf` support.
7. Store bytes once. Entity previews and agent excerpts are deliberately bounded projections, not duplicate full payloads.
8. Resource retrieval consumes budgets and respects source/trust/sensitivity. It does not grant capabilities.
9. Every phase changes tests/docs alongside behavior. No global logging, telemetry, or broad framework abstraction.

## 12. Proposed target architecture

PROPOSED — EXTEND existing packages, avoiding a public `artifact`/`toolresultstore` package:

```text
tools / web / MCP adapter -> bounded capture + typed tools.Result
  -> shared TUI result finalization (generation checked)
      -> entity.Registry.Publish (metadata + one optional body)
      -> receipt / observation refs + stable progress digest
      -> bounded model formatter -> native/fenced continuation

entity.Registry
  metadata/index/limits/pins/expiry/IDs
  private BodyStore (memory; optional session-temp backend)
  Lookup/OpenBody -> narrow read-only lease to tools

get_entity_details -> discover/inspect existing entity metadata
read_file(resource_id=...) -> window of retained body
grep(resource_id=...) -> bounded body search
read_file(path=...) -> confined current file + observed version
edit_file(path=..., expected_resource_id=...) -> versioned exact replacement
```

PROPOSED — package boundaries and changes:

| Owner | Files to change/create | Contract |
| --- | --- | --- |
| `tools` | new `result.go`, `result_format.go`, `output_capture.go`, `file_read.go`, `file_write.go`; extend `file_edit.go`, `search.go`, `native.go`, `tools.go`, `web.go` | Own call validation, result outcome/coverage, bounded producer capture, file operations and format; extract relevant functions from hotspot rather than extending it indefinitely |
| `entity` | extend `entity.go`, `registry.go`; new `body.go`, `body_memory.go`, later `body_disk.go`, `body_disk_*`, `resource.go` | Sole content-ID/index owner, immutable body storage, leases, limits, eviction, cancellation; no import of tools/TUI/provider |
| `tui` | new `tool_result_finalize.go`, `tool_resource_adapter.go`; change `app.go`, `entity_context.go`, `mcp_tools.go`, `progress.go`, `agent_loop.go`, `pipeline.go`, `tool_search.go` | Wire a session-scoped registry, authorize and finalize results once, attach proof/reference metadata; no disk scans in Update |
| `web` | `web.go`, `fetch.go`, `search.go`; new `conditional.go` | Transport and extraction, response metadata/conditional options; no second retained body cache |
| `mcp` | `mcp.go`, `stdio.go` | Preserve bounded text/structured result semantics at wire decode, no callback into TUI |
| `agent`/`agentverify` | `types.go`, `observations.go`, `receipts.go`, `recovered.go`, `proof.go`, relevant verifier formatting | Add bounded content-reference/digest/coverage evidence; retain existing execution statuses and state machine |
| `provider`/`chat`/`contextmgr` | ephemeral reference metadata and summary formatting only | No new provider execution API; raw bodies absent from history/control state |
| `config`/diagnostics | existing config types/defaults/validation; `/entities`, `/context`, `/debug` views | Small additive configuration; no new daemon or public output HTTP endpoint |

PROPOSED: keep provider and entity leaves independent. A `tools.ResourceReader` interface is declared by its consumer in `tools`; TUI supplies an adapter around its registry. The registry never imports `tools.Result`. Use existing constructor/dependency patterns; do not introduce a DI library.

PROPOSED: worker calls return bounded unpublished capture values. On a current-generation result, `finalizeToolResults` publishes candidates, obtains IDs, derives canonical metadata and records receipts/progress before formatting. For optional disk publication, stage/finalize in the existing async batch command; only register in Update after generation acceptance. Stale/cancelled batches must release staged bodies even when transcript results are discarded. No worker may mutate the currently active session through a stale adapter. The adapter captures a registry instance and generation, never a pointer to “whatever session is current.”

PROPOSED: all producers, including controller-only tools and terminal synthetic blocks, flow through the same typed finalization contract. Preserve their current standalone batch restrictions. A controller lookup that returns only failed resolutions is not recorded as a successful observation merely because JSON marshaling succeeded.

## 13. Tool-result/resource model

PROPOSED — REFINE `tools.Result` additively first. Go sketches specify ownership; names may be adjusted only to avoid existing collisions, not to change semantics.

```go
// internal/tools/result.go
type Outcome string
const (
    OutcomeUnknown Outcome = "unknown"
    OutcomeOK Outcome = "ok"
    OutcomePartial Outcome = "partial"
    OutcomeFailed Outcome = "failed"
    OutcomeCancelled Outcome = "cancelled"
    OutcomeTimeout Outcome = "timeout"
)

type ErrorInfo struct {
    Code string              // closed, stable vocabulary; not err.Error()
    Retry string             // none | correct_input | reread | later | reconnect
    Message string           // bounded, model-safe, actionable
}
type Coverage struct {
    SourceComplete bool      // producer actually inspected all intended source
    CaptureComplete bool     // all produced body bytes retained
    PreviewComplete bool     // all retained body bytes shown in this view
    ObservedBytes int64      // measured, not Content-Length asserted by a peer
    RetainedBytes int64
    TotalBytes *int64        // nil means unknown
    TotalLines *int64        // nil means unknown
    Reasons []string         // bounded vocabulary/count: bytes, files, line, etc.
}
type Window struct {
    StartLine, EndLine int64  // 1-based inclusive; zero for empty
    StartByte, EndByte int64  // 0-based half-open, retained representation
    NextOffset *int64        // next complete line, only when valid
    NextCursor string        // opaque bounded continuation; never a path
    PartialLine bool
}
type ResultMeta struct {
    Outcome Outcome
    Error *ErrorInfo
    Coverage Coverage
    Window *Window
    ResourceID entity.ID
    ContentDigest string     // stable retained representation hash
    Effect string            // none | changed | unchanged | unknown
    Reused bool
    Observation entity.ObservationMetadata
}
// Result keeps Call, Output, Diff, Err, Entities during migration.
// Add Meta and bounded Captures. Err still wraps original Go error with %w.
```

PROPOSED: `Captures` contains unpublished, quota-reserved body candidates, not arbitrary readers or producer callbacks. Each candidate carries its semantic entity candidate, representation/content type, source/capture coverage, sensitivity decision and an owned bounded buffer (later an already-staged private body handle). Limit initial producers to one canonical body per result; structured MCP parts share that body's documented encoding. Publication consumes ownership exactly once; rejection/cancellation releases the reservation. Do not duplicate the same body into `Output`, entity `Payload` and capture storage. Keep publication handles internal; only entity IDs enter model-visible results.

PROPOSED: keep `agent.ActionStatus` as **admission/execution** (`executed`, `denied`, `blocked`, `unknown`), supplied by batch/controller; do not duplicate it as an independently mutable tools field. Outer outcome plus action status yields “user denied,” “controller blocked,” “tool failed,” etc. An executed command timeout can have `Effect=unknown`; do not infer that it never changed a file. Empty valid output is `OutcomeOK`, zero entries/bytes, no fabricated failure. Partial is a successful bounded observation, not proof of an exhaustive result.

PROPOSED — EXTEND entity metadata:

```go
// internal/entity/resource.go
type ObservationMetadata struct {
    ObservedAt time.Time      // body acquisition time
    ValidatedAt time.Time     // last successful network validation, if any
    ValidUntil time.Time      // zero means no freshness assurance
    Freshness string          // snapshot | fresh | stale | unknown
    Acquisition string        // local | network | retained | revalidated
}
type FileVersion struct {
    Path string              // validated workspace-relative identity
    Digest string            // SHA-256 of COMPLETE RAW FILE, not rendered range
    SizeBytes int64
    Complete bool
    // private platform identity/stat fields for conflict checks, not prompt data
}
type ResourceMetadata struct {
    ContentType string
    BodyDigest string        // SHA-256 of retained canonical representation
    SourceDigest string      // optional; never pretend prefix hash is full hash
    ParentID ID              // only for actual derived bodies
    Relation string          // search_of | extraction_of | snapshot_of
    FileVersion *FileVersion
    Observation ObservationMetadata
    // Typed coverage/counts; preview limit separate from retained/source limits.
}
type BodyLease interface {
    io.ReaderAt
    Size() int64
    Close() error            // releases pin exactly once
}
type ResourceReader interface { // declared in tools using entity value types
    OpenBody(context.Context, entity.ID) (entity.ResourceView, entity.BodyLease, error)
}
```

PROPOSED: `Registry.Publish(ctx, Candidate, boundedBody)` returns a view only after complete publication of the **accepted capture**, even if coverage says the original source exceeded the capture limit. `Registry.Put` remains valid for legacy small semantic entities. Avoid building a body from `Output` after framing: producers supply canonical unframed content. `OpenBody` atomically validates ID/scope/expiry, pins body, and returns an immutable lease; I/O occurs without holding registry mutex. `Close` unpins; eviction cannot remove an open body.

PROPOSED: existing kinds stay; add `tool_output` for commands and generic future text, and `search_result` for captured result rows. `collection` continues to group semantic entities. Update `ValidKind`, `MaxEntityDetailsKinds`, schemas and visual-kind tests together. Do not turn `vision_observation` into raw image storage or personal-app handles into generic entities.

PROPOSED — one identity: all resources are entity IDs. Use `resource_id` in read/search schemas and return that same value as `id` in entity lookup. Do not introduce `artifact://`, filesystem spool paths, or another `res_` ID. `expected_resource_id` is the same ID referring to a file version. Parent/child links also use it.

PROPOSED — REFINE IDs for cross-session safety: mint new IDs as `ent_` plus 26 lower-case unpadded base32 characters encoding 128 random bits. Use `crypto/rand`, explicit generation failure, and injectable deterministic test source. Keep old `ent_00001` parsing only to return legacy-unavailable on resumed text; never map a legacy token to a newly generated resource. Existing live registry migration is unnecessary because upgrades restart the process. Every lookup is still scoped to one registry; random IDs supplement, not replace, session isolation. Update examples/tests using helpers, not copied literal IDs. Never parse arbitrary `ent_` strings out of untrusted source content and register them as controller references.

PROPOSED — identity distinctions:

- Call ID: unique accepted provider/fenced invocation; every invocation gets a correlated result.
- Entity ID: immutable captured content/version; new acquisition with changed content gets a new ID. A repeated view or same-body cache validation reuses its ID.
- Resource key: existing canonical target identity for recovery, private and potentially hashed; independent of content version.
- Observation/receipt ID: existing bounded agent proof record; can point to the entity ID but cannot be passed to `read_file`.
- Cursor: a bounded continuation capability tied to the same entity/view; not independently discoverable content.

PROPOSED: semantic entities remain bounded metadata/projections. Body bytes live only in the registry's private body backend and share one accounting/eviction lifecycle. This is not permission to raise `max_payload_bytes` to arbitrary sizes.

## 14. Artifact/output lifecycle

PROPOSED — EXTEND, with memory retention first and optional disk later:

1. Capture producer bytes incrementally under a hard budget; count total seen bytes when possible. Source I/O has its own cap/deadline.
2. Select a canonical representation: exact UTF-8 file text, extracted web text, stable search-row JSONL, combined command text, or MCP text/structured JSON parts. Record transformation/version; do not call extracted HTML “raw response.”
3. Apply sensitivity policy before retention. Exclude secret-file reads, clipboard, personal-app results and image bytes. Use `redact.Secrets` as best-effort protection for generic output, not a proof of safety. If redaction changes a file's text, do not publish an editable body; retain only a redacted projection and private version metadata, clearly labeled.
4. Publish immutable body/metadata under one entity ID; create preview from retained content with limits included in its total byte/token budget.
5. Register typed refs in the result message; future lookup/read/search uses them without producer I/O.
6. Evict deterministically using registry LRU/time + ID tie-breaking, skipping active leases/approval pins. Evict body and metadata together; short tombstones are bounded, never retain secret content. Return unavailable after eviction; never reconstruct by rerunning the producer.
7. Reset/close releases all bodies and staged files. A resumed saved session has no live bodies by default. Old references explicitly become unavailable.

PROPOSED — bounds (MiB = 1,048,576 bytes):

| Item | Initial default / ceiling | Semantics |
| --- | --- | --- |
| Model result budget | 8 KiB per tool result, lower if existing producer cap is lower; 32 KiB batch preview target before normal context preparation | Includes metadata/framing; reduction leaves recoverable refs where available; preserve one message per call |
| Semantic payload | Existing 64 KiB per / 4 MiB total | Existing small entities, visual observations; not increased to hold command logs |
| Retained output body | 4 MiB per / 16 MiB total in memory | Independently configurable; all bodies bounded before allocation growth |
| Capture staging | At most one per serialized Runner call; reserve bytes against total budget | No concurrent unaccounted `[]byte` copies; MCP batch is ordered |
| Optional disk backing | 4 MiB per initially / 128 MiB aggregate process-managed spool cap | Explicit opt-in; same entity owner, no raw unbounded stream |
| Entity count | Existing 256 default | Bodies count as entities; collection metadata bounded too |
| Cursor state | 64 active cursors/session; 256 bytes/token maximum; 10-minute idle expiry | Eviction means `cursor_expired`, no silent restart |
| Diagnostic tombstones | 256 metadata-only entries/session | Old IDs beyond tombstones still `unavailable`, not guessed |

PROPOSED: when a large batch's mandatory correlation/status metadata alone exceeds the 32 KiB preview target, remove optional previews first and preserve every required result envelope. The existing context guard must reject an unsendable continuation explicitly; never drop call results or claim the target is a hard bound on provider framing. Test this with long call IDs and 128-result batches.

PROPOSED: initially retain a contiguous prefix, not head+tail with synthetic missing lines. That makes line numbers and search coverage deterministic. A command may have a separate bounded tail for its immediate preview, but the preview explicitly labels two byte regions and the retained body coverage. Do not advertise a full artifact if the middle was dropped. The first implementation can use head-only previews for all producers to keep this simple.

PROPOSED: bounded command capture replaces `bytes.Buffer` with an `io.Writer` that retains at most capture budget, updates byte counters/digest, returns `len(p), nil` while discarding excess, and continues draining until process exit/cancellation. Do not kill a potentially mutating command merely because output storage filled, and do not let a storage error turn an exit-zero command into “not executed.” Keep process tree cleanup and `WaitDelay`. Use chunked capture to avoid growing copies; release capture storage after ownership transfers to the registry.

PROPOSED: generic output redaction may require a bounded final buffer. Keep it within reserved capture capacity, including transient copies; do not stream raw secret-bearing chunks to disk and redact afterward. Private-key/credential patterns crossing chunks require tests. If a safe bounded transformation cannot be completed, retain no body and report retention failure. No claim that best-effort redaction finds all secrets; session-disk mode is explicit storage opt-in.

PROPOSED — optional disk ownership: use a random owner-controlled session directory outside the workspace, permissions `0700`, files `0600`; never put tool names/URLs/model IDs into filenames. Use fixed hex internal basenames unrelated to user input. On Windows require owner-only ACL handling or disable disk backing with a clear diagnostic and fall back to memory; POSIX permission bits alone are not a Windows isolation guarantee. Open root-confined handles; verify regular files, never follow supplied spool symlinks. Write a temp sibling, verify byte count/digest, close, then rename into an immutable name. If any step fails, publish no handle to that file.

PROPOSED: keep an exclusive lifetime lock for each spool using existing platform locking patterns (review `personalapps/mutation_ledger_*`), release on close. Startup cleanup may delete only marked owned directories whose locks can be acquired; never delete a live session by age alone. Sweep stale abandoned spools to enforce aggregate cap before new disk allocation. If lock/ownership cannot be established, refuse disk allocation, retain a bounded preview, and continue with explicit `retention_unavailable`. No automatic archive/resume support in this program; temporary disk backing is not durable history.

PROPOSED: leases and pinned references count against quotas. If everything is pinned, new capture fails retention without evicting pending edit versions or exceeding budget. A command/MCP side effect that already happened remains recorded as happened. Storage errors never cause automatic replay.

## 15. read_file design

CURRENT: L3 already provides optional 1-based `offset`/`limit`, a compact header without per-line numbering, exact newline preservation, UTF-8-safe display conversion, an EOF error, and `os.Root` confinement. `read_range_test.go` intentionally locks in legacy whole-file behavior. It also demonstrates that the byte cap remains the read limit; it does not test arbitrary later ranges.

PROPOSED — REFINE/EXTEND: preserve the name, existing arguments and verbatim body. Adopt a bounded default window, and make `resource_id` an alternative to `path`. Keep all URL handling in `web_fetch`. An ID is never interpreted as a path; a path named `ent_...` remains a normal relative filename when passed in `path`.

PROPOSED — schema-level changes:

```json
{
  "type": "object",
  "properties": {
    "path": {"type":"string","description":"Workspace-relative file path. Use path or resource_id."},
    "resource_id": {"type":"string","description":"Exact retained entity ID. Reads saved evidence without fetching its source."},
    "offset": {"type":"integer","minimum":1,"description":"First line; default 1."},
    "limit": {"type":"integer","minimum":1,"maximum":500,"description":"Maximum lines; default 200."},
    "byte_offset": {"type":"integer","minimum":0,"description":"Advanced: copy next_byte_offset for a partial long line; cannot combine with offset/limit."}
  },
  "additionalProperties": false
}
```

PROPOSED: runtime requires exactly one of nonempty `path`/`resource_id`; no schema `oneOf`. `byte_offset` presence must be tracked with a pointer/optional field so zero is distinguishable from absence. Byte mode returns at most preview budget and `next_byte_offset`; no separate byte-limit knob. Invalid combinations receive `invalid_arguments` before I/O. Fenced form keeps `tool read_file <path>` plus optional JSON; for resource mode use no info-string path and JSON `{ "resource_id": "...", "offset": 201, "limit": 200 }`. Reject a resource selector combined with an info-string path.

PROPOSED — implementation algorithm in new `file_read.go`:

1. Validate bounds without integer overflow (`offset + limit` checked; int64 offsets internally). Enforce existing path and secret approval policy. Resolve through `os.OpenRoot` and check the opened descriptor is regular, not just the pre-open stat.
2. For files within `tools.max_file_kb`, read complete raw bytes under that cap+1, compute SHA-256 of raw bytes and line count, and check before/after descriptor metadata. Retry neither a changed source nor an overflow silently: return `source_changed` or a partial live view as specified. Publish a file-version entity only after successful complete acquisition. The digest describes the bytes actually acquired; metadata checks cannot prove an atomic external read snapshot against hostile concurrent writers.
3. For larger files, stream with bounded buffers (e.g. 32 KiB), discard complete lines before offset, collect at most requested lines and preview bytes, check `ctx` each chunk. The scan budget is separate from output: initially 64 MiB examined per call, bounded by cancellation/deadline. Do not load the first 512 KiB then slice it. Total raw size comes from descriptor stat with observed-time semantics; total lines remains unknown unless EOF was reached. Do not supply a full-file digest or edit-precondition token for a partial scan.
4. Read one boundary unit beyond the selected window to distinguish EOF from more content. Preserve CRLF and absence of terminal newline in body. `"a\n"` is one line; empty file is zero lines and default offset 1 returns empty success; offset >1 on empty file is range error.
5. On a complete-line window, return exact start/end and `next_offset` only if there is another line. On a partial long line, return byte interval, `partial_line=true`, `next_byte_offset`, and no next-line claim that skips its remainder. Byte mode on a live file is a new observation, labeled as such; on a retained entity it is an immutable continuation. Keep UTF-8 boundaries; invalid source bytes are counted and represented with replacements, marked `encoding_loss=true`, and never treated as copy-safe edit text.
6. Retain complete small snapshots once; each subsequent page can reuse the same body ID. Large live-file ranges may publish only the captured window with `source_complete=false`, its original line/byte coordinates and no full-file version. Recommend `path` plus returned offset to continue the live file, not the incomplete resource's EOF. For a huge line that exceeds capture limits, byte mode can seek directly to the returned raw byte offset under the same path policy.
7. Resource reads use a `BodyLease`, no filesystem source reopening. They return coordinates within the retained representation, plus source coordinates when available. Never hide upstream source truncation just because this body window reaches its own EOF.

PROPOSED — feature decisions:

| Feature / classification | Problem and new behavior | Model/API impact | Security/testing/measurement |
| --- | --- | --- | --- |
| Bounded default — REFINE | Whole-file defaults can expose 512 KiB; return 200 lines within 8 KiB | Intentional default change behind `tools.read.default_lines`; update legacy tests explicitly | Tiny-file output cost, token size; no bypass of secret approval |
| Exact later ranges — REFINE | Reach lines beyond old prefix via streaming | Existing offset/limit now useful across large files | Beyond-cap sentinel, scan cap, cancellation, long lines; ranged latency/allocations |
| Continuation — EXTEND | Separate complete-line/byte recovery | Structured `next_offset` or `next_byte_offset` | Unicode split, CRLF, EOF/empty, no infinite same-position continuation |
| Total counts — REFINE | Unknown totals no longer guessed | Nullable totals, observed size | Mutation during read; no extra full scan merely to count lines |
| Version — EXTEND | Bind full small-file snapshot to later edit | `resource_id`, private raw digest | Range-first read still hashes whole editable file; no digest for partial file |
| Binary detection — EXTEND | Do not fill context with binary noise | `unsupported_content`, bounded metadata | NUL sample and invalid-UTF8 policy; a NUL beyond sample still fails when encountered |
| Stable reference — EXTEND | Read same snapshot by ID | Optional resource selector | Session reset, pin/evict, cross-session failures; retrieval benchmark |
| Multi-range — DEFER | Could reduce repeated calls but complicates shape/defaults | No `ranges` array in first release | Reconsider only if task eval shows range-call overhead dominates |
| Code-aware summary — DEFER | Could reduce large-code prompts | No automatic AST summary now | Go-only `go/parser` experiment later; never substitute inferred summary for edit text |
| Line anchors — DEFER | Potential disambiguation | Keep verbatim text and range header | Avoid artificial numbers being copied into `old_text` |

PROPOSED: snapshot identity deduplicates within the registry by validated workspace target plus complete raw digest plus representation/sensitivity policy. Do not merge two unrelated files solely because their text is equal. Repeated reads can produce a new observation timestamp while returning the same body ID; progress ignores that timestamp.

## 16. grep/search design

CURRENT: L5 uses Go regexp, recursive `WalkDir`, sorted files, optional one glob and one root. It skips symlinks encountered during traversal, `.git`, and secret files recursively. Later `os.Stat`/`os.ReadFile` calls operate by pathname, unlike `readFile`'s `os.Root` descriptor path. Treat that as a confinement consistency gap to close, not evidence that every ordinary search escapes.

PROPOSED — KEEP Go-native search; REFINE its coverage and opening; EXTEND composability:

```json
{
  "type":"object",
  "properties":{
    "pattern":{"type":"string"},
    "path":{"type":"string","description":"One workspace file or directory; default workspace."},
    "resource_id":{"type":"string","description":"Search retained text only; no source I/O."},
    "glob":{"type":"string","description":"Optional include glob for workspace files."},
    "literal":{"type":"boolean","default":false},
    "case_sensitive":{"type":"boolean","default":true},
    "context":{"type":"integer","minimum":0,"maximum":5,"default":0},
    "limit":{"type":"integer","minimum":1,"maximum":200,"default":100},
    "cursor":{"type":"string","description":"Copy a returned continuation; omit all query/target fields."}
  },
  "additionalProperties":false
}
```

PROPOSED: first query requires pattern and forbids simultaneous path/resource. Continuation requires cursor alone (optionally a smaller limit); server validates cursor query binding. Keep existing Go regexp semantics, not PCRE. Literal mode uses literal matching rather than escaping through a shell; preserve intentional pattern whitespace. Case-insensitive regexp wraps with a scoped Go flag, without text normalization that changes the query. `glob`/context are not applicable to a bare non-file resource filter except context lines; return invalid argument for workspace-only filters on a resource instead of ignoring them.

PROPOSED: use shared Go decoder for native and fenced forms. Preserve legacy `tool grep <path>` body pattern. JSON fenced search is recognized only when the complete body is an object with the new known fields; otherwise preserve literal regex body. Avoid ambiguity by documenting a dedicated JSON-object body form and testing patterns that begin with `{`. Native and fenced normalized calls must have identical fingerprints.

PROPOSED — bounded snapshot of search results, no mandatory index:

- Enumerate stable lexicographic workspace-relative paths through `os.Root.FS`/root-confined opens. Skip symlink entries, recheck opened regular-file metadata, and retain secret-path checks on both logical and resolved target identity. No automatic `.gitignore` semantic change; keep current `.git`/secret policy. Explicit secret targets retain approval and are not retained as bodies.
- Stream each file with bounded line handling. Initial limits: 10,000 eligible files, 8 MiB examined per file, 64 MiB aggregate source bytes, 10,000 retained match rows or retained-body cap (whichever first), 30-second search deadline inherited from parent if sooner. Check cancellation while walking and every chunk. No unbounded `strings.Split` of entire files.
- Record rows as typed records, serialize canonical JSONL for body storage; stable path/line/match order. Rows contain path or parent resource ID, line/byte range, bounded text/context, and `text_complete`. Do not create one entity per match.
- Initial response shows up to limit rows and supplies a `search_result` entity ID plus cursor for additional **already captured** rows. The immutable result set is one artifact body owned by the registry. The cursor contains no path; lookup binds session, query digest, result ID, offset and expiry in a bounded map. Two equivalent continuations normalize to the same row interval, even if their opaque tokens differ.
- Body retention is a prerequisite for advertised pagination. If it fails, still return bounded rows with `retention_unavailable`, no fake cursor. Original query's captured rows and coverage remain explicit; a retry is a new search, not silently the next page.
- Initial scan is bounded but completes its allowed scope before publishing; no background scan continues behind the model's back. If a file/byte/match budget stops the scan, report incomplete **source coverage**, even after all captured pages have been read. Narrow the scope to continue searching unscanned source; do not issue a cursor that implies otherwise.
- `matches_returned`, `matches_captured`, `matches_total` (only if exhaustive), files eligible/scanned, source bytes scanned, and skipped counts by reason are distinct. Counts of inaccessible files do not disclose secret names. Empty matches plus skipped unreadable/large files is `partial`, not exhaustive “no matches.” Binary/secret/`.git` exclusion is declared search policy; exceptions and budget omissions are separately visible.
- Per-file diversity: defer a model knob. Initial order is path/line and hard cap only; do not silently skip middle matches to show other files. Optional per-file cap later must produce explicit omitted counts/coverage and a way to narrow to that file.
- Resource target search operates on retained body only, inherits trust, source completeness, redaction and age; matching a stale page does not make it current. Search result parent references identify its source. Derivative results do not automatically count as fresh external evidence.

PROPOSED — scoped feature decisions: multiple workspace roots and include/exclude arrays are P2/DEFER initially; one path + existing glob covers baseline. Add separate before/after context only if symmetric context fails measured tasks. Match count mode is internal metadata initially, not another tool. Paging uses opaque immutable-result cursors, not unstable `skip` over a changing workspace. Binary suppression stays. Full long-line retrieval uses read byte windows; clipped match text cannot be an edit anchor.

PROPOSED — `glob` and `list_dir`: keep names and native implementation; give structured coverage immediately in Phase 1. In search phase, add optional `limit` (1–200) and `cursor` to both, using captured sorted entry lists and the same cursor owner. Preserve current glob pattern and secret filename visibility policy; content exclusion is distinct from listing names. Continue to forbid `.git` search. Directory enumeration must have an entry-work cap and cancellation, rather than reading arbitrarily many entries before clipping. No new `find` tool.

PROPOSED — backend alternatives: first benchmark old Go read-all vs bounded Go streaming against identical fixtures and full coverage labels. An optional benchmark-only `rg --json` subprocess may run when already installed, with sanitized environment and no shell; do not install it or add a runtime fallback automatically. Match regex dialect/ignore/hidden/symlink/order semantics before comparing latency. If Go meets the evaluation budget, keep it. External backend adoption requires a later ADR, dependency authorization, platform availability and a demonstrated material improvement after process startup cost.

## 17. edit design

CURRENT: exact unique `old_text` is useful and safe against ambiguity; it does not prove that unrelated parts of a file still equal what the model read earlier. The current internal full-byte recheck is also useful and must remain, but occurs before a separate truncating open (L4).

PROPOSED — KEEP exact replacement; EXTEND version binding:

```json
{
  "type":"object",
  "properties":{
    "path":{"type":"string"},
    "old_text":{"type":"string","minLength":1},
    "new_text":{"type":"string"},
    "expected_resource_id":{"type":"string","description":"Exact ID returned by a read of this file. Rejects if its complete raw version changed."}
  },
  "required":["path","old_text","new_text"],
  "additionalProperties":false
}
```

PROPOSED: no full 64-character digest needs to be copied by the model. `expected_resource_id` resolves a controller-owned complete `FileVersion`; a web page, search row, other file, redacted-only body, or partial-file digest cannot satisfy it. Resource handles are read-only; write/edit target remains a validated workspace `path`. Bind explicit expected versions to approval fingerprints too; changing expected version cannot reuse an approval of a different proposed change unnoticed.

PROPOSED — missing expected version policy, chosen to help weaker models:

1. If an explicit ID was supplied, it must resolve to the same workspace/file and a complete version; otherwise fail (`resource_unavailable`, `wrong_resource_kind`, or `snapshot_incomplete`). Never fall back to an unchecked edit on ID failure.
2. Without an explicit ID, use the latest **model-observed** full-file version for that target in the active human task if one exists. Track observation only when a successful read/result was delivered to the model, not merely when a body was captured. A full-file version may accompany a ranged read, but seen ranges remain separately recorded.
3. If no prior version was observed in this task, retain today's exact-match mode and report `precondition=exact_text_only`. Do not pretend it is version-checked. Ordinary first-time edits therefore remain possible. This compatibility path is explicit in results and measured separately.
4. If a prior observed version existed but was evicted, retain its bounded digest/path metadata for the active task or reject and require reread; do not silently downgrade. This is a bounded map (256 paths/task), not a second body store. Pin the selected version while approval is pending; release on denial/cancel/commit.
5. A new human task clears implicit observed-version selection; explicit old resource IDs remain usable only if live and matching. The model can deliberately select a new read after a conflict. A successful edit response records the new version/result; do not infer that every unrelated file changed by a shell command was observed.

PROPOSED — full-file digest granularity first: editable files are already capped at `tools.max_file_kb`, so whole-file hashing is bounded and simpler than line-hash rebasing. Reject a version mismatch even when `old_text` still matches. Formatter changes and unrelated external modifications require reread; no fuzzy retry, automatic merge, whitespace normalization, or anchor relocation. Repeated same stale edit is handled by the existing progress ledger with error code and expected/current version digests, not a new failed-edit tracker.

PROPOSED — REPLACE shared write publication primitive:

1. Acquire existing Runner serialization and a process-local workspace/path write lock if multiple runners can share a workspace in tests/future hosts. Lock scopes include version check through publish; context cancellation while waiting is typed.
2. Open the workspace root. Validate write guardrails on logical and resolved identity. For this phase reject symlink write targets and symlink parent components with an actionable `symlink_write_unsupported`; stricter write admission is preferable to silently replacing a symlink rather than its target. Existing reads may follow confined links. Detect/reject known hard-linked targets where platform metadata exposes link count; otherwise document the remaining hard-link limitation. Never claim `os.Root` enforces inode exclusivity.
3. Open a handle to the target's validated parent directory under the root and stage a random sibling using `O_CREATE|O_EXCL`, initially owner-only. Parent handle prevents later pathname retargeting from redirecting writes. Check an existing target is a regular file. Preserve existing target permission bits via the staged file handle, not a path-based chmod race; create new files with current `write_file` mode policy. Platform-specific ownership/ACL handling must fail safely rather than widen permissions.
4. Read current bytes from the confined target handle under cap, compare expected complete digest if applicable, apply exactly one replacement, and validate resulting size/UTF-8. `write_file` receives optional `expected_resource_id` for overwrites; absent it retains explicit whole-file overwrite semantics, but uses the same atomic publication primitive. Creates do not require a read token.
5. Write all bytes to the sibling, check short writes/errors, sync and close. Reopen/check target identity and digest immediately before publication. If changed, delete only the temp and return stale/conflict without touching current file.
6. Publish with parent-root `Rename`/platform replacement helper. On Windows use a tested replace-existing operation with documented access/ACL behavior, not delete-target-then-rename. If atomic replacement is unsupported or denied, fail keeping original and temp cleanup; do not fall back to `O_TRUNC`. Keep platform-specific code in `file_replace_unix.go`/`file_replace_windows.go`.
7. Best-effort directory sync where supported, clearly distinguish a post-publish durability error from a pre-publish failure. Reopen/read back within cap and verify intended digest; report `Effect=changed` or `unknown` appropriately. Generate deterministic display diff from actual old/new bytes using existing `RenderWriteDiff`; no new diff library.

PROPOSED — honest concurrency guarantee: llmtui-controlled writes serialize; a stale version detected before publish is rejected; failed staging does not corrupt the original; readers see old or new content on supported filesystems. An arbitrary external writer can still modify a file after final check or after publish, and generic rename is not compare-and-swap. Post-write verification can detect some such interference but cannot undo it safely. Document this residual, test injected races at each seam, and never claim “concurrent changes are impossible to clobber.” Stronger cooperation/locking is a future feature, not a prerequisite disguised as solved.

PROPOSED: expose a bounded edit preview through the existing approval/diff surface, computed against the selected version. Approval authorizes exact tool/path/old/new/version; after approval, revalidate source and reject changes instead of silently recomputing a different edit. No automatic command-based post-validation. The next agent action may run approved tests through `run_command`; deterministic verifier evidence then evaluates their result.

PROPOSED: generated-file protection is an advisory marker only in this phase (recognize conventional header if present); no broad filename denylist. Existing blocked paths remain authoritative. A future configured generated-file policy is separate. No-op old==new still fails validation; successful write of identical bytes is `Effect=unchanged` and not progress.

## 18. web_search/web_fetch design

CURRENT: `web.Client` owns SSRF-aware transport and extraction, not generic caching; runner owns framed result/entity adaptation. `web_search` returns snippets rather than full target pages. `Page.Truncated` combines raw and content clipping. Request URL identity and safe display URL are different concerns (L7–8).

PROPOSED — REFINE/EXTEND: retain extracted page body before preview clipping, under unchanged raw network cap initially. Do not increase network acceptance merely because disk exists. `Page` gains requested/final safe URL, observed byte counts, capture/extraction completeness, content type, acquisition time, bounded ETag/Last-Modified/cache-control metadata and explicit response status. Full raw HTML need not be retained; extraction is the canonical body, with a transformation version. `raw_read_cap` truncation means source incomplete regardless of extraction success.

PROPOSED — `web_fetch` schema keeps required `url` and adds `mode: auto|cached|refresh` with default `auto`. Existing `freshness` compatibility field, if exposed/accepted, remains an opaque requested poll epoch but is admitted by controller rules in §20; it is not a promise that data is current. Fenced URL stays in info string and optional JSON body supplies mode/epoch. Existing empty body works. No arbitrary headers/auth/body/method fields.

PROPOSED — explicit freshness table:

| Request / situation | Behavior | Result label |
| --- | --- | --- |
| `read_file`/`grep` with prior page ID | Never network; read immutable captured text | `snapshot`, original observed/validated times; no claim of current weather |
| `web_fetch(mode=cached)` with live retained page | Never network, even when stale; absent body is `cache_miss` | `acquisition=retained`; fresh/stale/unknown computed now |
| `auto`, safe entry still valid under origin freshness and local ceiling | Reuse body, no network | fresh, original observation and validation times |
| `auto`, expired/unknown entry with usable validators | Conditional GET after ordinary approval | 304: same body ID, new validation time; 200: new body if content changed |
| `auto`, no entry/validators | Normal approved GET | acquired now; freshness guarantee only if origin policy supports it |
| `refresh` | Unconditional approved GET, bypass lookup | network; body dedupe allowed after response; record fetch even if bytes identical |
| Revalidation fails | Return timeout/network error plus optional stale reference, never successful fresh data | old snapshot remains stale/unknown; explicit next action |
| Origin `no-store` | Do not create reusable page body/cache entry | immediate bounded result only, retention policy reason |
| `no-cache`, `Vary:*`, unsupported Vary, or origin expiry absent | No implicit fresh hit; validate/refetch for `auto` | unknown until validated; snapshot retrieval remains explicit unless no-store |

PROPOSED: local freshness ceiling defaults to 60 seconds, and **does not manufacture a 60-second TTL when origin provides none**. Use remaining origin max-age accounting for Date/Age, never extend beyond origin validity. `ValidatedAt` from a 304 says server representation unchanged at that time; it does not prove real-world weather correctness. A user asking for “current/latest/refresh” is taught to use `refresh`; runtime must always show source time and never relabel an old snapshot as current based on natural-language heuristics. The four-hour follow-up can use the original hourly forecast if its time range is present, described as that retrieved forecast.

PROPOSED — one body owner, metadata-only cache index: TUI session resource adapter maps a private fetch-identity digest to live entity ID + validator metadata. It holds no separate page body. Extend `WebClient` with a sibling conditional-fetch method or new options interface and retain a compatibility adapter for existing fakes; do not change provider interfaces. Transport produces data; controller/runner session adapter chooses reuse. A serial Runner already suppresses simultaneous builtin fetch execution; future parallel callers may share one in-flight request only for identical authorized GET identity and compatible cancellation. Do not add singleflight dependency now.

PROPOSED — URL identity rules:

- Lowercase scheme/host, remove fragment, normalize default port and empty path. Preserve path escaping and raw query ordering/duplicates; sorting query parameters can change server semantics. Do not strip query for identity, only safe display. Reject userinfo/credential-bearing URL rather than treating it as cacheable public content.
- Hash the canonical URL plus representation/extraction version and fixed request-header variant in memory; never persist raw query or API credentials in cache keys/debug. No user-configured authentication is introduced. Secret-looking query strings disable reusable retention; existing fetch approval still applies.
- Record requested and final target identities separately; aliases are metadata pointers to a body. Do not automatically use a redirect alias to bypass approval for a different explicit URL. Redirects retain all current checks and vetted-IP dialing.
- Conditional validators are scoped to exact requested/final representation and known variant. On redirect, drop conditional headers if target changes origin/identity; do not leak validators to an unrelated host. Bound validator lengths, reject controls, handle invalid dates safely.
- Always enforce scheme, URL validation and tool-enabled state before consulting cached metadata. Cached access does not enable `/web`; resource-only historical reads remain available with tools/entities if web is later disabled, since they perform no network.

PROPOSED — search: preserve existing query/max-results policy and DDG backend. Give each search a `collection` or `search_result` entity containing the actual returned hits in stable rank order, plus bounded `web_result` children. Max-results is not an exhaustive web match count; report returned count and provider coverage unknown. Keep search requests live by default; do not invent conditional-request semantics for DDG query results. A model can inspect an existing result set by ID, then choose an explicit URL to fetch. Zero results is successful empty evidence, not an instruction to silently relax the query.

PROPOSED: web errors retain bounded response body with error status if sensitivity policy permits, including 404/block-page evidence; this is `OutcomeFailed`, not a successful page candidate. Capturing failure evidence must not let it satisfy a successful-fetch criterion. Continue the existing bounded HTTP/1 fallback under one deadline; no new hidden retries, shell curl fallback, or extra scraper service.

## 19. MCP integration

PROPOSED — EXTEND result adaptation, KEEP connection/protocol/approval rules:

1. Extend `mcp.Result` additively with bounded typed content parts and `StructuredContent json.RawMessage`, keeping `Content`/`IsError` compatibility until all adapters migrate. In `StdioClient.CallTool`, retain recognized text, structured JSON and resource-link **metadata** under the existing 4 MiB frame cap. Non-text unsupported parts have explicit type/count/omitted-byte metadata; images are not silently fabricated as text.
2. Embedded text resources can be retained as untrusted text within caps. Remote resource links/URIs are inert metadata, never auto-opened. MCP content that contains an `ent_...` string remains text, not a registered controller reference.
3. Move preview clipping after body capture in `executeMCPCall`; preserve `IsError` as typed tool failure. Keep original `%w` context cancellation/deadline handling and concise error summary. One server result can contain both partial evidence and failure; preserve both.
4. No generic caching or replay of MCP calls, even when names sound read-only. The operation journal remains authoritative. Searching a saved MCP body is a separate local read with original server/tool/time provenance.
5. Cache-index/session adapter is not exposed to servers. Server disconnect does not turn a captured resource into a live view; existing retained body can be read as historical evidence. Config/session reset clears it. Personal-data/high-sensitivity server outputs must be eligible for an explicit non-retention source policy; default disk mode does not override that exclusion.
6. Do not change MCP `tools/list`, auto-connect behavior, schema names, exact schema disclosure, or HTTP registry mirror. Keep malformed arguments correlated instead of guessing them.

## 20. Agent-loop integration

CURRENT: controller-only entity/discovery/ask handlers run before normal progress planning (L1/L10/L15). Current result recording precedes entity registration (L10/L13). A body layer added only inside `sendToolResults` would therefore leave receipts without references and risk inconsistent repeated-call digests.

PROPOSED — REFINE ordering in one shared finalization function:

```text
accept current generation
 -> normalize typed operation outcomes + existing ActionStatus
 -> accept/publish bounded captures (or explicit retention failure)
 -> assign existing/new entity references and stable coverage/digest
 -> record receipts / observations / actual budget charges
 -> observe real completions in the existing progress ledger
 -> format bounded result and append exactly once
 -> continuation or terminal stop
```

PROPOSED: make finalization idempotent per batch slot/generation. Synthetic denied/blocked/missing results get no body publication and no success observation. Actual cancelled/timeout operations may retain partial evidence, but remain non-successful outcomes and are charged only according to existing actual-execution policy. Missing correlation remains `ActionUnknown`, never implicit “failed without effect.”

PROPOSED — canonical call and progress rules (modify existing `progressFingerprintAtRoot`, not a second detector):

| Attempt | Canonical identity / progress |
| --- | --- |
| File read lines 1–100 vs 101–200 | Same target, different normalized window => distinct useful operation |
| Same file/window/body repeatedly | Same target/window/version and content digest => repeats; changing call ID/time/resource ID is irrelevant |
| Resource read/search | Entity body identity/digest + normalized window/query/cursor interval; inherits source age |
| Equivalent search cursors with different tokens | Resolve to result-set ID/query and row interval before fingerprinting; token spelling does not evade guard |
| Same URL `auto` cache hit twice | Same source representation/coverage, no new network or validation => no new acquisition progress |
| 304 revalidation | Same content digest, explicit validation event at a controller-admitted epoch; useful freshness evidence once for that epoch |
| Refresh returning different content | New content digest => new observation; same URL remains same recovery target |
| Refresh returning identical content | New validation evidence only if epoch admitted; no content progress; repeated model token changes do not bypass bounds |
| Exact same failed edit | Stable error code + expected/current version + replacement digest => repeat |
| Reread then corrected edit | New observed version or replacement yields new operation; successful changed bytes count as progress |
| Storage failure after command success | Original execution outcome unchanged; retention warning not a failed command eligible for replay |
| Entity lookup discovers no candidates | Empty valid discovery, not criterion completion; repeated identical lookup is still bounded |

PROPOSED: stable result digest includes outcome/error code, retained body digest, relevant source version, coverage/window and actual effect; excludes human-readable diagnostics, timestamps, random IDs, framing, elapsed duration and transient cache labels. For sources without bodies hash stable canonical output under cap; do not parse display strings back into semantics. `ToolCallRecord.Succeeded` reflects operation success, including partial observation where appropriate, not storage success; acceptance proof separately checks completeness required by criterion.

PROPOSED — freshness admission and blocked reads: the present pre-execution guard cannot discover a changed result after blocking it. Add a bounded **admission check inside the existing ledger path**:

- New human task retains existing ordinary ledger reset behavior. For agent cycles, ledger remains run-scoped.
- Current-file read can receive one recheck when the file version is invalidated by a known workspace write or explicit reread request after `stale_source`. Keep per-target generation in the same progress state. Arbitrary text variation is not invalidation. External changes may be detected by a cheap confined stat probe (async, never Update I/O); stat change grants one recheck, unchanged stat is not cryptographic proof of sameness.
- Web refresh epoch is controller-owned. First actual request is epoch 0. Admit a later epoch only for explicit `mode=refresh`, expired validation policy, or the existing nonempty changed `Freshness` request, and only within polling limits. Default at most three refresh epochs per target per human task/run segment, at least five seconds between them; a new user request can establish a new task epoch. Inject clock, do not sleep the UI. If too soon, return `poll_not_due` with retry-after; no automatic model spin loop or background timer. A changed opaque token alone cannot mint unlimited epochs.
- Each epoch permits the existing bounded retry/fallback policy; cache hits do not advance it. Global tool/token/time budgets still apply. Expired quota is a controller block with a clear explanation, not an execution failure attributed to the remote site.
- `local_context` retains its intentional volatile bypass; it remains globally budgeted. `ask_user` remains separately bounded and never becomes a permission source.

PROPOSED: read/search resource operations go through regular batch planning, not the earlier entity-detail fast path. Bring repeated `get_entity_details`/`tool_search` results under the shared ledger through the same helpers while preserving standalone restrictions and schema disclosure side effects. Never count a newly allocated ID alone as `NewEvidence`. Extend `recordAgentToolResultsCount` to compare admitted acquisition/coverage/effect, while preserving useful user-answer evidence and live budgets.

PROPOSED: extend `ObservationView` with optional entity ID, digest, coverage and observed time; keep 32/512-byte defaults and omission manifest. Add additive receipt metadata for stable outcomes; no raw body in `AgentRun`. Verifier input may include a small selected excerpt plus reference/digest/time/coverage through existing proof preparation, not all retained bodies. If required evidence was evicted, mark it unavailable; never promote the summary or an ID into proof. Resource-specific recovery continues to use existing keys; a search of cached data does not “recover” a failed fresh network fetch merely because URL text matches.

PROPOSED — execution sequences:

**A. Weather follow-up:** approved `web_search` returns ranked hits; approved `web_fetch` captures hourly forecast body `ent_<id>` with source time/coverage; answer uses preview. Next human asks four-hour detail; recent entity refs or `get_entity_details(query=...)` locate that forecast; `grep(resource_id, pattern="hourly", literal=true)` and ranged resource read inspect it; answer cites source and forecast acquisition time. Assert network count remains one fetch. If requested hours were absent, retained body incomplete, or user explicitly wants updated forecast, perform a separately admitted/approved fetch.

**B. Large file:** default `read_file(path)` shows lines 1–N with `next_offset`; model copies offset into next path read beyond original prefix. Each live-file result describes its observation/version availability. If complete body is retained, model can instead page the immutable resource. Giant-line byte continuation never skips unseen bytes or claims editable complete line.

**C. Surgical edit:** `grep(path,pattern)` returns file/range; `read_file(path,offset,limit)` observes full editable-file digest and selected text; `edit_file(path,old_text,new_text,expected_resource_id)` is previewed/approved; source version rechecked and atomic replacement published; result includes effect/new version/diff; existing approved test command verifies intended behavior.

**D. Stale edit:** read version A; fixture externally changes an unrelated line to B; edit old fragment still matches but version comparison rejects before write; result says reread with `stale_source`; read B; generate/review new exact edit; succeed without discarding B's unrelated change. Error is not recovered by a read of some other file.

**E. Large log:** command emits more than inline budget but less than capture budget; bounded writer retains body; result includes preview and entity ID; grep locates sentinel near end; read exact range; command executes once. Over capture budget instead yields partial retained coverage, explicitly impossible to search the dropped tail; no automatic rerun of command.

**F. Polling:** refresh at epoch 0, simulated clock advances beyond minimum; explicit refresh admitted for epoch 1; unchanged data is new validation evidence but not changed-content evidence. Immediate epoch/token churn is blocked, while legitimate later poll within quota executes. Native/fenced forms produce identical admission behavior.

## 21. Entity/context integration

PROPOSED — ownership dictionary:

| Concept | Owner and lifetime |
| --- | --- |
| Entity | `entity.Registry`: semantic identity/provenance/preview and optional body; session by default for published reusable evidence |
| Tool observation | `agent.ObservationCache`: bounded proof excerpt/reference; agent run only, not persisted raw |
| Artifact/body | Private entity backing bytes; same quota/expiry/owner as entity, no separate discoverable ID |
| Cached raw result | Not a new subsystem; canonical retained representation plus metadata-only fetch index. Raw HTML/wire frames generally not retained |
| Model-visible reference | Typed ID + bounded source/label/window in result or ephemeral `MessageReference`; not permission |
| Long-term memory | Existing memory/project/episode systems; no automatic raw-resource ingestion |
| Session memory/history | Transcript/previews/summary; live body lifetime is separate and explicitly unavailable on restart |

PROPOSED — REFINE scope: replace `registerResultEntities`' blanket agent-run override with explicit candidate scope policy. Safe published read/search/web/MCP output intended for reuse is session-scoped even when produced during `/agent`; provenance still includes RunID/Cycle. Agent-only internal proof/temporary working records remain run-scoped. `endAgentRun` releases run pins and run-only objects, not safe session evidence. All bodies remain bounded; this is not indefinite accumulation. Excluded personal/secret/clipboard material stays excluded.

PROPOSED: `get_entity_details(query)` remains bounded metadata/preview/small-payload discovery. It must not scan every multi-MiB disk body while holding registry mutex. Label/path/URL/preview remain indexed; exact full-body search uses `grep(resource_id)`. Existing small payload keyword lookup is retained. The result identifies whether full body is available and suggests read/grep. `level=full` retains its existing bounded expansion semantics; it does not emit a whole large body. Tell the model when “full entity detail” is only a bounded semantic projection.

PROPOSED: attach resource refs to native tool messages and fenced result messages through existing ephemeral `provider.MessageReference`; extend it with safe observed-time/coverage fields if necessary, `json:"-"` runtime-only ownership. `contextmgr` keeps compact reference markers when summarizing tool groups, never full bodies. On ID eviction, markers remain historical references and resolution says unavailable. Do not maintain bodies by reparsing summary text. Recent entity prompt block should carry compact label/source/age/availability rather than repeat result preview everywhere; fit within existing `entities.max_context_tokens`.

PROPOSED: keep raw user messages and native assistant/tool groups intact. Resource references/headers are data under `untrusted.Frame`, not instructions copied from source. No framed payload is re-framed as trusted controller prose. Vision kind filters remain exact; adding resource kinds must not cause image questions to match generic tool output. Disable entities => no retained body claims; tools still work with bounded truthful partial results.

## 22. Provider/tool-call integration

CURRENT: Ollama native arguments arrive as JSON objects; OpenAI-compatible calls use argument strings and stream assembly; embedded llamart owns template grammar and routes model-specific output. GPT-OSS/Harmony metadata and Gemma-specific compatibility already exist. LM Studio, vLLM, and external llama.cpp use OpenAI-compatible transport here, not independent runners (L16–17).

PROPOSED — KEEP canonical `provider.ToolSpec`, `ToolCall`, `Message`, `Chat` contract. New result semantics are encoded once by `tools` into compact text returned through existing role/tool-ID mapping; no custom provider field is required to preserve classification. Optional ephemeral controller metadata stays off the wire except its intentional text projection.

PROPOSED — distinguish failure layers in diagnostics/evaluation:

| Layer | Example | Appropriate correction |
| --- | --- | --- |
| Tool implementation | Ranged read cannot reach target; stale write | Fix runner behavior/invariants |
| Schema/contract | Model supplies path plus resource ID; copies line numbers | Flat schema, actionable validation, concise example |
| Wire/provider | Missing call ID, incomplete streamed arguments, unsupported native tools | Existing adapter/conformance/fallback seams |
| Model syntax | Visible pseudo-call or malformed JSON | Preserve current bounded recovery; do not execute guessed repair |
| Decision quality | Refetch despite available evidence | Resource affordance/prompt/eval, not provider parser hacks |

PROPOSED — short common guidance, original wording:

> Find filenames with glob; find text with grep; read only the needed ranges. Results may include a retained resource_id. Use read_file or grep with that ID to inspect previous output without rerunning its source. Follow next_offset, next_byte_offset, or cursor exactly. Incomplete coverage does not prove absence. For edits, reuse the file's read ID; stale_source means reread before trying again. A saved web result is a dated snapshot; use web_fetch refresh when an updated observation is required.

PROPOSED: keep names/semantics identical for small and frontier models. Short descriptions carry primary action and selector; examples appear in error/continuation hints and existing prompt detail, not a second dialect. Avoid reproducing the whole result type or every optional feature in system prompt. Tiny models need runtime implicit edit-version selection and automatic bounded previews, not instructions to remember arbitrary digests. For stronger models, existing full schema/tool inspection provides detail. Do not add a model-family switch for editing without stratified evaluation.

PROPOSED: update `Specs`, `WebSpecs`, `CallsFromNative`, `Parse`/tool-body decoders, `NativeInstructions`, `Instructions`, `FormatResults`, `NativeResults` and tests together. Keep all existing built-ins core as currently provisioned; optional web/skills/personal apps stay gated; MCP remains task-disclosed above threshold. Resource arguments add no new tool. HTTP tool registry must mirror changed native specs and still return no native specs during fenced fallback. No new HTTP output endpoint.

PROPOSED — conformance matrix: mock + actual configured Ollama, LM Studio, llama.cpp server, vLLM, generic OpenAI-compatible, embedded template fixtures; native and fenced variants; sequential and multiple accepted calls; IDs absent/duplicate; streamed truncation; cancellation. Native inference remains an opt-in gate and must be reported skipped separately. Schema compilation acceptance is not evidence a model chooses the right tool.

## 23. Error model

PROPOSED — REFINE without breaking `%w` or domain result detail:

| Classification | Outcome/action | Model response / retry rule |
| --- | --- | --- |
| `invalid_arguments`, `invalid_pattern` | failed; executed validation or controller blocked according to actual stage | State bad field and valid combination; correction may retry |
| `not_found`, `range_after_eof` | failed/executed | Give known line count when exact; suggest valid offset, not guessed content |
| `unsupported_content`, `encoding_loss` | failed or partial according to usable view | Binary/invalid-text explanation; never copy-loss text into edit |
| `stale_source`, `source_changed`, `snapshot_incomplete` | failed/executed, no publish | Reread matching path; no fuzzy/automatic mutation |
| `ambiguous_match`, `match_not_found`, `no_change` | failed/executed | Include match count and unique-context guidance; no blind identical retry |
| `resource_unavailable`, `cursor_expired`, `wrong_resource_kind` | failed local lookup | No automatic source I/O; narrow discovery/reread explicit source as appropriate |
| `network`, `dns`, `http_status`, `unsupported_content_type` | failed/executed | Bounded detail, alternative source or later retry; no shell bypass |
| `cancelled` | cancelled; actual admission status | Preserve `errors.Is(context.Canceled)`; stop, never automatic retry |
| `timeout` | timeout; actual admission status | Preserve `errors.Is(context.DeadlineExceeded)`; side effects may be unknown |
| `permission_denied` | failed/denied if human, failed/executed if OS | Keep human denial distinct from filesystem permissions |
| `safety_block`, `repeat_block`, `budget_block`, `poll_not_due` | no tool success; blocked | Explain controller reason; no fake successful receipt |
| `retention_unavailable`, `capture_limit` | warning with operation outcome preserved | Preview/partial body may exist; no replay of producer |
| `outcome_unknown` | unknown action/effect as appropriate | Do not retry a potentially completed mutation; existing journal policy |

PROPOSED: `Err` remains the original wrapped cause; `ErrorInfo` is stable safe classification, not a replacement for Go error identity. Add typed sentinels/wrappers where known producers can classify accurately. Retain string-classification fallback only for legacy adapters, mark it in diagnostics and remove after each producer migration. `OutcomeUnknown` is a zero/unset-safe state, never implicit success.

PROPOSED: one formatter produces a compact header such as `[status=partial resource_id=... source_complete=false retained_complete=true next_offset=201]`, followed by an untrusted framed body. Use deterministic field order and omit irrelevant defaults. Metadata comes from controller types, not source text. Never ask models to extract an error flag from arbitrary prose when a typed outcome exists internally. Do not double-serialize the same large text in `Output` and metadata.

PROPOSED: map personal-app domain statuses into generic operation outcome while preserving original JSON status/code. `change_apply` unknown outcome stays unknown; generic retry hints must not authorize a second mutation. `get_entity_details` aggregates all-invalid -> failed, mixed -> partial, empty successful query -> OK. `tool_search` no matches -> OK; failure to disclose a schema because of budget is explicit in metadata, not tool availability success.

## 24. Security model

PROPOSED: this is an architecture security review and required regression program, not a completed independent security certification. Existing guardrail suites are mandatory and remain authoritative. No proposed convenience weakens them.

| Boundary | Required behavior and regression |
| --- | --- |
| Workspace paths | Reject `../`, absolute paths, Windows drive-relative/UNC/device/ADS escapes as applicable, separator/normalization tricks. Apply logical secret classification and resolved confinement. Test symlinked non-existing parents, swaps during open, case-insensitive aliases and Unicode names. Root-confined opened handles, regular-file checks, bounded reads. |
| Search | Root-confined actual opens, not check-then-unrestricted `ReadFile`; recursive secret skip and explicit-secret approval preserved; symlink/parent races cannot escape. No hidden external rg config or shell interpretation. |
| Edit/write | Exact content+version approval, staged publication, permission preservation, no delete-before-replace fallback, old file survives disk-full/short-write. Document residual external-writer race; distinguish no effect from unknown effect after publication. |
| Artifacts | IDs never become paths; own registry only; private root/ACL; no cross-session fallback; no resource URL auto-open. Predictable old IDs never resolve to new session content. Validate leases/expiry/pins/quota; no symlink traversal in spool or cleanup. |
| Web | Keep direct-IP SSRF checks for every connection and redirects, reserved/private/loopback/IPv4-mapped addresses, five-hop cap. Reject `file://`, userinfo; safe query display independent of exact fetch identity. Cache never bypasses enabled state/approval for network. |
| Web size | Limit decoded response bytes after automatic decompression, cap extraction output, retain response cap+1 sentinel. Huge gzip body, malicious Content-Length and UTF-8 faults must not create unbounded allocations. |
| Injection | Source text and derived search/read excerpts retain `untrusted.Frame` + `terminaltext.Sanitize` at their respective boundaries. Test copied framing markers, OSC 52, encoded MCP control strings, malicious tool names/labels. Framing is defense in depth, never permission. |
| Shell | Existing classification/path/environment/git-hardening rules unchanged; output sink does not add shell URL/resource expansion. Approval is for original command. Draining cap does not extend deadline or allow descendants to survive. |
| Secrets | No args/raw queries/credentials in debug or journal; exclude sensitive sources; best-effort redaction cannot certify output harmless. Disk backing explicit, no export by default. File raw digest must not be exposed as a guessed secret value or used as auth. |
| Provider/MCP | Existing frame/argument/count caps; malformed/truncated calls never execute approximations. Unsupported parts are explicit; links are inert; tools remain undisclosed until admitted. |
| Persistence | No raw bodies in AgentRun/history/cache/memory. Old refs unavailable on reload; sensitive transient content is not accidentally promoted by summaries. |

PROPOSED: automatic artifacts do not authorize data exfiltration. A model wanting to send retained content to any external tool still needs that tool's normal arguments and approval policy. Do not add “resolve resource reference into any arbitrary tool input” middleware; composability is explicitly limited to local read/search and file version validation.

## 25. Persistence and compatibility

PROPOSED — KEEP saved-session loading. No stored session format version bump for memory-only resources. Existing visible tool text remains readable. Ephemeral `MessageReference` fields remain excluded from history serialization. On history load/reset, clear resource adapter maps, cursors, observed-file selection, and body store; surface “previous result references unavailable in this process” in compact context protocol if needed. Do not hydrate live evidence from transcript strings.

PROPOSED: additive outcome/digest/coverage fields in persisted `ToolCallRecord` must use omitempty and load legacy zero values as unknown/legacy, not proof of new invariants. Existing `Succeeded`, `Status`, error/recovery attribution remain readable by older schema paths. Agent resume without live bodies uses durable receipts and explicit omission, then obtains new evidence through normal tools if necessary.

PROPOSED: optional temporary disk bodies are still ephemeral and not saved-history assets. This avoids a half-implemented durable manifest/resume contract. Durable body persistence is DEFER, requiring separate consent policy, retention/expiry, encryption/ACL analysis and migration ADR.

PROPOSED — compatibility period: old path-only read, exact edit, web URL and grep forms remain accepted. Bounded default read is the intentional behavioral change, rolled out behind `tools.read.default_lines` (0 temporarily selects legacy default; explicit ranges always use corrected implementation). `tools.max_file_kb` remains the edit/write ceiling and legacy result ceiling; do not reinterpret it as permission to read/write arbitrarily large files. New capture/preview settings are independent and documented. Keep compatibility switches through two stable releases after evaluation; remove only in a separately announced cleanup.

## 26. Configuration

PROPOSED: expose only user-operational bounds, reuse existing flags where possible. No provider-specific edit-mode knobs or dozens of search controls.

| Key | Default / behavior | Migration / validation |
| --- | --- | --- |
| `entities.enabled` | Existing true | Disabling disables new bodies and resource arguments at runtime; does not disable tools |
| `entities.output_storage` | `memory`; optional `session_disk`; `off` disables large body retention only | New; disk opt-in, validated platform safety; small legacy entities stay |
| `entities.max_output_bytes` | 4 MiB | New positive bounded int; <= total; hard supported ceiling 16 MiB initially |
| `entities.max_total_output_bytes` | 16 MiB memory, explicit disk budget can be raised to <=128 MiB | New; includes reserved staging, retained bodies and private index storage |
| `tools.result_preview_kb` | 8 | New; minimum sufficient for bounded metadata, max <= legacy cap; batch budget initially derived 4× |
| `tools.read.default_lines` | 200 after rollout; 0 compatibility mode | New 0–500; explicit offset/limit unaffected |
| `tools.web.cache_max_age` | `60s` | New 0–5m ceiling; 0 forces validation for `auto`, does not delete existing snapshots |

PROPOSED: all other caps initially internal named constants with tests: cursor count/expiry, scan budgets, refresh admission quota, sparse-index stride. Existing entities payload/full-expansion/context limits remain. Do not permit negative/overflowing arithmetic; validate disabled subsystems without making ordinary chat fail startup. Preserve flags > env > YAML > defaults; config show may expose bounds but not resource paths/URLs. Update `config.go`, doctor/config tests and docs in the phase that introduces each key. No config changes are made by this planning task.

## 27. Observability

CURRENT: repository has no logger and no telemetry. `provider.ToolCallDiagnostic`, `/debug`, `/context`, `/entities` and doctor already provide appropriate surfaces.

PROPOSED — EXTEND those snapshots with bounded counters/events: result outcome, source/retention/preview completeness, retained bytes, resource reuse, actual network requests vs cache hits vs 304, continuation pages, stale-version rejections, capture/storage failures, repeat blocks, and parse-layer classifications. Event records contain tool/source class, opaque entity/call identifiers where safe, counts and stable codes; never bodies, raw arguments, query strings, full paths or secrets. Hashes of sensitive args stay process-local and are not a substitute for privacy review.

PROPOSED: `/entities status/list/inspect` displays body availability/size/age/coverage and memory/disk quota. Existing `/debug last` shows cache decision and retry class. `/context` reports preview/ref/schema token contributions. No new slash command or output-serving HTTP endpoint is required. TUI adds only compact status/partial/cached/stale indicators and continuation availability; do not redesign appearance.

## 28. Evaluation framework

CURRENT: `internal/eval` has opt-in contract/conformance matrices; TUI has synthetic/full-loop live fixtures. Extend them without a new autonomous production harness. Existing model-quality and provider-format measurements are distinct and must remain so.

PROPOSED — dataset and harness:

- Add deterministic source fixtures in `internal/tools/testdata/runtime/`, `internal/web/testdata/runtime/`, and TUI synthetic scenarios. Corpus generation records seed, fixture hash, bytes/lines and expected locations. Network uses an in-memory/fake client or `internal/testutil` server; no live weather dependency.
- In `internal/eval`, add content-safe task/trial summary types. Keep full shared-kernel drivers in TUI tests so lower layers do not import TUI. Use `tools.Runner` only for package tool tests; task evaluations must exercise `startToolBatch`/approval/correlation/context, not a second implementation of orchestration.
- Pin baseline to the reviewed master SHA; when implementation begins also record then-current master SHA and diff. Compare identical fixtures/model/template/sampling/context/approval settings, with resource features off/on where safe. Never re-enable unsafe unbounded capture merely to compare production runs; historical baseline executes only bounded disposable fixtures.
- Model strata: small local (roughly 1–8B), capable local dense/MoE, and optional frontier hosted. Record actual names, quantization, backend revision, native/fenced protocol and template, not just size class. At least two local models before claiming local-model improvement. Hosted class may remain explicitly untested if not configured; it is never a prerequisite to local function.
- Begin with five paired trials/task/model for smoke evidence; release decision uses at least twenty paired trials on changed behaviors when feasible. Interleave order, isolate workspaces and caches, separate cold model load from warm inference, record seeds only if backend honors them. Report success denominators, confidence intervals and paired differences; small samples produce uncertainty, not universal rankings.

PROPOSED — deterministic task suite and independent oracles:

| Task | Fixture / success oracle | Negative/adversarial variant |
| --- | --- | --- |
| Forecast reuse | Fetch hourly table; next user asks four hours; exact rows and one network fetch | Fresh data requested, old validity expired; must refresh or report stale |
| Large page tail | Sentinel beyond preview but below capture cap; answer with exact source row by resource | Sentinel beyond capture cap; must report incomplete, not invent |
| Code search | Known unique function in nested file; grep then exact-range read, correct path | No match in scanned subset but unreadable/oversized file exists; no exhaustive absence claim |
| Surgical edit | Change one constant, byte oracle ensures unrelated bytes identical | Duplicate old fragment; reject until unique context |
| Stale edit | External unrelated mutation after read; reject first edit, preserve external bytes | Implicit expected version omitted by model must still detect observed stale file |
| Large log | Command emits sentinel near end below capture limit; execute once, search retained output | Storage failure and over-limit output; no automatic command replay |
| Pagination | Read/search successive pages beyond old cap without false repeat block | Identical repeated page eventually blocked |
| Polling | Fake clock, allowed epoch refresh, 304/unchanged vs changed content | Freshness-token churn cannot evade quotas |
| MCP output | Long text+structured JSON, `isError` and disconnect | Resource link/embedded malicious control text never executes or escapes |
| Context compaction | Force small context after fetch; discover retained ID, recover exact detail | Evicted/restarted body explicitly unavailable; no summary-as-proof |
| Approval | Exact edit preview, denial, altered expected version | No write on denial or approval mismatch; `ask_user yes` not permission |
| Domain outcome | Personal-app partial/error JSON through adapter | Generic success counters cannot classify denied/unknown mutation as completed |

PROPOSED — record these metrics per trial, not just aggregates: task success, first decision correctness, first valid tool call, first edit success, final-byte/semantic edit correctness, total provider calls, actual tool executions, controller blocks, retries by cause, duplicate content views, duplicate network requests, full-file rereads, token totals (prompt/completion/reasoning/cache where available, otherwise labeled estimates), wall time, peak context, output bytes, stale-edit misses, incorrect completeness claims, hallucinated evidence and unsafe execution count. Count failed and censored trials in denominators; do not silently drop timeouts/provider failures. Show conditional success and all-trial success separately.

PROPOSED — task report template:

```text
baseline SHA / candidate SHA / fixture hash:
model / provider / template / quantization / protocol / settings:
task / trial / warm-or-cold:
baseline: success, tool calls, retries, actual network calls, tokens, elapsed
candidate: same fields
oracle: exact answer/file check, safety checks, coverage honesty
status: completed | failed | timed_out | provider_failed | skipped
```

PROPOSED: no assistant/verifier self-report is a correctness oracle. Forecast answers compare exact fixture values/time ranges; edits compare files and appropriate tests; network counter is transport-side; stale rejection inspects preserved bytes; hallucinated evidence is a claimed source/value absent from the fixture. Automated natural-language scoring may be advisory only until calibrated. Strong-model judges cannot replace hard oracles for these tasks.

## 29. Benchmarks

PROPOSED: add stdlib Go benchmarks beside owning packages, deterministic corpus, no network or real personal apps. Use `b.ReportAllocs`, custom `ReportMetric` for exposed bytes/estimated tokens/retained bytes, and one isolated benchmark process for peak RSS. `-benchmem` measures allocations, not peak RSS; disk bytes and process peak require separate counters/platform measurement. Do not conflate filesystem warm cache with application cache.

| Benchmark name / owner | Corpus / compared paths | Metrics and purpose |
| --- | --- | --- |
| `BenchmarkReadSmall` tools | 1/16/256 KiB files; old vs bounded default | ns/op, B/op, allocs, visible bytes; quantify tiny-file overhead |
| `BenchmarkReadLarge` tools | 4/32/64 MiB text; default head | RSS independent of full file size; source cap behavior |
| `BenchmarkReadRange` tools | first/middle/late windows, CRLF/UTF8/long line | O(scanned bytes) baseline; repeated snapshot paging vs live rescan |
| `BenchmarkGrepSmallRepo` tools | 100 files / 1 MiB, zero/one/many hits | latency/allocs/coverage parity |
| `BenchmarkGrepLargeRepo` tools | 10k files / 64 MiB, exclusions/unreadable entries | scan throughput, cancellation, bounded memory |
| `BenchmarkGrepLargeResult` tools/entity | 10k hits, page first/last, match near cap | one result body, cheap later pages, no giant inline output |
| `BenchmarkEditSmall` tools | 1/64/512 KiB, unique replacement | hash+staging overhead; optional fsync cost reported separately |
| `BenchmarkEditStale` tools | same sizes, changed source | rejection latency, zero writes |
| `BenchmarkFetchCold` web/tools | fake HTTP 16 KiB/1 MiB HTML and JSON | fetch/extraction/capture vs display cost; no internet variability |
| `BenchmarkFetchCached` adapter | same body, fresh hit / 304 / expired | network counts and allocations, no body duplicate |
| `BenchmarkSearchFetchedResource` tools/entity | hourly rows near end of 1 MiB body | lookup+search latency and zero network |
| `BenchmarkCommandCapture` tools | synthetic writer 64 KiB/4 MiB/256 MiB, then controlled subprocess | memory bound, count/drain CPU, retained bytes, exit status unaffected |
| `BenchmarkResourceRead` entity/tools | memory/disk, first/last page, leases/eviction | sparse-index/retrieval cost, disk overhead |
| `BenchmarkResultFormat` tools/contextmgr | batch 1/16/128 calls | metadata/framing fits budget; no duplicate body; context growth |

PROPOSED — baseline procedure: Phase 0 tests/bench files are added without changing runtime semantics, run on pinned baseline and candidate. Collect `go test -run '^$' -bench '<suite>' -benchmem -count=10` output with OS/CPU/Go/storage metadata. Optional existing `benchstat` may summarize; do not add it to go.mod. For old/new Go search vs optional existing `rg`, assert equivalent hit sets and declared coverage before timing. If no executable alternate backend is available, report “not measured,” not “Go faster.”

PROPOSED — acceptance targets: zero safety/correlation regressions; constant capture memory with increasing command output after budget; no more than 10% unexplained median latency/allocation regression on small unchanged read/search workloads across repeated measurements (explicitly budget full digest/fsync costs separately); at least 50% less model-visible output for large-output fixtures; zero repeated network fetch for answerable snapshot follow-up; no significant task-success degradation on either local stratum; improved reuse/continuation task completion. Targets may be revised with documented baseline evidence, never silently declared achieved. Exact-edit protocol comparisons report first-attempt and eventual success separately; an anchor protocol is not adopted unless it improves local-model task success enough to justify schema and safety cost.

## 30. Migration phases

PROPOSED: each phase is a sequence of reviewable commits, not one large PR. Phases 1a/1b and 2a/2b can land separately. Phase 7 is optional disk backing and is not required to prove the weather use case. Default promotion occurs only after Phase 8 evidence; unit-level safety improvements need no model-quality claim.

| Phase | Goal / dependencies | Main changes | Compatibility and risk | Independent completion |
| --- | --- | --- | --- | --- |
| 0 | Characterize current behavior and capture baseline | Test/benchmark fixtures in tools/web/entity/TUI; extend eval report types | Runtime unchanged; dangerous baseline output stays small/disposable | Reproducible baselines, known gaps captured as current behavior or explicit regression targets |
| 1a | Typed results, before any reusable bodies | `tools/result.go`, formatter, builtin/controller/MCP/personal status mapping; agent diagnostics | Keep legacy text and Err initially; unknown metadata not success; no config required | Every accepted call has correct admission/outcome/correlation and completeness representation |
| 1b | Safe write publication, before version protocol | `file_write.go`, platform replace helpers, existing write/edit shared core | No new edit syntax; stricter unsupported symlink/hardlink writes explicit; no unsafe rollback toggle | Staging failures preserve original; confinement/permission/fault tests pass |
| 2a | Bound command memory immediately | `output_capture.go`, `runCommandContext` | Preserve timeout/process/exit behavior; retained-output feature not required | Memory plateaus at configured capture budget; overflow truthfully partial |
| 2b | Reusable entity body substrate | entity body/resource types, memory backend, new IDs, TUI finalizer/adapter, read-only resource selector | Behind `entities.output_storage`; small entity behavior preserved; no disk yet | Web/command/MCP fixture body can be published/read; pin/reset/isolation tested; receipts/digests integrated |
| 3 | Correct file range/version observations | `file_read.go`, read schema/parsers, metadata, prompt hints, context refs | Preserve path calls; bounded default gated until evaluation | Beyond-prefix ranges and giant-line continuation correct; small full-file version available |
| 4 | Search coverage and resource composition | `search.go`, captured result sets/cursors, glob/list pagination, registry search rows | Old regex/path/glob forms preserved; no external backend | Same retained body searchable; zero-match coverage honest; pages stable and not repeat-blocked |
| 5 | Read-to-edit preconditions | `file_edit.go`, Call/schema, observed-version selection, approval fingerprint, snapshot outcome | Exact-text-only compatibility when no observed version; no anchors/fuzzy | Stale earlier read rejected, corrected retry preserves unrelated change; atomic primitive already landed |
| 6 | Web retention/freshness and MCP structured parts | `web` transport metadata/conditional interface, metadata-only fetch index, MCP richer decode, refresh admission | Default auto is conservative; existing sources/approval; no generic MCP cache | Weather follow-up zero refetch, explicit refresh/revalidation correct, source truncation preserved |
| 7 | Optional bounded temporary disk storage | entity disk backend, ownership/ACL/locks/cleanup; config/doctor | Explicit opt-in; memory fallback; no history persistence | Disk-full/abandoned-session/isolation tests on all target OSes; no startup regression |
| 8 | Comparative evaluation, tune and document | Existing eval/TUI fixtures, reports, prompts, docs | Promote bounded defaults only on evidence; retain safe rollback | Definition of Done met, or untested model/platform gates explicitly open |

PROPOSED — common merge gate G for **every phase/subphase**:

```text
go test -count=1 ./...
go vet ./...
gofmt -l .                   # no output
golangci-lint run ./...       # must actually be installed/run; no silent skip claim
make build
```

PROPOSED: also run the repository's `make check` before committing and required CI vulnerability checks; record any unavailable check explicitly. For concurrency/security phases run the required race subset:

```text
go test -race -count=1 ./internal/agent/... ./internal/agentverify/... \
  ./internal/mcp/... ./internal/provider/... ./internal/tools/... ./internal/tui/...
```

PROPOSED: add `./internal/entity/...` to focused race validation because body leases/eviction introduce new shared access (do not silently change CI workflow as part of this plan). Native macOS/Linux/Windows tests are required for replacement/spool claims; compilation alone is insufficient. A phase that changes documented behavior updates the relevant docs and `CLAUDE.md` if its statements become wrong. This plan itself does not change those files.

## 31. Per-phase implementation checklists

### Phase 0 — characterization and measurements

PROPOSED — EXTEND evaluation only; rollback is removal of the new test/benchmark assets, no feature flag.

- [ ] Re-pin local/current master, record git status and baseline environment; read current AGENTS/CLAUDE and applicable package docs.
- [ ] Add fixtures for read beyond prefix, oversized first line, empty file, omitted grep matches, unrelated modification before edit, domain error with nil outer Err, and large output without uncontrolled resource consumption.
- [ ] Preserve named existing tests in `read_range_test.go`, `edit_file_test.go`, `search_test.go`, `toolloop_progress_test.go`, `mcp_error_classification_test.go`, entity and context suites; document which new regression is expected to fail until its phase.
- [ ] Add benchmark harnesses from §29 without changing tool semantics; run bounded baseline and record absent live-model results honestly.
- [ ] Extend task report metadata/oracles using `internal/eval` and TUI scripted driver; no new production runtime loop.
- [ ] Pass gate G; save baseline report in an implementation-owned evaluation artifact, with no sensitive payloads.

Done: another implementer can reproduce source-loss, coverage and version gaps and compare later behavior. No test simply asserts that the proposed implementation exists.

### Phase 1a — common result metadata

PROPOSED — REFINE; additive value types and compatibility adapter; rollback removes new rendering while leaving stable classification usable.

- [ ] Define Outcome/ErrorInfo/Coverage/Window with explicit unknown states in `tools/result.go`; retain existing Result fields and `%w` causes.
- [ ] Adapt every inventory row, including personal-app domain statuses and all-invalid entity lookups; assign synthetic admission statuses only in controller/batch owner.
- [ ] Distinguish source/capture/preview truncation in read/web/MCP/search adapters before bodies exist; use unknown totals instead of guessing.
- [ ] Add one shared formatter called by `FormatResults` and `NativeResults`; initially preserve legacy body text to isolate semantic change.
- [ ] Replace text-based agent classification where producer code can provide exact stable error codes; retain bounded legacy fallback.
- [ ] Change `progressDigest` to stable operation metadata/content (no timestamps/IDs); verify mixed blocked/fresh batches and terminal result correlation.
- [ ] Update outcome counters/receipt tests; ensure storage warning is not execution failure and partial search is not exhaustive proof.
- [ ] Run formatter benchmark, gate G and focused race subset; update tools/errors docs.

Done: success, valid empty, partial, failure, denial, controller block, timeout, cancellation and unknown effect are independently observable without parsing arbitrary source text.

### Phase 1b — confined atomic writes

PROPOSED — REPLACE only the publication primitive; no unsafe behavior toggle. Rollback means disabling unsupported mutations or reverting the commit after preserving invariants, never an automatic `O_TRUNC` fallback.

- [ ] Extract `writeFileChecked` shared implementation to `file_write.go`, preserving guardrail/size/diff behavior.
- [ ] Implement parent-root staging and tested Unix/Windows replacement helpers; reject unsafe link targets explicitly and document platform metadata limits.
- [ ] Preserve target modes/ACL policy, distinguish before-publish failure from post-publish uncertainty, and clean only owned temps.
- [ ] Inject failures at create/write/short-write/sync/close/recheck/rename/post-verify; prove original survives every pre-publish failure.
- [ ] Add deterministic symlink-parent/target swap, permission and external mutation seams; preserve `TestEditFileStaleContentGuard` and expand its actual guarantee.
- [ ] Measure small edit and sync costs separately; gate G, security/race subset, native OS replacement tests.
- [ ] Correct docs/CLAUDE wording that overstates concurrency safety in the same implementation change.

Done: writes publish complete replacements or fail safely, and documented concurrency claims match tested mechanics.

### Phase 2a — bounded capture

PROPOSED — REPLACE command `bytes.Buffer`; no flag restores unbounded capture.

- [ ] Implement bounded output writer/counters under capture reservation, with valid UTF-8 preview and optional full-stream digest.
- [ ] Wire combined stdout/stderr into it before command launch; preserve `TrackProcess`, `KillGroup`, `WaitDelay`, sanitized env and classifier.
- [ ] Drain output past retention cap without growing memory or changing command exit status; preserve context timeout/cancel causes.
- [ ] Test chunk boundaries, huge one-line output, simultaneous stream writes if writer is shared, failed start, timed-out child and descendant pipe hold.
- [ ] Benchmark synthetic 256 MiB output and controlled subprocess; show bounded retained memory and truthful partial counts.
- [ ] Pass gate G/race; update command-output documentation.

Done: memory is bounded during execution, before any formatting cap.

### Phase 2b — entity bodies and shared finalization

PROPOSED — EXTEND; feature controls `entities.enabled` and `entities.output_storage=off|memory`; rollback disables retention, preserving typed previews and bounded capture.

- [ ] Define ResourceMetadata/FileVersion/BodyLease and private memory backend; implement quota reservations, publish, open/pin, close, reset and deterministic eviction.
- [ ] Introduce random entity IDs with strict parsing; legacy IDs never alias new resources; update entity/vision tests and examples.
- [ ] Add `tool_output`/`search_result` kinds and bounded source/trust/coverage fields, adjusting closed kind validation.
- [ ] Add consumer-owned ResourceReader interface and a session/generation-bound TUI adapter; no upward imports.
- [ ] Implement `finalizeToolResults` once for normal/controller/terminal paths; record references before receipts and avoid double finalization.
- [ ] Ensure cancelled/stale batch captures are released even when results are not delivered; no new-session registry mutation from an old command.
- [ ] Add resource-only `read_file` path and minimal formatter hints; original workspace read behavior unchanged until Phase 3.
- [ ] Publish command and synthetic web/MCP captures before clipping; preserve source-specific retention exclusion and failure evidence semantics.
- [ ] Refine published read scope to session while preserving run provenance and run-only cleanup.
- [ ] Integrate resource identity into existing progress ledger immediately; ID/time changes cannot defeat repetition.
- [ ] Test quotas, all-pinned failure, reset, stale leases, cross-session IDs, no replay, no raw body persistence; run resource-read benchmarks and gate G/race.

Done: a fixture's omitted preview bytes are recoverable by ID in a later turn, while registry memory remains bounded and old-generation output cannot leak across sessions.

### Phase 3 — file reading and context references

PROPOSED — REFINE; bounded default behind `tools.read.default_lines`; rollback can restore legacy default window but retains corrected range safety.

- [x] Extract/read implement complete editable-file snapshot and streaming large-file window algorithms with cancellation and separate scan/output caps.
- [x] Add explicit path/resource selector validation to both protocols.
- [x] Add byte continuation presence tracking to both protocols.
- [x] Return exact/unknown totals, complete-line/partial-line continuation, source coordinates and encoding flags; empty default read succeeds.
- [x] Publish full raw digest only for complete eligible file versions; store rendered/retained-body digest separately.
- [x] Track model-observed versions after successful result delivery, not capture time; reserve/pin metadata needed for forthcoming edits.
- [x] Attach ephemeral MessageReferences and preserve bounded markers through `contextmgr` compaction in ordinary and agent scope.
- [x] Update original concise instructions and legacy-default tests; no per-line number insertion into edit-ready text.
- [x] Test beyond-prefix sentinel, CRLF/invalid UTF8/NUL, partial giant line, scan cap, file mutation, cancellation, resource expiry and secret-read approval.
- [x] Benchmark small/large/ranged reads, token budget and compaction; gate G/race.

Done: legal ranges past the old cap work within declared scan bounds; every omitted window has an honest next step or explicit irretrievable reason.

### Phase 4 — composable search

PROPOSED — EXTEND; no new binary; rollback disables new search options/cursors while retaining coverage truth and confined opens.

- [x] Implement shared native/fenced SearchArgs validation for literal/case/context/limit/resource/cursor with legacy regex-body compatibility.
- [x] Replace unrestricted content opens with confined regular-file operations, preserving logical/resolved secret policy and exclusions.
- [x] Stream bounded scan and materialize one canonical result-set body; separate scanned coverage from emitted page limits.
- [x] Implement bounded cursor owner bound to registry/session/query/row interval; expired cursor does not silently restart scan.
- [x] Extend glob/list_dir with bounded captured pagination; do not use body cap as an excuse for unbounded directory enumeration.
- [x] Return zero-match partial status and skipped counts, text-completeness flags, exact ranges usable by read.
- [x] Normalize continuation operation identity in existing ledger; retained-source age/coverage follows derived matches.
- [x] Test stable result pages across later source modifications, cap boundaries, unreadable files, long lines, cancellation and malicious cursor/ID input.
- [x] Benchmark old/new Go search and optional installed rg with parity; run forecast/log search task fixtures and gate G/race/security tests.

Done: search can inspect prior output and every incomplete search is visibly incomplete, including no-match results.

### Phase 5 — read/version/edit coupling

PROPOSED — EXTEND; legacy exact-text-only mode remains only when no prior task read is observed; rollback removes automatic selection but never ignores an explicit expected ID.

- [x] Add `expected_resource_id` to edit and optional overwrite schema, Call, native/fenced parsers, validation and approval fingerprints.
- [x] Resolve explicit/implicit observed versions by workspace target; wrong kind/path/partial/evicted versions fail with actionable codes.
- [x] Pin selected version through approval and release every terminal path; revalidate exact approved content/version at execution.
- [x] Compare complete raw source digest before exact match/application; retain atomic write core from Phase 1b.
- [x] Return new version/effect and record real changed files only; never classify successful same-byte overwrite as progress.
- [x] Extend receipt/recovery tests: unrelated successful read does not recover stale edit; corrected matching-resource retry can recover.
- [x] Test omitted-ID local-model path, explicit stale ID, duplicate match, formatter change, external modification and approval-time source change.
- [x] Benchmark success/reject cases; run surgical/stale-edit task evaluations, gate G/race and platform replacement checks.

Done: a model-observed A followed by external B cannot pass as a checked edit of A; exact-text-only compatibility is visible and measured.

### Phase 6 — web freshness and MCP bodies

PROPOSED — EXTEND; `cache_max_age=0` disables fresh-hit reuse while preserving explicit snapshot reads; rollback never reintroduces source-loss claims.

- [x] Extend web response/options with bounded validators, requested/final identities, acquisition and extraction/capture metadata; adapt existing fake WebClients.
- [x] Retain extracted body before preview cap; no raw/network cap increase. Implement URL userinfo rejection and safe display/identity separation.
- [x] Implement metadata-only session fetch index referencing live entity bodies; no duplicate response store.
- [x] Implement auto/cached/refresh modes, conservative max-age/Date/Age/Vary/no-store rules, conditional GET and same-body 304 handling.
- [x] Preserve redirect SSRF and approval; drop validators when target identity changes; retain HTTP/1 fallback within one deadline.
- [x] Add controller-owned refresh epoch admission and integrate it into existing ledger/budgets, with fake-clock tests and no token-churn bypass.
- [x] Capture search result-set identity/coverage without pretending returned hits exhaust the web.
- [x] Extend MCP decode/adaptation for bounded structured data and unsupported-part metadata; no URI dereference or generic call cache.
- [x] Test weather two-turn reuse, compaction reuse, stale/current questions, 304/200/timeout, no-store, oversized/decompression, MCP disconnect/error/unknown side effect.
- [x] Benchmark cold/hit/revalidated fetch and retained search; gate G/race/security tests; update web/tool/context docs.

Done: the motivating follow-up uses captured forecast evidence once, and explicit current-data requests cannot silently receive stale success.

### Phase 7 — optional temporary disk backing

PROPOSED — EXTEND storage backend only; memory default, disk explicit. Rollback selects memory/off and leaves undeletable owned files reported for safe cleanup.

- [x] Implement lazy session spool, owner-only permissions/ACL, random private filenames, marker/lock and root-confined operations.
- [x] Apply sensitivity/redaction before any disk write; reserve staging memory and disk bytes together.
- [x] Publish verified body files atomically; failed short write/disk full exposes no valid ID.
- [x] Implement lease-safe deletion and stale-owned-session sweep with lock acquisition; never sweep live sessions or arbitrary directories.
- [x] Enforce aggregate cap including abandoned spools before allocation; if reclamation fails, fail retention rather than exceed quota.
- [x] Test Windows ACL/replacement and unsupported-filesystem fallback; macOS/Linux native equivalents.
- [x] Add config/doctor quota diagnostics, disk-retrieval benchmark and cleanup failure tests; gate G/race.

Done: optional disk storage has the same resource contract as memory and does not add cross-session lookup or history persistence.

### Phase 8 — calibration, defaults and cleanup

PROPOSED — REFINE measured behavior; defaults remain reversible through documented settings.

- [x] Run all deterministic task/safety/failure matrices, preserving baseline SHA and fixture hashes.
- [x] Run configured small/capable-local model matrices in native/fenced modes; hosted comparison optional and explicitly labeled if absent. No endpoint/model was configured in this run, so both matrices are recorded as skipped/censored.
- [x] Compare complete-task tokens/calls/success, false absence claims and stale-edit misses; include failed/censored trials. Deterministic counts and the zero-denominator live result are recorded in the Phase 8 report.
- [x] Tune preview/default-read limits based on measurements without increasing accepted network/file-write caps. Measurements did not justify a change; bounded defaults remain unchanged.
- [x] Promote bounded defaults only after no safety regressions and local-model non-regression/improvement evidence; record open platform/model gates. No promotion was made without local-model evidence.
- [x] Remove obsolete internal compatibility adapters only after all producers are typed; keep documented external compatibility window. Review found the resource adapter intentional and retained it; legacy fenced compatibility remains documented.
- [x] Update architecture README/package map, tools/entity/context/web/security/config/provider-registry/evaluation docs and affected CLAUDE claims. Existing provider/web coverage and the affected package guides now link the report; no stale CLAUDE claim required correction.
- [x] Run gate G, make check, required race/vulnerability gates and targeted native-platform tests; publish content-safe benchmark/evaluation report.

Done: every mandatory Definition of Done item has evidence; sophistication alone is not completion.

## 32. Test matrix

PROPOSED: tests should assert externally meaningful outcomes and invariants. Use stdlib `testing`, table-driven cases/fakes and deterministic synchronization, not sleeps or implementation-mirroring mocks.

| Area / existing suite anchor | Required additions / oracle |
| --- | --- |
| `tools/read_range_test.go`, `readfile_unix_test.go` | Late ranges past 512 KiB; exact newline/UTF8/empty/EOF; long-line byte cursor; binary and invalid encoding; output <= budget; correct unknown totals |
| `tools/search_test.go`, new search coverage/cursor tests | Literal/regex/case/filter; sorted rows, context overlap, all skip reasons, caps, zero-match partial; malformed/replayed/expired cursor; immutable page set; resource search zero source calls |
| `tools/edit_file_test.go`, new file replacement tests | Unique/zero/multiple/no-op, expected version mismatch outside edited range, implicit observation selection, explicit token cannot downgrade, staged fault matrix and permission preservation |
| `tools/guardrails*_test.go`, security review 2026-09-13/20 tests | Absolute/parent paths, secret logical quoting, symlink parent/target swaps, command classification/git helper bypass, unknown commands still ask |
| `web/ssrf_test.go`, `fetch_test.go`, `reliability_test.go` | Public-to-private redirect, DNS vetted dialing, userinfo, reserved IPs, 304 invariants, validator redirect scope, gzip decoded cap, stale error not fresh success |
| `entity/entity_test.go`, `search_test.go`, new body tests | Atomic publish, body-only cap, total staging cap, active pins, deterministic eviction, expired IDs, random-ID failure, cross-session rejection, kind filter, no giant body scan under metadata mutex |
| `tui/toolloop_progress_test.go`, `progress_test.go` | Same content/new ID/time repeats; pages not blocked; controller-issued refresh epochs; mixed batches charge real calls only; changed source invalidation grants bounded recheck |
| `tui/toolloop_test.go`, `mcp_tools_test.go` | One result per call, duplicate/missing ID repair, stale batch capture release, cancellation at publication/approval, absent body doesn't rerun operation |
| `agent/observations_test.go`, `recovered_test.go`, proof/criteria tests | References don't become proof; partial coverage cannot prove absence; resource-specific failure recovery; omission and resumed-unavailable body |
| `tui/entity_context_test.go`, vision suites | Session reuse after agent end; run-only cleanup; visual-kind evidence never substituted; legacy ID does not alias |
| `contextmgr`/prompt tests | Tool groups intact, refs survive summary, body not duplicated, protocol budget honored, source framing survives derived excerpts, raw user unchanged |
| Provider tools/conformance tests, embedded mock runtime | Flat selector schemas serialize; correlated outcomes in all adapters; fallback instruction parity; oversized/malformed/truncated calls don't execute |
| History/store/cache tests | Old sessions/runs load; new optional fields safe; no body/raw image/clipboard in persistence; request cache includes changed tool instructions |
| Personal-app result/ledger tests | Domain denied/partial/unknown mapped accurately; exact-plan approval and no replay retained; no generic resource capture |
| Config/doctor/toolapi tests | Old YAML works; explicit disabled subsystem inert; bounds overflow rejected; active schema mirror still exact; disk safety failure does not break normal chat |

PROPOSED: require platform-native tests for semantics that differ by OS: rooted paths/device names, rename-over-existing, ACL/modes, file-handle sharing, process kill/drain, and abandoned spool locks. Where a platform cannot uphold the optional disk feature's invariant, disable that backend with a clear diagnostic rather than silently pretending POSIX behavior.

## 33. Failure-injection matrix

PROPOSED: inject dependency outcomes at narrow existing/new interfaces and synchronization hooks. Hooks belong in tests or unexported seams, never model-callable controls.

| Injected failure | Expected state/result | Forbidden outcome |
| --- | --- | --- |
| Network deadline before headers / during body | typed timeout; optional partial capture; original cached body stays stale | successful fresh cache fallback |
| DNS failure / rebinding-style mixed answers | error or vetted public direct dial under existing policy | private connection, repeated unsafe HTTP/1 attempt |
| HTTP 404/403/503 | failed outcome with bounded response evidence; only existing allowed fallback | page success solely because body exists |
| Invalid UTF-8/NUL | explicit encoding loss/binary response; raw version not confused with rendered digest | text silently becomes safe edit input |
| File permission denied | structured OS failure or partial search with skipped count | exhaustive no-match claim |
| File modified while reading | source_changed/qualified observation; no false atomic-snapshot claim | misleading current full-file token from a known unstable read |
| File changed before approval or pre-publish check | stale edit, no publish; pin released | silent recomputed unreviewed edit |
| External writer after final check | documented residual; post-check can report conflict/unknown | claim of portable CAS or unsafe auto-revert |
| Staging short write / disk full / sync fail | no valid published handle; original file intact for pre-publish write failure | partial artifact advertised as complete; original truncated |
| Artifact quota all pinned | retention warning, bounded preview, existing pins intact | memory cap exceeded or pending approval resource evicted |
| Temp cleanup failure | report bounded diagnostic; reserve actual occupied bytes until reclaimed | repeated allocation beyond cap or deletion outside owned root |
| MCP disconnect/deadline/oversized frame | typed failed/timeout/unknown as actual; journal uncertainty respected | rerun remote mutation to reconstruct output |
| Provider malformed/incomplete call | correlated validation error or existing bounded protocol recovery | guessed executable approximation |
| Parent context cancelled during grep/read/store | prompt cancellation checks, lease/temp cleanup | uncancelled worker writes into new session |
| Child process holds pipe/open output floods | process-tree cleanup + WaitDelay, bounded writer | indefinite UI wait/unbounded buffer |
| Cursor/body evicted after preview | resource/cursor unavailable; no implicit source action | silent requery presented as same page |
| Session reset while body publication finishes | stale completion rejected, staged ownership released | old-session body visible in new registry |
| Crypto RNG failure for ID | publish fails explicitly with no reused ID | sequential fallback creating cross-session aliases |
| Origin no-store / unsupported Vary | no-store retention excluded; conservative validation policy | stale personalized content silently reused |

## 34. Rollback strategy

PROPOSED: safe degradation is bounded truthful previews without retained bodies. It is never disabled guardrails, missing result correlation, unbounded capture, or unchecked publication.

- **Outcome migration:** format legacy-style text from typed fields if model prompts regress; preserve typed receipt/error behavior and domain failures.
- **Retention:** set output_storage off or entities off; keep operation outcome and explicit unretrievable truncation. Cancel/release leases at an idle session boundary, not halfway through approval.
- **Default reads:** default_lines=0 temporarily restores legacy whole-file default within original cap; explicit ranges remain fixed and bounded. Do not restore the unreachable-range bug.
- **Search:** hide optional fields/cursors only in a coordinated schema+decoder+prompt release; already-issued cursors get a clear unavailable result. Keep confined opens and coverage labels.
- **Edits:** an explicit expected ID must never be ignored on rollback. If necessary reject versioned edits and request reread/manual handling; keep atomic write safety or disable affected mutations on an unsupported platform.
- **Web:** cache_max_age=0 restores live validation for auto. Explicit historical resource reads still work. Do not treat a transport failure as a fresh cache hit.
- **Disk:** switch to memory at idle boundary, close leases, clean only own unlocked spool; leave failed cleanup reported. No automatic conversion into durable history.

PROPOSED: every rollout has a synthetic old-configuration/old-session load test. No flag-day removal of `Result.Output`, legacy path calls, native/fenced correlation, or persisted receipt fields. Rollback documentation includes exact affected schema/default and phase tests, not only “revert if broken.”

## 35. ADRs required

PROPOSED: draft these in their implementation phases under `docs/architecture/decisions/`, allocating numbers from then-current inventory. Existing ADRs 0001–0003 remain accepted.

| Proposed title | Decision to record | Alternatives rejected |
| --- | --- | --- |
| One entity identity for reusable tool evidence | Registry owns metadata + bounded private body backing; read/grep accept entity IDs | Separate artifact/resource/entity public stores; abusing observations as blob store |
| Explicit tool outcome and coverage contract | Operation outcome, admission status, source/capture/preview completeness remain separate | Error prose only; one ambiguous truncated flag; storage errors rewriting side-effect outcome |
| Ephemeral body lifetime and optional private spooling | Memory default, session-temp opt-in, no automatic resume/persistence | History blob growth, silent raw disk cache, cross-session lookup |
| Optimistic versioned edits with atomic publication | Exact replacement + observed complete raw version; explicit residual external-writer race | Mandatory hashline/fuzzy mode; incorrect compare-and-swap claim; delete-before-replace fallback |
| Conservative web freshness and conditional reuse | Metadata-only index to retained body; origin-limited validity, explicit refresh/snapshot modes | Universal TTL, stale-on-error success, generic MCP/command replay |
| Stable evidence progress with bounded refresh admission | Extend current ledger; pagination and validation epochs explicit | Second repetition detector; timestamps/random IDs as progress; unlimited caller freshness tokens |

## 36. Risks and mitigations

PROPOSED:

| Risk | Mitigation / unresolved boundary |
| --- | --- |
| Entity registry becomes a general blob framework | Only text/structured bounded bodies; private backend; one owner/index/lifecycle; no plugin URI router |
| IDs/metadata cost more tokens than they save | Compact headers, preview budget includes metadata, recent refs not repeated bodies; measure small tasks as well as large |
| Weaker models mishandle optional selectors | Flat schemas, primary examples, exact returned next hints, implicit observed file version; no per-provider contract split |
| “Immutable snapshot” overpromises live file atomicity | Raw bytes/digest represent acquisition; before/after checks detect common races, residual explicitly documented; no full version for partial scans |
| Atomic replacement changes links/modes/Windows ACLs | Explicit stricter link policy; platform-native helpers/tests; fail closed on unsupported replacement semantics |
| Snapshot check still permits final external-write race | Do not claim CAS; serialize llmtui writers, final recheck/post-verify, no unsafe auto-merge/revert |
| Generic retained output leaks secrets | Excluded sources, bounded redaction, memory default, opt-in disk with owner-only access; no claim of exhaustive secret scanning |
| Large-file scan latency exceeds local inference savings | Bounded per-call scan, cancellation, reuse retained snapshots; no mandatory total-line scan; benchmark before sparse indexing |
| Cache “fresh” mistaken for real-world correctness | Show observed/validated time and source policy; explicit current-data refresh; snapshot immutable but dated |
| Refresh gates break legitimate polling | Injected-clock fixtures, explicit small quota/min interval, separate global budgets; measure false blocks and disclose limits |
| Resources disappear during long work | Pin active leases/approval only, deterministic eviction and unavailable errors; no unbounded pinning of all transcript refs |
| Stale batch writes into replacement session | Generation-bound adapter/publication and cleanup; same stale-event contract as provider/MCP today |
| Domain outcomes diverge from generic receipts | Typed adapter mapping for every inventory row; preserve unknown/partial and existing exact-plan mutation ledger |
| Evaluation reports optimistic success | Independent byte/network/safety oracles, all-trial denominators, no automatic retry erasure, actual-model skips explicit |

## 37. Deferred/future improvements

PROPOSED — DEFER with explicit reconsideration triggers:

- Hash/range anchors or hunk/patch editing: only if versioned exact replacement has measured local-model failure modes that simpler descriptions/ranges do not fix; compare first-edit success, unintended edits and total tokens.
- Multi-range reads and multiple search roots: if fixture telemetry shows material round-trip cost; add flat bounded arrays only after protocol conformance tests.
- AST/symbol summaries: optional Go-native parser experiment with exact omitted-range metadata; no universal parser dependency or automatic summary-as-edit-text.
- Optional optimized search backend: measured Go bottleneck plus output/security parity and platform packaging decision; no mandatory `rg`.
- Sparse line indexes: only for large retained bodies with repeated late ranges; indexes private, quota-counted and bound to body digest.
- Durable resource history: separate privacy/lifetime/migration program; not a hidden extension of chat.save_history.
- Semantic find, queryable JSON selectors, remote HTTP APIs, plugin resources, images/binary bodies, checkpoint/rewind and parallel execution: separate evidence-driven proposals. Their absence does not block the motivating reusable-text use case.
- More elaborate streaming redaction: only to support larger capture budgets without raw disk staging; requires adversarial chunk-boundary testing and explicit residual secret risk.

## 38. Final implementation order

PROPOSED:

1. Phase 0 baseline/characterization, then **Phase 1a typed result metadata** as the first runtime implementation. It has the smallest blast radius and prevents subsequent resources from corrupting receipt/progress semantics.
2. Phase 1b atomic write publication and Phase 2a bounded command capture close correctness risks before large-output/model experiments.
3. Phase 2b memory-backed entity bodies + result finalization + immediate ledger/scope integration. Prove one omitted-body read by reference.
4. Phase 3 reliable file windows/versions/context refs; Phase 4 search/resource composition and cursor coverage.
5. Phase 5 exact-edit version binding on the already-safe publication primitive.
6. Phase 6 web retention/freshness/revalidation and MCP structured capture. Prove the complete weather follow-up and current-data counterexample.
7. Phase 7 optional disk only if memory-budget measurements justify it and platform access guarantees are proven.
8. Phase 8 real-model evaluation, tuned defaults and documentation. Run deterministic gates throughout; do not wait until the end to test safety or correlation.

PROPOSED — implementation handoff rules: begin with the exact named source seams and regression fixtures; keep commits scoped; do not bulk-extract the TUI or genericize all tools at once. If a future source revision contradicts a CURRENT statement, update the characterization and this plan's implementation notes before changing the decision. Any new dependency, release change or relaxed guardrail remains outside this plan's authorization.

## 39. Definition of Done

PROPOSED: program completion requires evidence for each mandatory item; skipped live/platform checks remain open gates, not passes.

- [ ] Ordinary follow-up and post-agent follow-up retrieve appropriate prior evidence without unnecessary network/command/MCP replay.
- [ ] Output beyond preview remains retrievable up to the declared retention cap; bytes beyond source/capture caps are explicitly unavailable.
- [ ] File ranges beyond the old prefix and complete/partial-line continuations are correct, bounded and cancellable.
- [ ] Every search reports result limits and source coverage; incomplete zero-match results never claim exhaustive absence.
- [ ] Explicit or implicitly observed file versions detect stale edits; exact-text-only compatibility is labeled; staging failures preserve original bytes.
- [ ] Legitimate pagination and admitted polling work; true unchanged loops and caller-token churn remain bounded by the existing ledger.
- [ ] Tool failure, controller block, denial, valid empty, partial, cancellation, timeout and unknown effect are structurally distinguishable.
- [ ] Web freshness has observed/validated times and explicit snapshot/auto/refresh semantics; stale error fallback never masquerades as fresh success.
- [ ] Existing security, approval, operation journal, visual evidence, task contract, verifier and context invariants remain intact.
- [ ] Current provider transports and native/fenced calling continue working, including correlated terminal/cancelled results.
- [ ] Old configuration, sessions and agent runs load safely; ephemeral bodies never silently become durable memory/history.
- [ ] Full `go test -count=1 ./...`, `go vet ./...`, actual lint, required race/security/build gates pass; native/platform coverage is reported accurately.
- [ ] Benchmarks show bounded memory/disk and no unexplained unacceptable regressions; alternate backend comparisons, if made, have coverage parity.
- [ ] Controlled small/capable-local task evaluations demonstrate effective use and measurable reuse/continuation improvements without safety or overall success regression; hosted results are optional and explicitly labeled.
- [ ] Required ADRs/docs/config diagnostics are complete; no second orchestration/resource/repetition subsystem was introduced.

CURRENT — planning deliverable self-review: this document identifies the Phase 1 files/types/adapters, exact source-loss points, ownership and migration order, model schemas, error/freshness/progress semantics, security limits, tests/fault injection, compatibility and rollback. It deliberately does not claim measured model improvements, reproduced OMP weather behavior, full filesystem CAS, or completed implementation. Only this plan was created by the architectural-review task.
