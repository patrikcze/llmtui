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
API keys never logged — see [docs/security.md](docs/security.md).

## Install

Requires Go 1.26+.

```bash
git clone https://github.com/patrikcze/llmtui.git
cd llmtui
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

The full reference for every section lives in
[docs/configuration.md](docs/configuration.md).

## Commands

| Command | Description |
| --- | --- |
| `llmtui chat` | Interactive full-screen chat |
| `llmtui models` | List models on the active provider |
| `llmtui providers` | List configured providers and their status |
| `llmtui config init / show / path` | Manage configuration |
| `llmtui doctor` | Diagnose config and backend connectivity |
| `llmtui history` | List saved chat sessions |
| `llmtui stats` | All-time token usage per day with sparkline |
| `llmtui version` | Version info |

## Keyboard shortcuts

| Key | Action |
| --- | --- |
| `Enter` | Send message |
| `Shift+Enter` | Insert a newline — works out of the box in iTerm2, VS Code, WezTerm, Ghostty, Alacritty (see note below) |
| `\` + `Enter` | Insert a newline — trailing backslash continues the line, works in **every** terminal |
| `Ctrl+J` | Insert a newline (also works everywhere) |
| `Ctrl+S` | Save the session to the history directory |
| `Ctrl+Y` | Copy the last assistant reply to the clipboard (raw Markdown) |
| `Ctrl+O` | Toggle text-selection mode (releases the mouse so your terminal can select/copy; toggles back to wheel scrolling) |
| `Ctrl+V` | Paste an image from the clipboard (vision models) |
| `Ctrl+X` | Remove the last pasted image |
| `Esc` | Stop the current generation (keeps partial reply) |
| `PgUp` / `PgDn` | Scroll the chat (mouse wheel works too); typing never scrolls it |
| `Ctrl+L` | Clear conversation |
| `Ctrl+C` `Ctrl+C` | Quit (press twice within 2 s). The first press stops generation or clears the input; quitting auto-saves the session |

**Shift+Enter note:** legacy terminal input sends the exact same byte for
Enter and Shift+Enter, so historically TUIs couldn't tell them apart. llmtui
enables the `modifyOtherKeys` keyboard protocol at startup, which makes
Shift+Enter (and Ctrl+Enter) report distinctly in terminals that support it:
**iTerm2, VS Code, WezTerm, Ghostty, Alacritty, xterm**. No configuration
needed there.

Two terminals need a different route:

- **macOS Terminal.app** supports no keyboard protocol at all — use
  `\` + `Enter` or `Ctrl+J`, or enable *Settings → Profiles → Keyboard →
  Use Option as Meta key* to make `Option+Enter` work.
- **Kitty** uses only its own protocol — map it yourself:
  `map shift+enter send_text all \x1b[27;2;13~` in `kitty.conf`.

(`Cmd+Enter` can never work: macOS terminals consume Cmd shortcuts
themselves and never forward them to the running program.)

## Slash commands

Type `/` in the chat input to open a live suggestion popup (`↑`/`↓` to
navigate, `Tab` to complete, `Enter` to run, `Esc` to dismiss):

| Command | Description |
| --- | --- |
| `/help` | Show all keyboard shortcuts and commands (scrollable overlay) |
| `/copy` | Copy the last reply to the clipboard |
| `/clear` | Clear the conversation |
| `/models` | List models on the current provider |
| `/model <id>` | Switch to a different model |
| `/providers` | List configured providers |
| `/provider <name>` | Switch provider (adopts its default model) |
| `/stats` | Session + all-time token usage, durations, and tok/s |
| `/usage` | Usage dashboard: tokens-per-day chart, activity heatmap, per-model breakdown, streaks |
| `/save` | Save this session to the history directory |
| `/history` | List saved sessions |
| `/quit` | Save session and exit |

Overlays close with `Esc`, `Enter`, or `q` and scroll with `↑`/`↓`/`PgUp`/`PgDn`.

The full command set is documented in [docs/slash-commands.md](docs/slash-commands.md).
Local-LLM experience helpers:

| Command | Description |
| --- | --- |
| `/cache` | Local response cache — repeated prompts answer instantly ([docs](docs/cache.md)) |
| `/profile` | Model profiles tune temperature, context window, prompt style per model family |
| `/prompt preview` | See exactly what will be sent — the raw message is never rewritten ([docs](docs/prompt-composition.md)) |
| `/context` | Context-window management with heuristic summaries ([docs](docs/context-management.md)) |
| `/memory` | Opt-in local preference snippets ([docs](docs/memory.md)) |
| `/tools` | Opt-in agent mode — the model can create/read files and run commands in your current directory, with y/n approval |
| `/web` | Opt-in web tools — the model can search the web (DuckDuckGo, no API key) and fetch pages as Markdown; fetches ask per URL |
| `/rag` | Opt-in local workspace retrieval — index files and add keyword-matched snippets as labeled reference context; fully local, no embeddings ([docs](docs/rag.md)) |
| `/template` | Reusable conversation templates from the config |
| `/doctor` | Provider, model, and network diagnostics |
| `/keys` | Key inspector — verify what your terminal sends ([docs](docs/keyboard.md)) |
| `/retry` | Retry the last message; transient network errors also retry automatically |
| `/debug last` | Full drawer for the last request: sections, cache status, retries, timings |

## Images (vision models)

Copy an image, then press `Ctrl+V` in chat. The attachment shows as a chip
above the input and is sent with your next message — as OpenAI-style
`image_url` content parts for OpenAI-compatible servers, or native base64
`images` for Ollama.

Vision capability is detected from the model ID (llava, `*-vision`,
qwen-vl, minicpm-v, gemma3, moondream, …). If your vision model is not
recognized, set:

```yaml
chat:
  force_vision: true
```

Clipboard backends: macOS `pngpaste` (optional, faster) or built-in
AppleScript; Linux `wl-paste` or `xclip`; Windows PowerShell.

## Workspace tools (agent mode)

With tools enabled, the model can act on files in the directory you started
`llmtui` from — ask for a script and it lands on disk, not just in the chat:

```text
you: write a backup script and save it as scripts/backup.sh
assistant: (emits a write_file tool block)
⚒ tools: wrote 214 bytes to scripts/backup.sh
assistant: Saved. Run it with: sh scripts/backup.sh
```

Enable per session with `/tools on` (status: `/tools`), or permanently:

```yaml
tools:
  enabled: true
  approve: ask # ask | auto
  native: auto # auto | off — tool-calling protocol (see below)
  max_iterations: 10 # tool rounds per user message
  max_file_kb: 512   # per-file read/write and command output cap
  command_timeout: "30s"
```

Four tools are available: `list_dir`, `read_file`, `write_file`, and
`run_command` (one shell command in the workspace — `sh` on macOS/Linux,
`cmd` on Windows, detected automatically). With `native: auto` (the default)
they are offered through **standard function calling**: the tool schemas ride
in the request, the model answers with structured `tool_calls`, and results
go back as `role:"tool"` messages — the protocol tool-capable models
(Ollama's `tools` models, LM Studio, vLLM, llama.cpp) are trained on, so
multi-step tasks end naturally with a final answer instead of running into
the round limit. Backends that reject tool declarations fall back
automatically to a fenced-block prompt protocol that works with any model.
If the round limit is reached mid-task, you decide: a prompt asks whether
to grant another round budget or have the model give its final answer with
what it already has — the turn never dead-ends in an error.

**You stay in control**, the same way Claude Code or Codex work:

- While tools are on, a standing banner in the chat shows the exact
  directory the model can act on and the approval mode.
- Tool activity renders compactly, Claude-Code style: one line per action
  (`⚒ run: ls -la`) and one line per result (`⎿ 24 lines of output`);
  errors stay visible in full. `/tools output` toggles the complete output
  for inspection. The model always receives the full text — collapsing is
  display only.
- **File writes show a diff**, Claude-Code style: `Create(file)` lists the
  new content, `Update(file)` shows added/removed counts and a
  line-numbered `+`/`-` diff against the previous content (green/red, with
  unchanged context elided). Long diffs are capped in compact mode; the
  diff is never sent to the model.
- Reads, listings, and provably read-only commands (`ls`, `grep`, `cat`,
  `find`, `git status/log/diff`, … with no shell metacharacters) run
  automatically.
- **Writes and every other command stop and ask first** with a
  Claude-Code-style menu — the exact command or file is shown, then
  `Yes` / `Yes, and don't ask again this session` / `No`, picked with
  `↑`/`↓` + `Enter` (or `1`/`2`/`3`; `y`/`a`/`n` still work). `Esc`
  denies — the model is told and continues without the action. Set
  `approve: auto` or `/tools auto` to skip prompts entirely.

Safety: tools are **off by default**; everything is confined to the launch
directory (absolute paths, `..`, and symlink escapes rejected); writes into
`.git/` are blocked (a written git hook would be code execution); command
environments are stripped of secrets (`*_API_KEY`, tokens, passwords, all
`LLMTUI_*` vars); commands are time-limited; file sizes and outputs are
capped; and there is no delete tool. Works with any local model — models
with native tool support interact most reliably; for the rest the fenced
fallback needs an instruction-tuned model (≥7B recommended).

## Web tools

`/web on` (or `tools.web.enabled: true`) adds two more tools on top of
agent mode:

- `web_search` — DuckDuckGo search, **no API key needed**; returns titles,
  URLs, and snippets. Runs automatically (only the model's query leaves
  your machine).
- `web_fetch` — downloads one page, extracts the readable article, and
  hands it to the model as clean Markdown (capped at `max_page_kb`,
  default 128 KB). **Asks for approval per URL** — a fetched URL can leak
  data and fetched pages can carry prompt injection, so you stay in the
  loop.

```yaml
tools:
  web:
    enabled: false    # off by default — llmtui stays local-first
    max_results: 5
    max_page_kb: 128
    timeout: "20s"
```

Requests to private and local addresses (localhost, 10.x, 192.168.x,
link-local, …) are blocked by design, including via redirects and DNS
tricks, so a hostile page cannot use the model to probe your LAN. The
system prompt tells the model to treat fetched content as untrusted data
and to cite its sources.

## History & usage stats

With `chat.save_history: true` (the default), llmtui keeps everything under
`chat.history_dir` (`~/.local/share/llmtui/history`):

- **Exit summary** — quitting the chat prints a session report to the
  terminal: session ID, message counts, wall/API time, average speed, and
  per-model token usage, plus a pointer to the saved session.
- **Sessions** — saved with `Ctrl+S` / `/save`, and automatically on quit.
  One JSON file per chat session (messages + token totals); repeated saves
  update the same file. Image attachments are never persisted.
- **Usage log** — every completed exchange appends one line to
  `usage.jsonl` (timestamp, provider, model, tokens, duration — no message
  content). View it with `llmtui stats` or `/stats` in chat.

Set `chat.save_history: false` to disable both.

## Troubleshooting

Run `llmtui doctor` first — it checks the config file, the active
provider/model resolution, and pings every configured backend.

- **"offline demo mode" banner in chat** — no backend answered the health
  check at startup; start Ollama/LM Studio or fix `base_url`, then run
  `/config reload` in the chat (or restart) to reconnect.
- **Generation stops with "no response … the model may be stuck"** —
  `network.timeout` is the maximum wait for the *next* token (it resets on
  every token and on reasoning activity, so a steadily-streaming model is
  never cut off). A genuinely slow first token — a cold model load or a very
  long reasoning pause before any output — can still trip it. Raise it
  without a config file via an env var:

  ```bash
  LLMTUI_NETWORK_TIMEOUT=600s ./llmtui chat
  ```

  Reasoning models (those that "think" before answering) show a live
  `thinking…` indicator while they work.
- **Fonts/symbols look wrong** — llmtui works with any monospace font, but
  looks best with a Nerd Font such as JetBrains Mono Nerd Font or MesloLGS NF.

## Development

All common tasks are in the Makefile:

```bash
make build      # compile ./llmtui with version metadata
make run        # build and launch chat
make check      # fmt + vet + golangci-lint + race tests
make test       # unit tests
make cover      # coverage report
make dist       # cross-compile darwin/linux/windows into dist/ with checksums
make clean      # remove artifacts
make help       # list all targets
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
internal/cache/           local response cache (/cache)
internal/contextmgr/      context-window budgeting + heuristic summaries
internal/memory/          opt-in local memory snippets (/memory)
internal/modelprofile/    per-model-family tuning profiles (/profile)
internal/tools/           workspace file tools for agent mode (/tools)
internal/prompt/          prompt composition (raw message never rewritten)
internal/history/         session persistence + usage log
internal/clipboard/       image paste / text copy via platform tools
internal/tui/             Bubble Tea chat screen
internal/tui/components/  status bar, charts, usage panel, buttons
internal/tui/styles/      Lip Gloss theme
```

Design principles and layout are described in
[docs/tui-design.md](docs/tui-design.md).

## License

[MIT](LICENSE)
