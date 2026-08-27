# Context Management

Local models have small context windows. llmtui estimates token usage
(~4 chars/token, marked as estimated) and keeps the conversation inside the
budget: `window − context.reserve_response_tokens`. The estimate covers text,
structured tool calls and results, image attachments, the composed system
prompt, and native/MCP tool schemas.

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

Long native-tool turns also retain the latest real user message as their
active-turn anchor. Completed older assistant/tool groups move into the
session summary as complete units, while the newest complete tool group stays
verbatim. If that irreducible continuation still cannot fit, llmtui replaces
an oversized text-only user message with a bounded continuation anchor and
preserves the original request in the summary; image turns fail explicitly
instead of silently dropping their visual input.

If the fixed system/user prompt plus tool schemas and response reserve cannot
fit at all, llmtui stops before contacting the provider and explains which
overhead must be reduced. `/context` and `/debug last` show the estimated
breakdown.

## The summary

Built by a **heuristic summarizer** (no extra LLM call, deterministic): it
keeps lead sentences plus technically important lines — errors and failures,
explicit decisions and constraints, open work, command/test outcomes and exit
status, exact file paths, files created or modified, and code. Lines are
copied as written, so a recommendation is never rewritten into a decision and
a proposed command is never recorded as executed. The summary enters the
prompt clearly marked as "Summary of earlier conversation (not verbatim)" and
is capped at `context.summary_max_tokens`. Inspect it with `/context summary`,
rebuild with `/context rebuild`. Automatic summaries are rebuilt from the
current older-message partition and replace the previous automatic summary;
retries and tool-loop continuations therefore cannot append the same history
again and grow the prompt repeatedly.

### Volatile tool output is not durable

`local_context` results are runtime observations, not facts. The summarizer
reduces each one to a provenance marker and drops the payload:

- `kind=time` — the old timestamp is never kept as the current time; the
  marker tells the model to call `local_context(kind="time")` again.
- `kind=system` / `workspace` / `processes` / `recent_files` — kept only as a
  "point-in-time snapshot" note. An old git branch, `dirty` flag, modified
  count, process list, or recent-file ordering is never presented as current.
- `kind=clipboard` — the text is never written into a summary (or episodic /
  project / user memory, or logs). Only a confirmed outcome that later work
  acted on survives, through the messages that acted on it.

Summarized tool output is labelled "untrusted evidence" so a line inside a
tool result cannot be promoted to a system instruction. Raw model reasoning
(`Message.Reasoning`) is never summarized, and a leaked leading `<think>` or
GPT-OSS Harmony analysis block in visible content is stripped first.

## Current date and time

Local models do not know today's date. Two mechanisms cover this without
invalidating the prompt/KV cache:

- A **stable instruction** in the native tool rules tells the model to call
  `local_context(kind="time")` for the current date, time, timezone, weekday,
  or relative dates (today, tomorrow, next Monday, deadlines) and never to
  infer the date from training knowledge. It carries no timestamp, so it does
  not move the cached prompt prefix.
- The **`local_context(kind="time")` tool** returns the authoritative current
  local and UTC time on demand. `context.timezone` (empty = system zone;
  otherwise an IANA name) pins the reported zone; an invalid value is flagged
  by `llmtui doctor` and returns a clear tool error.

An automatic per-turn date anchor (`context.time_context`) is planned as a
follow-up; today the tool-driven path above is the supported mechanism.
