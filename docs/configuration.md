# Configuration

## Location

- macOS/Linux: `~/.config/llmtui/config.yaml`
- Windows: `%APPDATA%\llmtui\config.yaml`

`llmtui config init` writes an annotated starter file with no embedded
credentials or machine-specific paths. Optional execution and external-data
features (tools, web, MCP, RAG, and agent mode) start disabled, and mutating
tools default to `approve: ask`. The command refuses to overwrite an existing
config and uses user-only permissions (`0600`) on Unix. `config path` shows
where it lives, `config show` prints the effective merged config with secrets
redacted. Inside the chat, `/config reload` re-reads the file and rebuilds the
cache, memory store, profiles, and the active provider — CLI flag and env
overrides survive the reload.

## Precedence

Highest wins:

1. CLI flags (`--provider`, `--model`, `--base-url`, `--api-key`,
   `--temperature`, `--top-p`, `--max-tokens`, `--system`, `--theme`,
   `--context-size`, `--gpu-layers`, `--no-stream`, `--debug`, `--config`)
2. Environment variables, prefix `LLMTUI_` (`LLMTUI_PROVIDER`,
   `LLMTUI_MODEL`, `LLMTUI_BASE_URL`, `LLMTUI_API_KEY`,
   `LLMTUI_CONTEXT_SIZE`, `LLMTUI_GPU_LAYERS`,
   `LLMTUI_CHAT_TEMPERATURE`, `LLMTUI_CHAT_MAX_TOKENS`,
   `LLMTUI_NETWORK_TIMEOUT`, …; dots become underscores). This lets you tune
   the common knobs without a config file, e.g.
   `LLMTUI_NETWORK_TIMEOUT=600s ./llmtui chat`.
3. The YAML config file
4. Built-in defaults

## Sections

### `providers`

Each entry has `type` (`ollama`, `openai_compatible`, `embedded`, `mock`),
`base_url`, `api_key`, `api_key_env`, and `default_model`. Prefer
`api_key_env` so secrets never live in the file:

```yaml
providers:
  lmstudio:
    type: openai_compatible
    base_url: http://localhost:1234/v1
    api_key: "" # local LM Studio normally needs no key
    default_model: local-model

  openai_compatible:
    type: openai_compatible
    base_url: http://localhost:8080/v1
    api_key_env: LLMTUI_API_KEY
    api_key: ""
    default_model: local-model
```

`api_key_env` names an environment variable; it is not the credential itself.
An unset variable falls back to `api_key`, which should remain empty for local
servers that do not require authentication.

Provider capability overrides are optional and tri-state. Omit a field to
retain automatic/unknown behavior; set an explicit `true` or `false` when a
backend is known to support or reject it:

```yaml
providers:
  local_server:
    type: openai_compatible
    base_url: http://localhost:8080/v1
    capabilities:
      native_tools: true
      parallel_tool_calls: false
      reasoning_events: false
      structured_output: true
      context_window_tokens: 32768
```

The effective values are shown by `/doctor`. A runtime native-tool rejection
is remembered only for the exact provider/model pair and switches that pair to
the fenced fallback protocol; it does not disable native tools globally.

`ollama`, `lmstudio`, `openai_compatible`, `embedded`, and `mock` are always
available as built-ins even with an empty config. `default_provider` and
`default_model` at the top level pick the starting point; a provider's
`default_model` wins over the global one. For an embedded provider,
`model_path` wins over both defaults; an explicit `--model` or
`LLMTUI_MODEL` wins over `model_path`.

Embedded-only provider keys:

| Key | Default | Meaning |
| --- | --- | --- |
| `model_path` | empty | Local GGUF model file |
| `mmproj_path` | empty | Optional matching multimodal-projector GGUF; enables vision and fixes this provider to the configured pair |
| `library_path` | automatic | Advanced override for a trusted llama.cpp library directory; otherwise resolution checks `YZMA_LIB`, the release archive, the managed runtime, then a matching stamped legacy directory |
| `context_size` | `0` | `min(n_ctx_train, 8192)`; positive values are capped at the trained context unless an extrapolating `rope_scaling_type` is explicitly selected |
| `gpu_layers` | `-1` | `-1` all possible layers; `0` CPU only |
| `threads` | `0` | Automatic CPU thread selection |
| `batch_size` | `512` | Prompt-decode batch size |
| `chat_template` | GGUF metadata | Inline Jinja template override |
| `swa_full` | `false` | `true` restores full-size sliding-window KV caches (more memory) |
| `kv_cache_type` | `f16` | `q8_0` halves KV memory with a small quality cost |
| `flash_attention` | `auto` | `auto`, `on`, or `off` |
| `tool_format` | `auto` | Native tool grammar: `auto`, `standard`, `qwen`, `glm`, `mistral`, `gemma`, `gpt`, or `phi` |
| `rope_scaling_type` | GGUF metadata | Optional override: `none`, `linear`, `yarn`, or `longrope` |
| `rope_freq_base` | GGUF metadata | Positive RoPE base-frequency override |
| `rope_freq_scale` | GGUF metadata | Positive RoPE frequency-scale override |
| `yarn_ext_factor` | llama.cpp/model default | YaRN extrapolation mix factor |
| `yarn_attn_factor` | llama.cpp/model default | YaRN attention magnitude factor |
| `yarn_beta_fast` | llama.cpp/model default | YaRN low correction dimension |
| `yarn_beta_slow` | llama.cpp/model default | YaRN high correction dimension |
| `yarn_orig_ctx` | llama.cpp/model default | Positive original context size used by YaRN |
| `sampling.top_k` | `40` | Top-k sampler; omit the field to use the default, or set `0` to explicitly disable top-k filtering |
| `sampling.min_p` | `0.05` | Min-p sampler; omit the field to use the default, or set `0.0` to explicitly disable min-p filtering |
| `sampling.repeat_penalty` | `1.1` | Repetition penalty |
| `sampling.repeat_last_n` | `64` | Repetition-history length |
| `sampling.presence_penalty` | `0.0` | Flat per-token penalty for any token already seen, independent of `repeat_penalty` |
| `sampling.dry_multiplier` | `0.0` | DRY anti-repetition strength; `0` disables DRY |
| `sampling.dry_base` | `1.75` | Exponential base used when DRY is enabled |
| `sampling.dry_allowed_length` | `2` | Repeated sequence length allowed before DRY applies |
| `sampling.dry_penalty_last_n` | `-1` | DRY history window; `-1` resolves to the active context size |
| `sampling.dry_sequence_breakers` | `["\\n", ":", "\\\"", "*"]` | Sequence boundaries; the listed llama.cpp defaults apply when DRY is enabled and this key is omitted |
| `sampling.seed` | `0` | Random; nonzero is deterministic |
| `sampling.stop` | `[]` | Case-sensitive stop strings |

See [embedded.md](embedded.md) for installation, platform support, examples,
vision pairing, image limits, native tools/reasoning, and limitations.

RoPE/YaRN and DRY are embedded-runtime controls. Ollama, LM Studio, vLLM,
llama.cpp server, and other OpenAI-compatible backends own equivalent model
loading/sampling configuration; llmtui does not send non-standard fields that
could make otherwise compatible servers reject requests.

### `chat`

| Key | Default | Meaning |
| --- | --- | --- |
| `system_prompt` | helpful-assistant text | First system section of every request |
| `temperature` | `0.7` | Sampling temperature; `0` is honored (deterministic) |
| `top_p` | `0.9` | Nucleus sampling |
| `max_tokens` | `4096` | Maximum response budget; embedded requests clamp it to the positions remaining after the prompt. A response cut off by this limit is flagged (not silently accepted) — raise it for models/tasks that rewrite large files with `write_file` |
| `stream` | `true` | Stream tokens (`--no-stream` overrides) |
| `save_history` | `true` | Enables sessions + `usage.jsonl` under `history_dir` |
| `history_dir` | `~/.local/share/llmtui/history` | Where history lives |
| `force_vision` | `false` | Allow image paste for unrecognized models |
| `model_profile` | auto | Pin a model profile by name |
| `reasoning` | `auto` | `auto` \| `on` \| `off` — explicit thinking toggle for reasoning models; `auto` sends nothing |
| `strip_leaked_thinking` | `true` | Reroute a leading `<think>…</think>` block leaked into content by a misconfigured backend template out of the visible answer, history, and cache |

`llmtui chat --resume <name>` and `--continue` read saved sessions from
`history_dir` the same way `llmtui history` does — regardless of the current
`save_history` value, since they only read existing files rather than write
new ones.

### `agent`

Optional bounded multi-cycle execution with independent verification. Disabled
by default; `/agent on` toggles it for the current session. Agent mode does not
enable tools or change approvals. See [agent-loop.md](agent-loop.md) for the
lifecycle, stop policy, persistence, cancellation, and local-model behavior.

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Start verified runs for user messages (`/agent on|off` overrides for the session) |
| `max_cycles` | `8` | Maximum executor/verifier cycles per run |
| `max_tool_calls` | `32` | Hard total tool calls across all cycles |
| `max_tokens` | `100000` | Executor plus verifier token budget when usage is available |
| `max_elapsed` | `30m` | Wall-clock run limit |
| `max_repeated_failures` | `3` | Identical verification failures before stopping |
| `persist` | `true` | Atomically store bounded resumable run state; forced off by `privacy.store_prompts: false` |
| `path` | `~/.local/share/llmtui/agent-runs` | Private versioned run-record directory |
| `max_memory_kb` | `64` | Maximum serialized bytes per run |
| `max_runs` | `32` | Number of newest records retained |
| `verifier.enabled` | `true` | Legacy toggle; when `verifier.mode` is empty, `true` derives `adaptive` and `false` derives `deterministic` |
| `verifier.mode` | empty | Verification policy: `off`, `deterministic`, `adaptive` (deterministic evidence first, semantic evaluation only when it cannot decide), or `always` (semantic evaluation every cycle) |
| `verifier.model` | empty | Optional evaluator model ID on the active provider; empty reuses the executor model |
| `verifier.max_tokens` | `1024` | Evaluator response cap; raise it if the verifier itself gets cut off mid-JSON on a slower/weaker model |
| `verifier.timeout` | `120s` | Whole evaluator-request deadline |
| `verifier.max_attempts` | `2` | Verifier-inference attempts per cycle (a provider error, timeout, or exhausted internal format repair) before the cycle is parked as `verification_unavailable` instead of restarting the executor — see [agent-loop.md](agent-loop.md#verification) |
| `enforce_budgets_live` | `true` | Check `max_tool_calls`/`max_tokens` on every tool round using the run's true running totals, not only when a cycle completes. Set `false` to fall back to the cycle-boundary-only check if this causes an unexpected early stop — see [agent-loop.md](agent-loop.md#stop-conditions-and-budgets) |

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
| `max_file_kb` | `512` | Per-file read/write, command output, and MCP tool result size cap |
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

### `tools.no_progress`

Blocks repeated calls individually when they have produced no new evidence
(an unchanged result), in both ordinary tool-enabled chat and `/agent on`.
Fresh calls in the same batch still execute and results retain their original
order. Legitimate repetition — polling, pagination, retries — never trips it,
since any change in the result resets the count. See
[agent-loop.md](agent-loop.md#repeated-tool-calls-and-no-progress-detection):

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `true` | Master switch. Set `false` to revert to pre-v1 pass-through behavior if this produces a false positive in practice |
| `threshold` | `3` | Consecutive no-new-evidence repeats of the same call allowed before the next one is blocked |

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

### `skills` and `plugins`

Declarative task-instruction packages and the plugin packages that
contribute them. Documented in detail in [skills.md](skills.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `skills.enabled` | `true` | Master switch for discovery and activation |
| `skills.paths` | `[]` | Extra skill search directories |
| `skills.expose_catalog_to_model` | `true` | Offer tool-capable models a compact catalog plus the `skill_load` tool (requires `/tools on`) |
| `skills.max_active` | `8` | Concurrently active skills |
| `skills.max_skill_kb` | `64` | Per-skill file cap; oversized skills are rejected, never truncated |
| `skills.max_total_active_kb` | `256` | Combined active skill content cap |
| `plugins.paths` | `[]` | Extra plugin search directories |
| `plugins.enabled` | `[]` | Plugin IDs enabled at startup (`/plugins enable` adds more per session) |

### `rag`

Optional local workspace index and keyword retrieval, off by default.
Indexing skips likely secret filenames and high-confidence secret content;
retrieved snippets are sent to the configured model provider as prompt
context. Documented in detail in [rag.md](rag.md).

### `mcp`

Optional Model Context Protocol servers, off by default. Servers connect
over stdio only on an explicit `/mcp connect`. Documented in
[mcp.md](mcp.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Master switch; a server runs only when this and its own `enabled` are true |
| `servers.<name>.enabled` | `false` | Enable one declared server |
| `servers.<name>.transport` | — | Wire protocol (`stdio`) |
| `servers.<name>.command` / `args` | — | Command to launch the server |
| `servers.<name>.env` | — | Environment for the server; supports secure `env:NAME` and `file:/path` references; values redacted in `/mcp inspect`, never logged |
| `servers.<name>.approve` | `ask` | `ask` or `auto` for the server's tool calls |
| `servers.<name>.timeout` | `30s` | Per-call timeout |

### `tool_registry`

Optional read-only HTTP discovery for agent hosts and other clients that need
the exact native tool schemas llmtui will attach to its next provider request.
It is disabled by default and exposes only `GET /api/v1/tools`; it cannot call
tools or bypass approvals. See [tool-registry.md](tool-registry.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `enabled` | `false` | Start the registry with the chat TUI |
| `listen` | `127.0.0.1:7834` | TCP listen address; unauthenticated listeners must be loopback |
| `token_env` | empty | Environment variable containing a bearer token; required for non-loopback listeners |
| `shutdown_timeout` | `5s` | Grace period for in-flight registry requests when the TUI exits |

The snapshot changes with session state. `/tools off`, `/web off`, loss of
native-tool support, and MCP disconnect/disable remove affected schemas. After
`/mcp connect <server>` completes its MCP `tools/list`, those prefixed
`mcp__<server>__<tool>` schemas appear in both the registry response and every
subsequent native LLM request.

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

Keep profile matches lowercase and set `context_window` to the context size
actually loaded by the server, not automatically to the model's advertised
maximum. A generalized non-reasoning local-model profile looks like this:

```yaml
model_profiles:
  local-gemma-12b:
    match: ["vendor/gemma-12b", "gemma-12b"]
    context_window: 65536
    preferred_temperature: 0.2
    supports_json_mode: true
    prompt_style: direct
    reasoning_hint: false
```

`reasoning_hint` only controls the helper text added to the prompt. It does
not set `/think`. Keep `chat.reasoning: auto` to omit `enable_thinking` for
non-reasoning models; use `/think on|off` only as a session override for a
backend and model that support it.

### `ui` and `privacy`

`ui.theme` (`claude_inspired` (default), `midnight`, or `forest`), `ui.markdown`, and
`ui.show_reasoning` are honored today. `show_reasoning` defaults to `true` and
can be changed for the current session with `/thoughts show|hide`; it affects
only presentation and does not enable or disable model reasoning. The
remaining `ui` keys (`use_nerd_font`, `animations`, `show_usage_chart`,
`show_token_stats`, `compact_mode`) are reserved for future use. The
`privacy` section is declarative — the behaviors it describes (local-first,
key redaction) are hardcoded and not configurable off; see
[security.md](security.md).
