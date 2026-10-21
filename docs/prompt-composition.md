# Prompt Composition

llmtui builds each request from labeled sections. **The raw user message is
never rewritten** — it is sent verbatim as the final user message. Helpers
are separate sections you can always inspect with `/prompt preview`.

## Sections (in order)

1. **System Prompt** — `chat.system_prompt` (plus tool instructions while
   `/tools` is on)
2. **Template Prompt** — from the active `/template`
3. **Active Skills** — the skills you activated (or the model loaded via
   `skill_load`), each delimited with source and path provenance;
   workspace/plugin text is explicitly untrusted and subordinate to the
   sections above ([docs](skills.md))
4. **Skill Catalog** — compact id + description list, only when
   model-driven `skill_load` is available
5. **Helper Instructions** — local-assistant guidance (`prompt.helper_text`
   overrides the default; shown in full by `/prompt composed`)
6. **Coding Guidance** — only in `coding` mode
7. **Model Helper Hints** — derived from the model profile
8. **Session Summary** — condensed older conversation, clearly marked
9. **Active Context** — one versioned, ranked, token-budgeted block containing
   eligible user/project/episode/active-run/RAG records; each body has its own
   collision-checked untrusted-content boundary
10. **Entity Context** — while tools and entities are enabled, the runtime lookup
    protocol and bounded recent references; the model can look up omitted
    entities by name/topic through `get_entity_details` before expanding IDs
11. **Recent Messages** — recent conversation, verbatim
12. **Raw User Message** — your text, untouched

**Frozen system message per turn.** The first request of a user turn (and of
each agent cycle) is composed with the layout above, and its system message is
kept for that turn. Every native-tool continuation of the turn, on any
provider, repeats that system message byte for byte. The *current* Agent
Cycle, Session Summary, memory/retrieval, Entity Context, and tool-recovery
sections are grouped into a clearly labeled, request-local user-role context
message after history. Continuations end with tool (or assistant) messages, so
this never creates two user turns in a row. On tool/yield continuations the raw
message is omitted, but this runtime context remains. It is never saved as a
user turn or used to grant permissions. Existing untrusted-content framing
remains intact. Fenced-tool requests keep the layout above.

**Runtime context on a turn's first request** (`prompt.fresh_runtime_context`,
default `auto`). With `message`, the first native-tool request of a turn also
sends its runtime sections as the labeled context message, placed immediately
**before** the verbatim raw user message, so the system message is identical
from one turn to the next and a backend keeps its cached system/tool prefix
across tasks. That context message is frozen for the turn: every continuation
repeats it verbatim right before the same raw user message (when that message
is still in history), so the whole first request stays a reusable prefix, and
the trailing context message leaves out sections it already carries unchanged.
The raw user message is still last and never rewritten. This does create two
consecutive user messages, which strict-alternation templates reject, so
`auto` enables it only for the embedded provider; `system` keeps the runtime
sections in the system message; `message` enables it everywhere. Verified to
render as two separate user turns: Gemma 4 E4B (GGUF template), gpt-oss-20b
(jinja template), and LM Studio serving both models. Measured on Gemma 4 E4B
(embedded): a follow-up agent task's first executor request evaluated 1,373
instead of 5,394 prompt tokens; single-turn tasks changed by about +1%.

The frozen system message is reused only while it still applies:
- it was composed for the same provider and model;
- its static part (every section except the runtime ones listed above) is
  unchanged. A skill loaded mid-turn, newly disclosed tools, or a native-tool
  fallback changes it, and the continuation freezes its own system message
  instead;
- using it costs no history. It still holds the turn's first runtime sections,
  so it is larger; when it would make the request compact more history or not
  fit, the continuation uses the unfrozen layout (static sections in the system
  message, runtime context after history).

The frozen message may carry stale turn-start runtime data, for example an
empty "observed so far"; the fresher data follows in the context message. This
keeps the system/tool/history token prefix reusable by a backend's prompt or KV
cache; it does not freeze references or omit fresh evidence. The response cache
key hashes every composed message, including the context message. The context
message does not repeat what the frozen system message already holds: a runtime
section that is there verbatim is left out, and a section's fixed preamble is
replaced by a pointer to the system message. Only verbatim text is removed.

Evidence is sent once. In Entity Context, an entity whose producing tool result
is in the request's history keeps only its header (id, kind, label, digest);
its preview returns once that result leaves the history, and
`get_entity_details` always returns the full record. The agent directive
applies the same rule to retained observations (see
[the agent loop](agent-loop.md)). Gemma's
provider followup reminder is applied consistently to cloned user-role messages
so adding a context turn does not rewrite an earlier prompt turn. Compaction
can still legitimately change the prefix.

During agent yield recovery, premature no-tool replies remain in the visible
transcript but are excluded from subsequent executor requests. Read recovery
targets the first missing interval, capped at the tool's 500-line maximum.
Coverage hints are rebuilt after every tool round, including reads that follow
a recovery nudge, so the next request never repeats an obsolete page hint.
New delivered coverage resets the no-progress nudge budget; duplicate reads,
unknown totals, mixed file versions, and re-reading lines that were already
covered earlier in the episode do not. The final reply and full
tool receipts remain available to verification.

The Recent Messages section in `/prompt composed` is a shortened preview.
`/debug last` labels it as such; the request estimate counts the full rendered
messages and tool schemas, and remains an estimate rather than tokenizer usage.

## Tool guidance

While `/tools` is on, the System Prompt section ends with tool instructions
(`tools.NativeInstructions` or, for the fenced protocol, `tools.Instructions`).
Native requests also carry every visible tool's JSON schema. Both are resent on
every request of a turn, so their size multiplies by the number of tool rounds:
a turn with nine tool rounds sends the same instructions and schemas ten times.
A backend with prompt caching (the embedded runtime, llama.cpp, LM Studio,
vLLM) reuses that unchanged prefix instead of recomputing it, but the usage
summary still counts it as input on each request.

What goes where:

| Text | Carries |
| --- | --- |
| Tool schema (native) | What one tool does and what each argument means: offsets, cursors, `resource_id`, `expected_resource_id`, `dry_run`. |
| Choosing tools | Which tool fits which job: `glob` for names, `grep` for contents, `directory_tree`/`list_dir` for layout, `read_file`/`read_multiple_files`, `edit_file` before `write_file`, `run_command` only for builds, tests, git and other programs, `local_context kind=time` for dates. Batch independent reads; never repeat a call already answered. |
| Rules | Path confinement, approvals and denials, `ask_user`, when to stop. |
| Web rules | Only with `/web on`: `web_research` first, `web_search` then `web_fetch`, cache refresh, citations, untrusted content. |
| Personal Mail/Calendar rules | Only with personal apps connected. The opening bullet is the active protocol's calling convention only; read flows, the two-step change flow, change-type shapes, and outcome honesty follow. |
| Compact MCP directory | Only while discovery hides MCP schemas: the directory plus the `tool_search` rules and body format. `tool_search` itself is offered only then. |

Native mode does not repeat argument mechanics in the system prompt, because
the model reads the schema with every call. The fenced protocol has no
schemas, so its instructions list each tool's block form. Every safety rule
that came from a confirmed model failure (ids copied verbatim, change types are
not tools, `outcome_unknown` is not success, calendar events are not files,
`ask_user` is not approval) keeps a regression test on its wording.

Measured with native tools, web, Mail/Calendar and entities on and no MCP
server: tool schemas went from 22.6 KB to 18.9 KB, tool and web rules from
3.9 KB to 2.8 KB, and the Mail/Calendar rules from 5.0 KB to 3.8 KB, about
1.5k fewer tokens per request.

## Modes

| Mode | Behavior |
| --- | --- |
| `minimal` | System prompt + conversation only |
| `balanced` | All enabled helpers (default) |
| `coding` | Balanced + coding guidance |
| `strict` | System prompt + "answer exactly as asked", no other helpers |

Active skills are included in **every** mode — you activated them
explicitly, so `minimal` and `strict` never drop them silently.
The Entity Context tool protocol also applies in every mode while enabled;
users refer to stored results naturally and the model handles opaque IDs.
Entity kind and source are evidence boundaries: a web_result is never image
evidence. For a prior screenshot, image, or photo, use get_entity_details with
kinds=[vision_observation]; if no such entity exists, report that the old
visual evidence is unavailable rather than reconstructing it from related
entities. Visual observations are bounded, model-derived evidence and may
contain extraction errors.

Active Context records, web results, and MCP results are wrapped in matching,
content-derived begin/end markers before they re-enter model context. This
structural framing supplements the prose warning and prevents content from
closing its own wrapper by copying a fixed delimiter. It is defense in depth,
not an authorization boundary: tool policy and approval checks remain the
authority for every action.

Set per session with `/prompt mode <m>`, per template via `prompt_mode`,
or globally via `prompt.mode` in the config. Individual helpers toggle with
`prompt.include_*` config keys.

Set `memory.retrieval.enabled: false` to use the legacy separate memory and RAG
sections. The raw-user-message and response-cache completeness invariants are
the same in both modes.

## Explicit experimental harness section

An explicitly adopted harness plus the per-session `--experimental-harness`
flag adds **Experimental Harness** immediately after System Prompt only while
an agent run is active. The application-owned guidance catalog is framed with
`untrusted.Frame`; it cannot override user/system instructions, verification,
approvals or tool policy. Model proposal explanations and experience-library
bodies never become production prompt instructions. The section participates
in `StaticSystem` and therefore cache identity and frozen-prefix compatibility.
It leaves the raw user message unchanged and is visible in `/prompt composed`
while applicable. See [harness-experiments.md](harness-experiments.md).
