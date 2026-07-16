# Context Management

Local models have small context windows. llmtui estimates token usage
(~4 chars/token, marked as estimated) and keeps the conversation inside the
budget: `window − context.reserve_response_tokens`.

The window size is resolved in this order: `context.max_context_tokens`
(config) → provider capabilities → model profile → 8192 fallback.
`/context` and `/doctor` show which source is active.

## Strategies (`/context strategy <s>`)

- `none` — never touch the conversation
- `truncate` — over budget: drop oldest messages, keep the last
  `context.keep_last_messages`
- `summarize` — over budget or after `context.summarize_after_messages`
  messages: condense older messages into a session summary
- `auto` (default) — summarize long conversations, truncate short ones

Whatever the strategy, the kept window never opens on a tool result: if the
`keep_last_messages` boundary would separate a tool result from the
assistant message that requested it, the window widens backwards to include
the request, keeping the tool-call/result pair intact (a lone `tool` message
is protocol-invalid for OpenAI-compatible backends).

## The summary

Built by a **heuristic summarizer** (no extra LLM call): it keeps lead
sentences plus technically important lines — errors, file names, commands,
decisions, code. The summary enters the prompt clearly marked as
"Summary of earlier conversation (not verbatim)" and is capped at
`context.summary_max_tokens`. Inspect it with `/context summary`,
rebuild with `/context rebuild`.
