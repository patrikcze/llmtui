# Configuration

## Location

- macOS/Linux: `~/.config/llmtui/config.yaml`
- Windows: `%APPDATA%\llmtui\config.yaml`

`llmtui config init` writes an annotated starter file, `config path` shows
where it lives, `config show` prints the effective merged config with secrets
redacted. Inside the chat, `/config reload` re-reads the file and rebuilds the
cache, memory store, profiles, and the active provider — CLI flag and env
overrides survive the reload.

## Precedence

Highest wins:

1. CLI flags (`--provider`, `--model`, `--base-url`, `--api-key`,
   `--temperature`, `--top-p`, `--max-tokens`, `--system`, `--theme`,
   `--no-stream`, `--debug`, `--config`)
2. Environment variables, prefix `LLMTUI_` (`LLMTUI_PROVIDER`,
   `LLMTUI_MODEL`, `LLMTUI_BASE_URL`, `LLMTUI_API_KEY`,
   `LLMTUI_CHAT_TEMPERATURE`, `LLMTUI_CHAT_MAX_TOKENS`,
   `LLMTUI_NETWORK_TIMEOUT`, …; dots become underscores). This lets you tune
   the common knobs without a config file, e.g.
   `LLMTUI_NETWORK_TIMEOUT=600s ./llmtui chat`.
3. The YAML config file
4. Built-in defaults

## Sections

### `providers`

Each entry has `type` (`ollama`, `openai_compatible`, `mock`), `base_url`,
`api_key`, `api_key_env`, and `default_model`. Prefer `api_key_env` so
secrets never live in the file:

```yaml
providers:
  openai_compatible:
    api_key_env: LLMTUI_API_KEY
```

`ollama`, `lmstudio`, `openai_compatible`, and `mock` are always available
as built-ins even with an empty config. `default_provider` and
`default_model` at the top level pick the starting point; a provider's
`default_model` wins over the global one.

### `chat`

| Key | Default | Meaning |
| --- | --- | --- |
| `system_prompt` | helpful-assistant text | First system section of every request |
| `temperature` | `0.7` | Sampling temperature; `0` is honored (deterministic) |
| `top_p` | `0.9` | Nucleus sampling |
| `max_tokens` | `4096` | Response budget; reasoning models may need more |
| `stream` | `true` | Stream tokens (`--no-stream` overrides) |
| `save_history` | `true` | Enables sessions + `usage.jsonl` under `history_dir` |
| `history_dir` | `~/.local/share/llmtui/history` | Where history lives |
| `force_vision` | `false` | Allow image paste for unrecognized models |
| `model_profile` | auto | Pin a model profile by name |

### `tools`

Workspace file tools let the model list, read, and write files under the
directory llmtui was launched from (see the README's "Workspace tools"
section and [security.md](security.md)):

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Master switch (or `/tools on` per session) |
| `approve` | `ask` | `ask` prompts y/n before writes and non-read-only commands; `auto` runs them unprompted |
| `native` | `auto` | Tool-calling protocol: `auto` uses standard function calling (tools declared in the request, results returned as `role:"tool"` messages) and falls back automatically to the fenced-block prompt protocol when the backend rejects tools; `off` always uses fenced blocks |
| `max_iterations` | `10` | Tool rounds per user message. When spent, a prompt asks whether to grant more rounds or have the model answer with what it already has |
| `max_file_kb` | `512` | Per-file read/write and command output size cap |
| `command_timeout` | `30s` | Wall-clock limit for one `run_command` execution |

### `tools.guardrails`

Hardens the workspace tools. Every protection defaults **on**; set one
`false` only to loosen it. Use `/tools check "<command>"` to preview how a
command line would be classified, and `/tools list` / `/tools inspect
<name>` to review each capability's safety class and approval policy. See
[security.md](security.md):

| Key | Default | Meaning |
| --- | --- | --- |
| `block_git_dir_writes` | `true` | Reject `write_file` into `.git` (a written hook would run on your next git command) |
| `block_symlink_escape` | `true` | Reject paths whose symlinks resolve outside the workspace root |
| `protect_secret_files` | `true` | Reject writes into key-material directories (`.ssh`, `.gnupg`) |
| `protect_shell_startup_files` | `true` | Reject writes to shell startup files (`.bashrc`, `.zshrc`, `.profile`, `config.fish`, …) |
| `require_approval_for_secret_reads` | `true` | `read_file` of likely secret files (`.env`, `*.pem`, `*.key`, `id_rsa`, …) asks first |

### `tools.web`

Optional web tools (`web_search` via DuckDuckGo — no API key — and
`web_fetch`, which returns one page as readable Markdown). Off by default;
toggle per session with `/web on`. `web_search` runs automatically;
`web_fetch` asks for approval per URL. See the README's "Web tools" section
and [security.md](security.md):

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Master switch (or `/web on` per session) |
| `max_results` | `5` | Search hits returned per `web_search` call |
| `max_page_kb` | `128` | Fetched page content cap sent to the model |
| `timeout` | `20s` | Per-request limit for searches and fetches |

### `rag`

Optional local workspace index and keyword retrieval, off by default.
Documented in detail in [rag.md](rag.md).

### `mcp`

Optional Model Context Protocol servers, off by default. This build ships
config/interfaces only — servers can be declared, validated (`/doctor mcp`),
inspected, and toggled, but not yet connected. Documented in
[mcp.md](mcp.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Master switch; a server runs only when this and its own `enabled` are true |
| `servers.<name>.enabled` | `false` | Enable one declared server |
| `servers.<name>.transport` | — | Wire protocol (`stdio`) |
| `servers.<name>.command` / `args` | — | Command to launch the server |
| `servers.<name>.env` | — | Environment for the server; values redacted in `/mcp inspect`, never logged |
| `servers.<name>.approve` | `ask` | `ask` or `auto` for the server's tool calls |
| `servers.<name>.timeout` | `30s` | Per-call timeout |

### `cache`, `memory`, `prompt`, `context`, `network`

Documented in detail in [cache.md](cache.md), [memory.md](memory.md),
[prompt-composition.md](prompt-composition.md), and
[context-management.md](context-management.md). Network:

```yaml
network:
  # Inactivity timeout: the maximum wait for the *next* streamed token. It
  # resets on every token — and on reasoning activity — so a model that keeps
  # producing output is never cut off, however long the full answer is. Only
  # a genuine stall trips it. See docs/providers.md.
  timeout: "120s"
  connect_timeout: "10s"   # connection-attempt timeout
  retry:
    enabled: true          # retries only transient network errors
    max_attempts: 2
    backoff: "750ms"
```

`network.timeout` is the value to raise for a slow model with a long pause
before its first token (a cold load, or lengthy thinking that produces no
tokens at all). Set it in the file, or without a config file via
`LLMTUI_NETWORK_TIMEOUT=600s`, or per run with `--config`. Precedence is the
usual flags > env > file > defaults, so an env var overrides the file.

### `templates` and `model_profiles`

`templates` are reusable conversation presets (`/template use <name>`):
`description`, `system_prompt`, `prompt_mode`, `temperature`. Custom
`model_profiles` match by model-ID substring and are checked before the
built-ins (`/profile list`).

### `ui` and `privacy`

`ui.theme` (currently `claude_inspired`) and `ui.markdown` are honored
today; the remaining `ui` keys (`use_nerd_font`, `animations`,
`show_usage_chart`, `show_token_stats`, `compact_mode`) are reserved for
future use. The `privacy` section is declarative — the behaviors it
describes (local-first, key redaction) are hardcoded and not configurable
off; see [security.md](security.md).
