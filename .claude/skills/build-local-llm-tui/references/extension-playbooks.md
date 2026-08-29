# Extension Playbooks

## Add A Provider

1. Implement `provider.Provider` in `internal/provider/<name>`.
2. Add `Capabilities()` if the backend can describe streaming, model listing, token usage, JSON mode, system prompts, or context window.
3. Translate `provider.Message` roles, images, tool calls, and tool results into the backend wire format.
4. Parse streaming incrementally. Emit `EventReasoning` separately from `EventDelta` when the backend supports thinking/reasoning chunks.
5. Estimate usage with `provider.EstimateTokens` and `EstimatedTokensPerImage` only when the backend omits real usage; mark `Estimated`.
6. Wire the provider in `internal/app/factory.go` and config defaults/docs.
7. Add tests with `httptest` for model listing, request shape, stream parsing, non-streaming behavior if supported, error status bodies, cancellation, usage, and tool-call transport.

## Add A Config Field

1. Add the field to the correct config struct in `internal/config/config.go`.
2. Add a built-in default in the config defaults code.
3. Bind a CLI flag only if it is important for one-off runs; otherwise prefer YAML/env only.
4. If adding a flag, bind it only when changed so unset zero values do not override env/file.
5. Add env documentation using the `LLMTUI_` prefix with dots converted to underscores.
6. Update `config init`, `config show` redaction if secret-adjacent, docs, and tests.

## Add A Slash Command

1. Read `internal/tui/commands.go` and command-specific tests.
2. Keep command parsing deterministic and lightweight.
3. Long-running work returns a `tea.Cmd`; do not block `Update`.
4. Add overlay output for inspect/list commands when the result is larger than a short notice.
5. Ensure `/debug last` and docs expose enough state to explain the command's effect.
6. Update `docs/slash-commands.md` and README command tables when user-facing.

## Add Or Change A Workspace Tool

1. Update all surfaces together: fenced parser/instructions, native `ToolSpec`, `CallsFromNative`, `Runner.Execute`, registry metadata, approval policy, compact display, and tests.
2. Use JSON Schema parameters as the source for native providers; keep names stable.
3. Classify safety explicitly: read-only, workspace write, command, network, external MCP.
4. Route execution through the runner or a dedicated safe executor. Do not execute from TUI rendering code.
5. Add guardrails before capability exposure. New mutating or network tools need approval behavior, output caps, timeout/cancellation, and docs.
6. Add tests for parser/native conversion, approval classification, success, failure, output caps, and security edge cases.

## Add Web Capability

1. Keep web off by default.
2. Search may run without approval only when it sends only the model's query.
3. Fetch must ask per URL because URLs can exfiltrate information.
4. Preserve SSRF protections: only http/https, DNS-resolved dial to vetted public IP, redirect re-check, private/local/link-local/unspecified blocked.
5. Treat fetched content as untrusted reference data in prompts.
6. Cap raw body and Markdown content. Refuse binary content.

## Extend RAG

1. Keep RAG off by default and fully local.
2. Do not introduce embeddings, external vector stores, or network calls without explicit config and docs.
3. Preserve index scoping: workspace root only, include/exclude globs, default prunes, binary skip, secret skip, symlink escape rejection, file and total budget caps.
4. Add retrieved snippets as labeled reference context through `internal/prompt`; never rewrite the raw message.
5. Surface retrieved sources in `/prompt preview`, `/debug last`, and `/rag search`.
6. Test indexing, skipping, deterministic ranking, formatting, persistence, and stale/empty cases.

## Extend MCP

1. Keep MCP off by default.
2. Declaring/enabling a server must not start it. Only `/mcp connect <server>` launches a subprocess.
3. Preserve controlled environment: safe base env plus configured env only; redact values in inspect/log/debug.
4. Keep registry transport-agnostic. Add transports behind `Client`/`ClientFactory`.
5. Connected server tools should flow into a shared capability view, but calls must still enforce per-server approval mode and timeout.
6. Test disabled/malformed config, enable/disable/connect/disconnect, clean shutdown, env redaction, JSON-RPC errors, tool-list shape, and timeout.

## Change Prompt Composition

1. Read `internal/prompt/compose.go` and `docs/prompt-composition.md`.
2. Keep section ordering stable unless the change explicitly requires order semantics.
3. Keep raw user text last and untouched.
4. Add helper context as labeled sections with clear trust boundaries.
5. Update `/prompt preview`, `/debug last`, prompt docs, and tests.

## Change The TUI Pipeline

1. `Update` must remain responsive; background work is a `tea.Cmd`.
2. Reset idle watchdog on `EventDelta` and `EventReasoning`.
3. Keep partial output on cancellation or stream failure.
4. Tool continuations do not add a user message and do not use response cache.
5. Preserve native tool fallback: if backend rejects tool declarations, retry without specs and switch to fenced protocol.
6. Avoid viewport/input interference: typed keys belong to textarea; only scroll keys and mouse wheel move transcript.

## Add Dependencies

1. Prefer the standard library and existing dependencies.
2. Add a dependency only when it removes substantial complexity or is required by a protocol.
3. Check maintenance, API size, transitive weight, license compatibility, and testability.
4. Run `go mod tidy` only when module files should change.
