# Security & Privacy

llmtui is local-first. It makes no network calls except to the LLM backends
you configure, and it sends no telemetry of any kind.

## Secrets

- Prefer `api_key_env` in the config so keys never live in a file:

  ```yaml
  providers:
    openai_compatible:
      api_key_env: LLMTUI_API_KEY
  ```

- API keys are never logged and never appear in `/debug`, `/prompt
  composed`, or error messages.
- `config show` and `/config show` redact keys: anything shorter than 12
  characters is masked entirely; longer keys show only the first and last
  two characters.
- Response cache keys are SHA-256 hashes of request-shaping fields only —
  API keys are never part of the key or the cached entries
  ([cache.md](cache.md)).

## What is stored on disk, and where

| Data | Location | Opt-out |
| --- | --- | --- |
| Config | `~/.config/llmtui/config.yaml` (file `0600`) | — |
| Sessions (messages + token totals) | `chat.history_dir` (dir `0700`, files `0600`) | `chat.save_history: false` |
| Usage log (timestamps, provider, model, token counts — no message content) | `usage.jsonl` in the history dir | `chat.save_history: false` |
| Response cache | `cache.path` (dir `0700`, files `0600`) | `cache.enabled: false` or `/cache off` |
| Memory snippets | `memory.path` (file `0600`) | disabled by default; `/memory off` |

Image attachments are never persisted anywhere. Memory is strictly opt-in,
never auto-extracts anything, and must not be used for secrets (`/memory
add` reminds you).

## Hardening details

- Session names from `/history load` are validated against path traversal —
  they cannot escape the history directory.
- Workspace tools (`/tools`) are off by default and follow the
  least-privilege posture of mainstream coding agents (per OWASP LLM Top 10,
  "Excessive Agency"):
  - **Disclosure** — while tools are on, a standing banner in the chat names
    the exact directory the model can act on and the approval mode; every
    executed action and its result are shown in the conversation.
  - **Approval gate** — writes and non-read-only commands require an
    explicit `y`/`a` from the user before anything happens (`tools.approve:
    ask`, the default). Only reads, listings, and allowlisted read-only
    commands without shell metacharacters run unprompted.
  - **Confinement** — absolute paths, `..`, and symlinks resolving outside
    the launch directory are rejected; commands run with the workspace as
    their working directory.
  - **`.git` protection** — writes into `.git/` are blocked; a
    model-written git hook would otherwise execute on your next git command.
  - **Secret hygiene** — the environment passed to `run_command` is
    stripped of `LLMTUI_*` and anything matching key/token/secret/password
    patterns, so credentials cannot round-trip into model context via `env`.
  - **Bounded execution** — commands are time-limited
    (`tools.command_timeout`), reads/writes/outputs are size-capped, the
    tool loop stops after `tools.max_iterations` rounds per message, and
    there is no delete tool.
  - **Residual risk to know about** — content the model reads (files,
    command output) re-enters its context; a malicious file could try to
    instruct the model to take actions (prompt injection). The approval
    gate exists precisely so *you* are the final check on every mutating
    action — prefer `ask` mode when working on untrusted repositories.
- Debug output (`/debug last`) shows request shape, sections, and timings,
  never credentials.
- The `privacy` config section documents intent (`local_first`,
  `redact_api_keys_in_logs`, `store_prompts`) but these behaviors are
  hardcoded on — there is no switch that starts logging keys or sending
  data anywhere.

## Reporting

If you find a security issue, please open a GitHub issue on the repository
(or contact the author privately for anything sensitive) before publishing
details.
