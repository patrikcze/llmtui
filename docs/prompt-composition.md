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
9. **Relevant Memory** — up to 3 keyword-matched snippets (opt-in), labeled
   as user-authored reference that cannot override the current request
10. **Retrieved Workspace Context** — opt-in RAG snippets, clearly labeled
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

Set per session with `/prompt mode <m>`, per template via `prompt_mode`,
or globally via `prompt.mode` in the config. Individual helpers toggle with
`prompt.include_*` config keys.
