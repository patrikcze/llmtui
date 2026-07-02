# Prompt Composition

llmtui builds each request from labeled sections. **The raw user message is
never rewritten** — it is sent verbatim as the final user message. Helpers
are separate sections you can always inspect with `/prompt preview`.

## Sections (in order)

1. **System Prompt** — `chat.system_prompt`
2. **Template Prompt** — from the active `/template`
3. **Helper Instructions** — local-assistant guidance (`prompt.helper_text`
   overrides the default; shown in full by `/prompt composed`)
4. **Coding Guidance** — only in `coding` mode
5. **Model Helper Hints** — derived from the model profile
6. **Session Summary** — condensed older conversation, clearly marked
7. **Relevant Memory** — up to 3 keyword-matched snippets (opt-in)
8. **Recent Messages** — recent conversation, verbatim
9. **Raw User Message** — your text, untouched

## Modes

| Mode | Behavior |
| --- | --- |
| `minimal` | System prompt + conversation only |
| `balanced` | All enabled helpers (default) |
| `coding` | Balanced + coding guidance |
| `strict` | System prompt + "answer exactly as asked", no other helpers |

Set per session with `/prompt mode <m>`, per template via `prompt_mode`,
or globally via `prompt.mode` in the config. Individual helpers toggle with
`prompt.include_*` config keys.
