# llmtui

A fast, keyboard-first terminal UI for chatting with **local LLMs** — Ollama,
LM Studio, vLLM, llama.cpp, Unsloth, or any OpenAI-compatible server.

```text
╭──────────────────────────────────────────────────────────────╮
│  you                                                         │
│  Explain goroutines in one paragraph.                        │
│                                                              │
│  assistant                                                   │
│  Goroutines are lightweight threads managed by the Go        │
│  runtime…                                                    │
╰──────────────────────────────────────────────────────────────╯
╭──────────────────────────────────────────────────────────────╮
│ ▁▂▃▅▇█▅▃  usage  prompt 412 · reply 887 · total 1299         │
╰──────────────────────────────────────────────────────────────╯
╭──────────────────────────────────────────────────────────────╮
│ ┃ Ask anything… (Enter to send, Ctrl+C to quit)              │
╰──────────────────────────────────────────────────────────────╯
● online │ provider ollama │ model qwen3 │ session 1299 tok │ speed 42.3 tok/s
enter send · ctrl+l clear · ctrl+c quit
```

Local-first: no telemetry, no external calls unless you configure them,
API keys never logged.

## Install

Requires Go 1.24+.

```bash
git clone <this-repo>
cd llm_chat
go build -o llmtui ./cmd/llmtui
```

## Quick start

If no backend is running, `llmtui chat` automatically falls back to a built-in
**offline demo provider**, so you can try the UI immediately:

```bash
./llmtui chat
```

### With Ollama

```bash
ollama serve                # if not already running
ollama pull qwen3
./llmtui chat --provider ollama --model qwen3
```

### With LM Studio

Start LM Studio's local server (default `http://localhost:1234/v1`), then:

```bash
./llmtui chat --provider lmstudio --model <loaded-model-id>
```

### With any OpenAI-compatible server (vLLM, llama.cpp, Unsloth, …)

```bash
./llmtui chat --provider openai_compatible --base-url http://localhost:8000/v1 --model <model-id>
```

## Configuration

```bash
./llmtui config init    # write a starter config
./llmtui config path    # show where it lives
./llmtui config show    # print effective merged config (secrets redacted)
```

Config lives at `~/.config/llmtui/config.yaml` (macOS/Linux) or
`%APPDATA%\llmtui\config.yaml` (Windows). Precedence, highest first:

1. CLI flags (`--provider`, `--model`, `--base-url`, `--api-key`, …)
2. Environment variables (`LLMTUI_PROVIDER`, `LLMTUI_MODEL`, `LLMTUI_BASE_URL`, `LLMTUI_API_KEY`, …)
3. YAML config file
4. Built-in defaults

Keep secrets out of YAML by referencing an environment variable:

```yaml
providers:
  openai_compatible:
    api_key_env: LLMTUI_API_KEY
```

## Commands

| Command | Description |
| --- | --- |
| `llmtui chat` | Interactive full-screen chat |
| `llmtui models` | List models on the active provider |
| `llmtui providers` | List configured providers and their status |
| `llmtui config init / show / path` | Manage configuration |
| `llmtui doctor` | Diagnose config and backend connectivity |
| `llmtui version` | Version info |

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| `Enter` | Send message |
| `Ctrl+L` | Clear conversation |
| `Ctrl+C` | Quit |

More (provider/model pickers, command palette, help overlay) are planned.

## Troubleshooting

Run `llmtui doctor` first — it checks the config file, the active
provider/model resolution, and pings every configured backend.

- **"offline demo mode" banner in chat** — no backend answered the health
  check; start Ollama/LM Studio or fix `base_url`, then restart chat.
- **Fonts/symbols look wrong** — llmtui works with any monospace font, but
  looks best with a Nerd Font such as JetBrains Mono Nerd Font or MesloLGS NF.

## Development

```bash
go fmt ./... && go vet ./... && go test ./...
```

Package layout:

```text
cmd/llmtui/               entry point
internal/cli/             Cobra command tree
internal/config/          Viper config loading + precedence
internal/provider/        Provider interface + shared types
internal/provider/mock/   offline demo provider
internal/provider/ollama/ native Ollama API (NDJSON streaming)
internal/provider/openai/ OpenAI-compatible API (SSE streaming)
internal/app/             config → provider factory
internal/chat/            session state + usage statistics
internal/tui/             Bubble Tea chat screen
internal/tui/components/  status bar, usage panel, sparkline
internal/tui/styles/      Lip Gloss theme
```
