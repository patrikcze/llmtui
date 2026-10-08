# Slash Commands

Type `/` in the chat input to open the suggestion popup. `↑`/`↓` navigate,
`Tab` completes, `Enter` runs the highlighted command, `Esc` dismisses.
An exactly typed command always runs itself even when a longer command is
suggested. `/help` shows everything grouped by category; `/help <category>`
filters.

Commands that would change what an in-flight request depends on (`/clear`,
`/provider`, `/model`, `/config reload`, `/history load|clear`) are
unavailable while a reply, tool batch, or verification is in progress — press
`Esc` to stop it first.

## Chat
| Command | Description |
| --- | --- |
| `/help [topic]` | Keys and commands, grouped by category (scrollable dialog) |
| `/copy` | Copy the last reply to the clipboard |
| `/clear` | Clear the conversation (and session summary) |
| `/retry` | Retry the last user message with current settings |
| `/quit` (alias `/exit`) | Save the session and exit |

## Agent

| Command | Description |
| --- | --- |
| `/agent` · `/agent status` | Show mode and current run/cycle/stage/status |
| `/agent on` / `/agent off` | Enable bounded verified runs for new messages, or restore ordinary chat |
| `/agent cancel` | Cancel the active task-contract request, executor, tool batch, or verifier |
| `/agent resume [run-id]` | Resume the latest or selected resumable run with a fresh cycle (never replay incomplete work) |

Agent orchestration is separate from tool authority: use `/tools on` when a run
needs workspace tools. See [agent-loop.md](agent-loop.md).

## Provider
| Command | Description |
| --- | --- |
| `/provider` · `/provider list` | Choose a configured provider |
| `/provider switch <name>` (or `/provider <name>`) | Switch provider |
| `/providers` | Providers dialog: status, details and models per provider; `↑`/`↓` + `Enter` switches, `r` rechecks |

## Model
| Command | Description |
| --- | --- |
| `/models` | Choose a model with `↑`/`↓` and `Enter` |
| `/model <id>` | Switch model |
| `/profile list` | Choose and pin a model profile with `↑`/`↓` and `Enter` |
| `/profile auto` | Restore automatic profile matching for the active model |
| `/profile set <name>` / `/profile inspect` | Pin a named profile / inspect the active profile |
| `/think [on\|off\|auto\|low\|medium\|high\|status]` | Reasoning mode; GPT-OSS uses low/medium/high effort and defaults to medium |
| `/thoughts [show\|hide\|toggle\|status]` | Show or hide captured reasoning without changing model behavior |
| `/math [on\|off\|toggle\|status]` | Render LaTeX math (`$…$`, `$$…$$`) in Markdown answers as terminal Unicode; display-only, needs `ui.markdown` |

## Prompt
| Command | Description |
| --- | --- |
| `/prompt` | Composition overview |
| `/prompt preview` / `/prompt composed` | Full preview of the next request |
| `/prompt raw` | Just the raw user message part |
| `/prompt mode <minimal\|balanced\|coding\|strict>` | Set composition mode |
| `/template [list\|use <name>\|clear\|inspect <name>]` | Conversation templates; `list` opens a dialog where `Enter` uses the highlighted template, or clears it when already active |

## Context
| Command | Description |
| --- | --- |
| `/context` / `/context status` | Immutable context snapshot: budget, scope, summaries, agent/verifier, and active turn state |
| `/context summary` | Show the bounded summary applicable to the next request with its scope label |
| `/context preview` | Bounded categories and token metadata; `/prompt preview` shows the exact prompt |
| `/context refresh` | Recompute the read-only diagnostic snapshot |
| `/context strategy` | Show the active runtime strategy |
| `/context summarize` | Rebuild eligible older idle session context |
| `/context compact` / `/context rebuild` / `/compact` | Aliases for `/context summarize` |
| `/context clear-summary` | Clear only the idle session summary |
| `/context strategy <none\|truncate\|summarize\|auto>` | Change strategy while safe |

The first five commands are read-only and remain available during active work.
The mutation commands are rejected while a response, tool batch, approval,
budget extension, `ask_user`, tool continuation, verifier, or resumable agent
cycle owns context. They never stop work automatically.

## Cache
| Command | Description |
| --- | --- |
| `/cache` · `/cache stats` | Cache statistics |
| `/cache clear` | Remove all cached responses |
| `/cache on` / `/cache off` | Toggle at runtime |

## Memory
| Command | Description |
| --- | --- |
| `/memory` · `/memory list [user\|project\|episode\|run]` | List typed memory records |
| `/memory add <text>` · `/memory add user <text>` | Remember a user preference (never secrets) |
| `/memory add project architecture <text>` | Remember a workspace architecture fact |
| `/memory add project convention <text>` | Remember a workspace convention |
| `/memory add project decision <text>` | Remember a workspace decision |
| `/memory inspect <id>` | Show one record's kind, scope, trust, review state, and timestamps |
| `/memory search <query>` | Search eligible user/project/episode/run/RAG sources under the configured budget |
| `/memory explain <query>` | Show score components, token costs, selected hits, and content-free rejection reasons |
| `/memory status` | Show retrieval state, stored-record counts, last retrieval tiers, and active-context budget |
| `/memory remove <id>` · `/memory clear` | Remove one record; `clear` clears user preferences only |
| `/memory on` / `/memory off` | Toggle for this session |

## Tools
| Command | Description |
| --- | --- |
| `/tools` · `/tools status` | Workspace tools overlay: state, approval mode, workspace root, limits |
| `/tools on` / `/tools off` | Let the model list/read/search/write files and run commands under the launch directory |
| `/tools ask` / `/tools auto` | Require approval and revoke temporary scoped grants (default), or explicitly run workspace tools unprompted in a fully trusted workspace |
| `/tools output` | Toggle between full tool output and one-line summaries in the transcript |
| `/tools list` | List the available tools and their capabilities |
| `/tools inspect <name>` | Show one tool's details |
| `/tools check <cmd>` | Show how a command line would be classified, without running it |

## Web
| Command | Description |
| --- | --- |
| `/web` · `/web status` | Show whether the web tools are on |
| `/web on` / `/web off` | Let the model use `web_search` (DuckDuckGo, runs without asking) and `web_fetch` (asks per URL), or turn them off |

## RAG
Optional local workspace retrieval, off by default. See [rag.md](rag.md).

| Command | Description |
| --- | --- |
| `/rag` · `/rag status` | Show retrieval state and index size |
| `/rag on` / `/rag off` | Let indexed snippets inform prompts, or stop retrieval |
| `/rag index` | Build or rebuild the workspace index |
| `/rag search <query>` | Search the index |
| `/rag sources` | List the indexed source files |
| `/rag clear` | Clear the index |

## MCP
Optional Model Context Protocol servers over stdio, off by default.
Declaring a server starts nothing; only `connect` launches it. See [mcp.md](mcp.md).

| Command | Description |
| --- | --- |
| `/mcp` · `/mcp status` · `/mcp list` | Configured servers and their state |
| `/mcp tools` | Tools offered by connected servers |
| `/mcp inspect <server>` | One server's configuration and state |
| `/mcp enable <server>` / `/mcp disable <server>` | Allow a server to be connected, or disable it (disconnects it if running) |
| `/mcp connect <server>` / `/mcp disconnect <server>` | Launch the server and connect, or stop it |

## Skills
| Command | Description |
| --- | --- |
| `/skills` · `/skills status` | Skills overlay: discovered, active, limits, model-driven load state |
| `/skills list` | Dialog of discovered skills; `Enter` activates/deactivates the selected skill for the session |
| `/skills active` | Active skills in deterministic prompt order |
| `/skills inspect <id>` | Metadata, provenance, hash, recommended tools, content preview |
| `/skills use <id> [--scope run\|session]` | Activate a skill (default: session; model-driven loads are always run-scoped) |
| `/skills disable <id>` | Deactivate it (the file stays on disk) |
| `/skills reload` | Rescan search paths; active snapshots are kept and changes reported |
| `/skills paths` | Discovery paths and whether they exist |

## Plugins
| Command | Description |
| --- | --- |
| `/plugins` · `/plugins list` | Dialog of discovered plugin packages; `Enter` enables/disables the selected plugin (`/plugins status` shows the read-only list) |
| `/plugins inspect <id>` | Manifest, source, root, declared skills |
| `/plugins enable <id>` | Register the plugin's skills (activates nothing, runs nothing) |
| `/plugins disable <id>` | Unregister its skills; deactivates any that were active |
| `/plugins reload` | Rescan plugin paths |
| `/plugins paths` | Plugin discovery paths |

## Diagnostics
| Command | Description |
| --- | --- |
| `/doctor [provider [name]\|mcp\|personal-apps]` | Provider/model/network diagnostics; `personal-apps` passively checks Mail/Calendar configuration and the EventKit helper without launching it |
| `/personal-apps [status\|connect\|disconnect mail\|calendar]` | Mail/Calendar dialog: configuration, connection and scope per app; `Enter` connects or disconnects the highlighted app (your decision — the model can never do this) |
| `/entities [status\|list\|inspect <id>]` | Runtime entity references; `list` opens a dialog where `Enter` shows the highlighted entity |
| `/debug [on\|off\|last]` | Debug drawer for the last request (`last` opens a scrollable dialog; drag to copy text) |
| `/debug tool-calls [test]` | Inspect native tool calls for the last request; `test` runs a tool-call conformance probe against the current model |
| `/keys [raw]` | Interactive key inspector |
| `/config [path\|show\|reload]` | Configuration (secrets redacted) |

## Session
| Command | Description |
| --- | --- |
| `/usage [session\|last\|reset\|export]` | Usage dashboard and stats |
| `/stats` | Per-exchange session table |
| `/save` | Save the session |
| `/history [load <name>\|search <q>\|export md\|json\|clear]` | Saved sessions; bare `/history` opens a dialog where `Enter` loads the highlighted session |
