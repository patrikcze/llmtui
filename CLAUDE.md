# Project: Local LLM Terminal UI in Go

You are building a professional Golang Terminal UI application for chatting with local and OpenAI-compatible LLM backends.

The application must feel premium, smooth, animated, keyboard-first, and visually polished. It should be inspired by modern terminal coding assistants such as Claude Code, but it must not copy proprietary branding, logos, exact color palette, wording, or protected UI assets. Create an original “Claude-Code-inspired” aesthetic: calm, elegant, minimal, terminal-native, fast, and highly readable.

## Primary Goal

Build a full-screen TUI CLI application in Go that allows the user to chat with local LLMs hosted by:

* Ollama
* LM Studio
* Unsloth / vLLM / llama.cpp or any OpenAI-compatible server
* Generic OpenAI-compatible endpoint
* Future providers through clean provider interfaces

The app must support interactive chat, streaming tokens, provider/model switching, YAML configuration, command-line flags, usage charts, session history, and a beautiful terminal dashboard.

## Technical Stack

Use Go.

Use these libraries unless there is a strong reason not to:

* `github.com/spf13/cobra` for CLI commands
* `github.com/spf13/viper` for YAML config, environment variables, and defaults
* `github.com/charmbracelet/bubbletea` for the TUI runtime
* `github.com/charmbracelet/bubbles` for reusable TUI components
* `github.com/charmbracelet/lipgloss` for layout, borders, colors, spacing, and typography
* `github.com/charmbracelet/glamour` for Markdown rendering inside chat responses
* `github.com/charmbracelet/harmonica` or another suitable animation approach for smooth transitions
* A Bubble Tea compatible charting/sparkline library such as `ntcharts` if suitable
* Standard Go `net/http` with streaming support for provider clients
* `httptest` for provider tests

Before adding a dependency, check if it is maintained, idiomatic, and appropriate.

## Non-Negotiable UX Requirements

The terminal UI is the most important part of this project.

The TUI must include:

* Full-screen chat interface
* Smooth animated loading/thinking indicator
* Streaming token rendering
* Markdown rendering for assistant responses
* Syntax-highlighted code blocks if feasible
* Usage panel with token counts, elapsed time, tokens/sec, context usage, and session totals
* Visual chart showing usage over time, similar to a terminal-native graph
* Provider status indicator
* Current model indicator
* Keyboard help footer
* Command palette or quick action overlay
* Configurable themes
* Graceful fallback for non-TrueColor terminals
* Graceful fallback when Nerd Font symbols are unavailable
* Mouse support only as enhancement, never as a requirement
* Fast startup
* No flickering
* No broken layout on resize
* Clean behavior over SSH and in standard terminals

The design should use:

* Rounded borders when supported
* Subtle panels
* Minimal but premium icons
* Unicode block charts and sparklines
* Carefully aligned columns
* Soft contrast
* Clear state changes
* Smooth transitions, not noisy animations
* A compact but readable layout

The app cannot force a terminal font. Document recommended terminal fonts instead, such as JetBrains Mono Nerd Font, MesloLGS NF, Berkeley Mono, or SF Mono where available. All UI must still work without Nerd Fonts.

## CLI Requirements

The binary name should be short and memorable. Use `llmtui` unless a better name is chosen.

Required commands:

```bash
llmtui chat
llmtui chat --provider ollama --model qwen3
llmtui models
llmtui providers
llmtui provider switch ollama
llmtui config init
llmtui config show
llmtui config path
llmtui doctor
llmtui version
```

Required flags:

```bash
--config
--provider
--model
--base-url
--api-key
--temperature
--top-p
--max-tokens
--system
--theme
--no-stream
--debug
```

Configuration precedence must be:

1. CLI flags
2. Environment variables
3. YAML config file
4. Built-in defaults

Environment variable prefix:

```text
LLMTUI_
```

Examples:

```bash
LLMTUI_PROVIDER=ollama
LLMTUI_MODEL=qwen3
LLMTUI_BASE_URL=http://localhost:11434
LLMTUI_API_KEY=...
```

## Config File

Default config locations:

* macOS/Linux: `~/.config/llmtui/config.yaml`
* Windows: `%APPDATA%\llmtui\config.yaml`

Example config:

```yaml
default_provider: ollama
default_model: qwen3

providers:
  ollama:
    type: ollama
    base_url: http://localhost:11434
    api_key: ""
    default_model: qwen3

  lmstudio:
    type: openai_compatible
    base_url: http://localhost:1234/v1
    api_key: lm-studio
    default_model: local-model

  unsloth:
    type: openai_compatible
    base_url: http://localhost:8000/v1
    api_key: not-needed
    default_model: local-model

  openai_compatible:
    type: openai_compatible
    base_url: http://localhost:8080/v1
    api_key: ""
    default_model: local-model

chat:
  system_prompt: "You are a helpful local assistant."
  temperature: 0.7
  top_p: 0.9
  max_tokens: 4096
  stream: true
  save_history: true
  history_dir: "~/.local/share/llmtui/history"

ui:
  theme: claude_inspired
  use_nerd_font: auto
  animations: true
  show_usage_chart: true
  show_token_stats: true
  markdown: true
  compact_mode: false

privacy:
  local_first: true
  redact_api_keys_in_logs: true
  store_prompts: true
```

## Provider Architecture

Create a clean provider abstraction:

```go
type Provider interface {
    Name() string
    ListModels(ctx context.Context) ([]ModelInfo, error)
    Chat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
    HealthCheck(ctx context.Context) error
}
```

Implement at least:

* `OllamaProvider`
* `OpenAICompatibleProvider`

Ollama support:

* Native Ollama API where appropriate
* OpenAI-compatible mode where useful
* Default base URL: `http://localhost:11434`

LM Studio support:

* Use OpenAI-compatible endpoints
* Default base URL: `http://localhost:1234/v1`

Unsloth/vLLM/llama.cpp support:

* Treat as OpenAI-compatible unless a dedicated API is later implemented

Streaming must be implemented properly. Do not fake streaming.

## TUI Architecture

Use Bubble Tea idioms correctly.

Suggested package structure:

```text
cmd/llmtui/
internal/app/
internal/cli/
internal/config/
internal/provider/
internal/provider/ollama/
internal/provider/openai/
internal/tui/
internal/tui/components/
internal/tui/styles/
internal/tui/theme/
internal/tui/charts/
internal/chat/
internal/history/
internal/diagnostics/
```

TUI screens:

* Chat screen
* Model picker
* Provider picker
* Config editor/read-only viewer
* Help screen
* Doctor/diagnostics screen

Components:

* Chat viewport
* Input box
* Status bar
* Usage chart
* Token meter
* Provider badge
* Model badge
* Spinner/thinking animation
* Error toast
* Modal overlay
* Help footer

Keyboard shortcuts:

```text
Ctrl+C        Quit
Ctrl+S        Save session
Ctrl+L        Clear screen
Ctrl+P        Provider picker
Ctrl+M        Model picker
Ctrl+K        Command palette
Ctrl+H / ?    Help
Esc           Close modal
Enter         Send message
Shift+Enter   Newline if terminal supports it
```

## Usage Chart Requirements

The app must show a terminal-native usage chart.

Track:

* Prompt tokens
* Completion tokens
* Total tokens
* Tokens/sec
* Request duration
* Context window usage if known
* Session total tokens
* Rolling token usage per message

The chart should be rendered as Unicode bars, blocks, sparklines, or an `ntcharts` graph. It should look like a polished full terminal graphic, not a simple table.

If the provider does not return token usage, estimate usage approximately and mark it clearly as estimated.

## Privacy and Security

This app is local-first.

Requirements:

* Never log API keys
* Redact secrets in debug output
* Do not send telemetry
* Do not call external services unless explicitly configured
* Make history saving configurable
* Store config with reasonable file permissions
* Do not store provider secrets in shell history examples
* Support `api_key_env` so users can reference environment variables instead of writing secrets into YAML

Example:

```yaml
providers:
  openai_compatible:
    api_key_env: LLMTUI_API_KEY
```

## Testing Requirements

Implement useful tests from the beginning.

Required tests:

* Config loading and precedence
* Provider request creation
* Provider streaming parser
* Provider error handling
* TUI model update logic where practical
* Usage statistics calculations
* History save/load
* Doctor command checks

Use `httptest` for mock providers.

Do not only create happy-path tests.

## Quality Requirements

Code must be:

* Idiomatic Go
* Modular
* Readable
* Properly error-handled
* Context-aware
* Testable
* Cross-platform
* Race-safe where applicable

Run before considering work complete:

```bash
go fmt ./...
go test ./...
go vet ./...
```

If a linter config is added, also run it.

## Documentation Requirements

Create:

* `README.md`
* `docs/configuration.md`
* `docs/providers.md`
* `docs/tui-design.md`
* `docs/security.md`

README must include:

* Screenshots or terminal mockups if possible
* Installation
* Quick start with Ollama
* Quick start with LM Studio
* Quick start with OpenAI-compatible endpoint
* Config examples
* Keyboard shortcuts
* Troubleshooting

## Implementation Style

Work incrementally.

Do not create a huge untested monolith.

Suggested phases:

1. Project skeleton, Cobra CLI, config loading
2. Provider interfaces and mock provider
3. OpenAI-compatible provider with streaming
4. Ollama provider
5. Basic Bubble Tea chat UI
6. Premium styling with Lip Gloss
7. Usage chart and token stats
8. Provider/model picker
9. Session history
10. Doctor command
11. Polish, tests, README

At every phase, keep the app buildable and runnable.

## Important Constraint

The goal is not to clone Claude Code. The goal is to build an original local LLM TUI with similar quality, elegance, smoothness, and terminal-native polish.

