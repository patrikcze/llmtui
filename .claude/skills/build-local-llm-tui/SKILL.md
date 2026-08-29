---
name: build-local-llm-tui
description: Project-specific engineering skill for improving and extending llmtui, a Go local-first terminal AI agent for local LLMs. Use when modifying providers, CLI/config, Bubble Tea TUI flows, prompt composition, agent tools, guardrails, web tools, local RAG, MCP, memory/cache/history, diagnostics, tests, or docs in this repository.
---

# llmtui Agent Engineering

Use this skill inside the `github.com/patrikcze/llmtui` repository. Treat the app as a mature local-first agent platform, not as a blank chat-client scaffold.

## Operating Rules

1. Inspect the current code before changing it. Start with `README.md`, `CLAUDE.md`, `docs/security.md`, and the package directly related to the request.
2. Keep default behavior local-first: no telemetry, no unexpected external calls, tools/RAG/MCP/web off unless explicitly enabled.
3. Preserve user control: mutating workspace actions, risky commands, web fetches, and external MCP tool calls must remain visible and approval-gated unless the user explicitly chose auto mode.
4. Keep the raw user message verbatim. Add helper context as labeled prompt sections; never rewrite the user's text.
5. Prefer small vertical changes over broad rewrites. Reuse current package boundaries and tests.
6. Keep provider code independent from TUI code, config code independent from provider code, and execution guardrails independent from UI rendering.
7. Add or update focused tests for every behavior change, especially safety, streaming, config precedence, and tool-loop behavior.
8. Update docs when user-facing commands, config, safety behavior, or extension points change.

## Reference Loading

Load only the reference needed for the task:

- Read `references/project-map.md` when you need architecture, package ownership, or request flow.
- Read `references/extension-playbooks.md` before adding or changing providers, slash commands, tools, web/RAG/MCP capabilities, prompt sections, config fields, or TUI state.
- Read `references/quality-standards.md` before final validation, security-sensitive edits, or larger refactors.

## Common Workflows

For provider work:

1. Read `internal/provider/provider.go`, the concrete provider package, and `docs/providers.md`.
2. Preserve `Provider.Chat` semantics: return a channel, emit deltas/reasoning, finish with exactly one `EventDone` or `EventError`, close the channel, and respect context cancellation.
3. Keep long streams alive. Do not add global `http.Client.Timeout`; connect timeout belongs in `internal/app/factory.go`, inactivity timeout belongs in `internal/tui/pipeline.go`.
4. Test streaming parsers, usage accounting, reasoning fallback, tool-call translation, errors, and cancellation.

For agent-tool work:

1. Read `internal/tools/*`, `internal/tui/toolloop_test.go`, `docs/security.md`, and `docs/configuration.md`.
2. Keep tool execution confined to the launch workspace. Reject absolute paths, `..`, symlink escapes, `.git` writes, key-material writes, shell-startup-file writes, and secret reads according to guardrail config. See `CLAUDE.md`'s "Workspace Tool Safety Invariants" and `docs/architecture/v1-security-review.md` for the confirmed bugs these checks exist to cover (e.g. a symlink check that only fires when `EvalSymlinks` succeeds silently passes not-yet-existing write targets; `cmd.Dir` alone does not stop an allowlisted read command from taking a path argument outside the workspace).
3. Keep native tool specs, fenced-block fallback, capability registry metadata, approval UI, compact rendering, and debug output consistent.
4. Never add a delete tool casually. If deletion is explicitly required, design approval, diff/preview, tests, and docs first.

For TUI work:

1. Read `internal/tui/app.go`, `internal/tui/pipeline.go`, `internal/tui/commands.go`, and relevant component/style files.
2. Keep Bubble Tea state transitions explicit and non-blocking. Long work returns `tea.Cmd`; never block `Update`.
3. Preserve keyboard-first behavior, resize safety, no flicker, terminal fallbacks, and typed-key isolation from viewport scrolling.
4. Test update logic where practical with package tests instead of relying only on manual TUI checks.

For config/CLI work:

1. Read `internal/config/config.go`, `internal/cli/root.go`, `docs/configuration.md`, and command-specific files.
2. Preserve precedence: changed CLI flags > `LLMTUI_*` environment > YAML config > defaults.
3. Bind only flags the user actually set, so zero values such as `0` temperature remain meaningful without clobbering file/env values.
4. Redact secrets in `config show`, `/config show`, `/debug`, MCP inspection, and logs.

## Validation

Before finishing an implementation pass, run the smallest relevant checks and broaden when shared behavior changed:

```bash
gofmt -l .        # must print nothing — this is what CI checks
go test ./...
go vet ./...
```

Run `go test -race ./...` or `make check` for changes touching concurrency, streaming, tool execution, TUI pipeline, shared state, or release-quality work. If a command cannot be run, state why and name the residual risk.

Two traps when reporting results: `make lint` prints a skip notice and exits 0 when `golangci-lint` is absent, so "check passed" can mean "lint never ran" — run `golangci-lint run ./...` directly if you need certainty. And a green `go test ./...` does not exercise `internal/provider/embedded/llamart`'s integration tests, which skip unless `LLMTUI_TEST_GGUF`/`LLMTUI_TEST_CPU`/`YZMA_LIB` are set. Never report a check as passing that you did not actually observe pass.

## Completion Report

End with:

- What changed, with file paths.
- What was verified.
- Any known limitation or follow-up that affects correctness, safety, or UX.
