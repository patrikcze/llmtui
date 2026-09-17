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
