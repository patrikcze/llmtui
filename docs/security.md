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
    commands without shell metacharacters run unprompted. Run `/tools check
    "<command>"` to preview how any command line would be classified and
    why.
  - **Command classifier** — a command is auto-approved only when it is an
    allowlisted read-only program (`ls`, `cat`, `grep`, `rg`, `find`,
    `git status/log/diff`, `go test/vet/fmt/list`, …) with no shell
    metacharacters (pipes, redirects, chaining, substitution) and no
    escalating arguments (`find -delete/-exec`). Known-dangerous programs
    (`rm`, `mv`, `chmod`, `sudo`, `curl`, package managers, cloud/container
    CLIs) always ask, as does anything unrecognized.
  - **Confinement** — absolute paths, `..`, and symlinks resolving outside
    the launch directory are rejected; commands run with the workspace as
    their working directory.
  - **Write guardrails** — writes into `.git/` (a model-written git hook
    would otherwise execute on your next git command), key-material
    directories (`.ssh`, `.gnupg`), and shell startup files (`.bashrc`,
    `.zshrc`, `.profile`, `config.fish`, …) are blocked.
  - **Secret-read approval** — reads of likely secret files (`.env`,
    `*.pem`, `*.key`, `id_rsa`, `id_ed25519`, `.netrc`, credential-named
    files) require approval even though ordinary reads run unprompted.
    Each protection can be relaxed individually under
    `tools.guardrails.*` in config; all default on.
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
- Web tools (`/web`) are off by default and add their own guardrails:
  - **SSRF guard** — only `http`/`https` URLs; hosts resolving to loopback,
    private (RFC 1918), link-local, unique-local, or unspecified addresses
    are refused. The check happens inside the dialer (the vetted IP is what
    gets connected, so DNS rebinding cannot bypass it) and re-runs on every
    redirect hop (max 5). A hostile page cannot use the model to probe
    `localhost` or your LAN.
  - **Per-URL approval for fetches** — a model-chosen URL can encode data
    in its query string (exfiltration), so `web_fetch` asks before every
    request; `web_search` sends only the model's query to DuckDuckGo and
    runs unprompted.
  - **Bounded content** — response bodies are read up to 4 MB and reduced
    to readable Markdown capped at `tools.web.max_page_kb` (default 128 KB)
    before reaching the model; binary content types are refused.
  - **Prompt-injection posture** — fetched pages are untrusted input. The
    system prompt says so explicitly, and the fetch approval plus the
    write/command approvals mean injected instructions still cannot mutate
    anything without you seeing it.
  - **No API keys involved** — search uses DuckDuckGo's public HTML
    endpoint; nothing identifies you beyond the request itself.
- Local RAG (`/rag`) is off by default and stays fully local (see
  [rag.md](rag.md)):
  - **No network, no third parties** — retrieval is local keyword scoring
    (BM25-lite). There are no embeddings, no vector database, and no external
    calls; enabling RAG does not cause any data to leave your machine.
  - **Indexing respects the same secret hygiene as the tools** — `.env`,
    `*.pem`, `*.key`, `id_rsa`, `.netrc`, and `.ssh`/`.gnupg` contents are
    never indexed; binary files are skipped; nothing outside the workspace
    root is read, and symlinks resolving outside it are rejected.
  - **Retrieved context cannot override you** — snippets are added as a
    clearly-labeled reference section that instructs the model to prefer the
    user request on any conflict; your raw message is never modified. What
    was retrieved is visible in `/prompt preview` and `/debug last`.
  - **The on-disk index** (`rag.index_path`) may contain workspace source
    excerpts and is written owner-only; remove it with `/rag clear`.
- MCP servers (`/mcp`) are off by default and, in this build, are
  config/interfaces only (see [mcp.md](mcp.md)):
  - **Nothing starts on its own** — declaring a server does not contact or
    launch it. Starting a server would run its configured command, a
    potentially dangerous action gated by the approval model (`approve: ask`
    by default). A server runs only when both `mcp.enabled` and the server's
    own `enabled` are true.
  - **Invalid config never blocks startup** — `/doctor mcp` validates config,
    but a malformed disabled server does not affect normal chat; command
    existence is only probed for enabled servers while MCP is enabled.
  - **Environments are redacted** — `/mcp inspect` shows only env variable
    names, never their values, and env values are never logged.
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
