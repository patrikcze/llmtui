# Quality Standards

## Non-Negotiable Product Standards

- Local-first by default: no telemetry and no unconfigured network calls.
- Tools, web, RAG, and MCP are optional and visible to the user.
- Secrets are never logged, cached, printed in debug output, included in command environments, or stored casually in YAML examples.
- Config, history, cache, memory, and RAG index files use owner-only permissions where implemented.
- UX remains keyboard-first, fast, readable, resize-safe, and terminal-native.
- All features must degrade gracefully when a backend lacks native tools, token usage, model listing, vision, or reasoning support.

## Go Standards

- Keep `cmd/llmtui/main.go` minimal; put behavior under `internal/*`.
- Use `context.Context` for network calls, provider streams, subprocesses, and cancellable operations.
- Return errors with useful context. Do not panic for normal runtime failures.
- Keep interfaces near consumers unless the interface is a cross-package contract such as `provider.Provider` or `mcp.Client`.
- Avoid package cycles and broad shared state. Keep TUI state owned by `internal/tui.Model`.
- Use table-driven tests with named subtests for parser, config, provider, tool, and retrieval behavior.
- Prefer deterministic tests over sleeps; use `httptest`, temp dirs, and fake providers/clients.

## Security Checklist

Before touching tools, web, RAG, MCP, config, subprocesses, paths, or logs:

- Identify the trust boundary and attacker-controlled input.
- Check path confinement, symlink handling, output/body/file-size caps, timeouts, and cancellation.
- Confirm approval policy for mutation, code execution, external network fetch, and external tool calls.
- Confirm secrets cannot enter model context through logs, debug output, command env, config display, cache keys, RAG index, or MCP inspect output.
- Add regression tests for every guardrail touched.
- Check against `CLAUDE.md`'s "Workspace Tool Safety Invariants" section and `docs/architecture/v1-security-review.md` before assuming a guardrail is sound — both were written from a confirmed-bug audit (symlink checks skipped for not-yet-existing paths, `cmd.Dir` alone not confining a command's path arguments, subcommand allowlists not inspecting mutating flags, cache keys omitting history/composed-prompt state) and list the exact patterns to re-check whenever similar code changes.

## Testing Expectations By Area

- Config: defaults, path resolution, env mapping, changed-flag precedence, zero-value flags, redaction, invalid config.
- Providers: request JSON, stream parser, non-200 body, cancellation, usage estimation, reasoning chunks, native tools, vision payloads.
- TUI pipeline: cache hit/miss/bypass, retry, idle timeout, partial output, tool-loop continuation, native fallback.
- Tools: fenced parsing, native conversion, path resolution, symlink escape, secret detection, command classification, output caps, diffs.
- Web: URL validation, blocked IP ranges, redirects, binary refusal, Markdown cap, approval requirement.
- RAG: default-off behavior, index skip rules, ranking determinism, source line ranges, context formatting.
- MCP: inert config, validation, connect lifecycle, env redaction, tool listing/calling, shutdown.
- Prompt: section order, enabled/disabled helpers, raw-message preservation.

## Validation Commands

Use the narrowest command while iterating, then run broader checks before finalizing shared behavior:

```bash
go test ./internal/tools
go test ./internal/provider/openai ./internal/provider/ollama
go test ./internal/tui
go test ./...
go vet ./...
go test -race ./...
make check
```

`make check` runs formatting, vet, optional golangci-lint, and race tests. If `golangci-lint` is unavailable, the Makefile skips it.

## Documentation Standard

Update docs in the same change when behavior changes. Do not leave config keys, slash commands, safety rules, or provider behavior discoverable only from source. Use precise language for defaults: "off by default", "asks per URL", "inactivity timeout", "owner-only", and similar phrases must match code.
