---

name: build-local-llm-tui
description: Build or continue the local LLM Terminal UI Go application with premium Bubble Tea/Lip Gloss styling, provider support, streaming chat, config, usage charts, and tests.
-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

# Build Local LLM TUI Skill

You are working on a Go-based local LLM Terminal UI application.

Follow the project instructions in `CLAUDE.md`.

## Objective

Build the next useful increment of the application while keeping the repository buildable, tested, and clean.

The application must become a premium terminal-native local LLM chat client with:

* Beautiful Bubble Tea TUI
* Lip Gloss styling
* Smooth animations
* Streaming chat
* Usage charts
* YAML configuration
* Ollama support
* LM Studio support
* Generic OpenAI-compatible support
* Provider/model switching
* Session history
* Doctor diagnostics

## Working Method

Before editing code:

1. Inspect the repository structure.
2. Read `CLAUDE.md`.
3. Read existing README/docs.
4. Determine the current implementation phase.
5. Continue from the smallest valuable next step.
6. Do not rewrite the entire app unless the current structure is broken.

## Implementation Rules

* Keep changes focused.
* Prefer small, testable packages.
* Avoid global state.
* Use context-aware HTTP requests.
* Keep provider code independent from TUI code.
* Keep config code independent from provider code.
* Make UI components composable.
* Never log API keys or secrets.
* Preserve cross-platform behavior.
* Use graceful fallbacks for terminals without Unicode, Nerd Font, or TrueColor support.

## TUI Design Rules

The TUI must feel elegant and modern.

Use:

* Full-screen layout
* Soft panels
* Rounded borders where supported
* Subtle status bar
* Animated spinner
* Streaming Markdown responses
* Usage chart panel
* Token statistics panel
* Provider/model badges
* Keyboard help footer
* Modal overlays for provider/model selection

Avoid:

* Busy colors
* Excessive animations
* Flickering
* Hard-coded terminal width
* Broken resize behavior
* Emoji-only controls
* UI that requires a mouse

## Provider Rules

Implement providers through a common interface.

At minimum:

* `OpenAICompatibleProvider`
* `OllamaProvider`

The OpenAI-compatible provider should support LM Studio, Unsloth, vLLM, llama.cpp, and similar local servers by changing `base_url`.

Streaming must parse server-sent or newline-delimited streaming responses correctly. If different providers use slightly different streaming formats, isolate compatibility handling inside the provider package.

## Config Rules

Use Viper/Cobra with this precedence:

1. CLI flags
2. Environment variables
3. YAML config
4. Defaults

Default config path:

* macOS/Linux: `~/.config/llmtui/config.yaml`
* Windows: `%APPDATA%\llmtui\config.yaml`

Include `llmtui config init`, `llmtui config show`, and `llmtui config path`.

## Validation Checklist

Before stopping, run:

```bash
go fmt ./...
go test ./...
go vet ./...
```

If those fail, fix the failures or explain exactly what remains.

## Deliverable Summary

At the end of each implementation pass, summarize:

* What was created or changed
* How to run it
* How to test it
* What the next logical step is
