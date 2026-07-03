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
   `LLMTUI_CHAT_TEMPERATURE`, …; dots become underscores)
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

### `cache`, `memory`, `prompt`, `context`, `network`

Documented in detail in [cache.md](cache.md), [memory.md](memory.md),
[prompt-composition.md](prompt-composition.md), and
[context-management.md](context-management.md). Network:

```yaml
network:
  timeout: "120s"        # whole-request ceiling; raise on slow machines
  connect_timeout: "10s"
  retry:
    enabled: true        # retries only transient network errors
    max_attempts: 2
    backoff: "750ms"
```

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
