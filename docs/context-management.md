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

## Context controls

`/context` is an alias for `/context status`. The following inspection
commands are always available, including while a model, tool batch, or
verifier is active. They read an immutable diagnostic snapshot: they do not
alter the conversation, summary, strategy, RAG results, cache, agent state, or
pending tool/approval state, and they never invoke a model, tool, MCP server,
or summarizer.

| Command | Meaning |
| --- | --- |
| `/context` / `/context status` | Token budget, request scope, summary state, agent/verifier status, and tool lifecycle ownership |
| `/context summary` | The bounded summary applicable to the next request, labeled as session, agent start, or agent-scoped request context |
| `/context preview` | Ordered context categories and bounded token metadata; use `/prompt preview` for the exact outgoing prompt |
| `/context refresh` | Recompute the diagnostic snapshot without forcing compaction or refreshing RAG/memory retrieval |
| `/context strategy` | Show the runtime strategy and its source |

The following commands change prompt projections and therefore run only at an
idle safe boundary:

| Command | Meaning |
| --- | --- |
| `/context summarize` | Rebuild the idle session summary from eligible older turns |
| `/context compact` / `/context rebuild` / `/compact` | Backward-compatible aliases for `/context summarize` |
| `/context clear-summary` | Clear only the idle session summary |
| `/context strategy <none\|truncate\|summarize\|auto>` | Change the runtime strategy |

Mutations are rejected, never deferred or auto-cancelled, while a streamed
response, tool batch, tool approval, budget extension, `ask_user` prompt,
tool-result continuation, Harmony continuation, verifier request/retry, or
resumable agent cycle owns context. The status snapshot names the exact blocker.

## Agent and verifier scope

An ordinary chat request uses the **session summary**. An agent's first cycle
may use its bounded captured **agent start summary** and start turns. Later
cycles retain structured **verified cycle memory** and current-cycle messages
verbatim while projecting completed raw tool exchanges away. A summary derived
only for an agent request is held in bounded process memory as an
**agent-scoped request summary**; it never overwrites the ordinary session
summary and is never treated as agent state.

`AgentRun`, its cycle, acceptance-criteria ledger, execution result, verifier
verdict/evidence, retry state, tool IDs/results, approvals, `ask_user` state,
run memory, provenance, and stop reason remain authoritative structured state.
Summaries are prompt projections only and are never used to reconstruct that
state machine.

Automatic compression occurs only while a stable provider request is being
prepared: before streaming starts, or after a complete tool-result batch is
correlated and appended for a continuation. It never cuts between a tool call
and its result or runs during approval, `ask_user`, execution, verifier work,
or cancellation cleanup. The verifier remains separately assembled from
structured observable evidence in fresh, tool-free context; session summaries,
ordinary history, tool schemas, and hidden reasoning never enter its request.

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
