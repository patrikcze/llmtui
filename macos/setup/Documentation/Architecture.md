# llmtui Configurator architecture

The macOS app is a graphical configurator for the real `llmtui` application. llmtui remains responsible for providers, inference, tools, agent runs, MCP, RAG, memory, guardrails, and runtime behavior.

## Configuration flow

The app loads the default `~/.config/llmtui/config.yaml` into a working draft. Known settings are projected into native SwiftUI forms while the source YAML remains available for advanced editing. Save operations compare the source loaded by the app with the current file, create a `.bak` backup, write a temporary file, and atomically replace the live configuration.

The current implementation uses a projection over the source text so unknown scalar and list settings are retained. The next configuration milestone is replacing the projection parser with a YAML AST/document layer to preserve comments and all YAML node types more completely.

## UI boundaries

Configuration views should remain separate from parsing and persistence. The overview is the default screen; chat is a secondary provider verification feature. The provider, model profile, agent, entities, tools, personal-app, and miscellaneous settings views edit the shared working configuration.

## Validation and diagnostics

YAML syntax and semantic validation should ultimately be delegated to the real llmtui binary where a machine-readable command is available. The current Go CLI exposes `config path`, `config show`, and `doctor`; the Swift adapter should query those commands rather than duplicate llmtui runtime rules.

## Rendering

Chat rendering is presentation-only. Markdown, code highlighting, math, and Mermaid must not change the llmtui runtime or provider implementation. Rich rendering should be isolated from the configuration subsystem and must gracefully fall back to source text for malformed input.

## Testing

Configuration parsing, preservation, safe writes, external-change detection, provider projections, and validation adapters are the highest-value tests. UI tests should cover the workflow from Overview through edit, validation, and save.
