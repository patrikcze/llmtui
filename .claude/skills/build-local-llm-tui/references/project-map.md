# Project Map

## Product Shape

`llmtui` is a Go terminal AI agent for local and OpenAI-compatible LLMs (Go
version: see `go.mod`). It includes a Bubble Tea full-screen chat UI, streaming
providers, in-process GGUF inference, provider/model switching, prompt
composition, a bounded verified agent loop, skills/plugins, local memory and
tiered retrieval, response cache, context management, history/usage stats,
vision image paste, workspace tools, optional web tools, optional local RAG, and
MCP over stdio.

The current product is an agent platform. Do not restart it from a chat-client
skeleton.

## Package Ownership

Canonical, maintained package-by-package map — every package under `internal/`,
the dependency shape, and the duplicate-noun cross-check:

**`docs/architecture/package-map.md`**

Read it rather than a copy. It is kept in sync with the README's "Package
layout" section; a second list here would only drift. Root `CLAUDE.md` carries
the non-obvious design decisions and the conventions.

## Request Flow

1. CLI loads config in `internal/cli/root.go` using `internal/config`.
2. `internal/app.BuildActiveProvider` creates the active provider.
3. `internal/tui.New` builds the Bubble Tea model and calls `rebuildFromConfig`.
4. `rebuildFromConfig` derives history/cache/memory/tool runner/web client/RAG store/MCP registry/model profiles from config.
5. User input goes through `Model.dispatch`.
6. Cache lookup happens before prompt composition mutates context state.
7. `Model.compose` builds labeled sections: system, template, helper guidance, model hints, summary, memory, retrieved RAG context, recent messages, raw user message.
8. `Model.buildRequest` adds provider messages and native tool specs when enabled.
9. `Model.startRequest` owns retries, native-tool fallback, and the inactivity watchdog.
10. Provider emits `ChatEvent` values; TUI appends deltas, reasoning activity, usage, and tool calls.
11. Tool calls execute through `tools.Runner` after approval checks, append results, then continue the model turn without cache or new user text.

## Core Invariants

- No global HTTP timeout on provider clients. Streaming can take minutes; idle timeout is reset by token and reasoning events.
- Every provider stream must close its event channel and emit one terminal event.
- Reasoning events are progress, not visible answer text, unless fallback text is needed because the model produced no answer.
- Tool-result display diffs are UI-only and must not be sent to backends.
- Response cache must not include API keys, images, tool continuations, or mutable debug/display content.
- RAG context is reference material in a separate labeled section; it never replaces user text.
- MCP config/registry is inert until explicit connect. Declaring a server must not run a subprocess.
- Disabled or malformed optional systems must not block normal chat startup.

## User-Facing Docs To Keep In Sync

- `README.md`: capabilities, commands, usage, safety overview, development commands.
- `docs/configuration.md`: config schema and defaults.
- `docs/security.md`: local-first behavior, guardrails, storage, web/RAG/MCP safety.
- `docs/providers.md`: provider behavior, streaming, retry, reasoning, timeout semantics.
- `docs/prompt-composition.md`: prompt sections and modes.
- `docs/rag.md`, `docs/mcp.md`, `docs/cache.md`, `docs/memory.md`, `docs/context-management.md`, `docs/slash-commands.md` when those areas change.
