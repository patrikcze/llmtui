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
| `/help [topic]` | Keys and commands, grouped by category |
| `/copy` | Copy the last reply to the clipboard |
| `/clear` | Clear the conversation (and session summary) |
| `/retry` | Retry the last user message with current settings |
| `/quit` (alias `/exit`) | Save the session and exit |

## Agent

| Command | Description |
| --- | --- |
| `/agent` · `/agent status` | Show mode and current run/cycle/stage/status |
| `/agent on` / `/agent off` | Enable bounded verified runs for new messages, or restore ordinary chat |
| `/agent cancel` | Cancel the active executor, tool batch, or verifier |
| `/agent resume [run-id]` | Resume the latest or selected resumable run with a fresh cycle (never replay incomplete work) |

Agent orchestration is separate from tool authority: use `/tools on` when a run
needs workspace tools. See [agent-loop.md](agent-loop.md).

## Provider
| Command | Description |
| --- | --- |
| `/provider` · `/provider list` | Choose a configured provider |
| `/provider switch <name>` (or `/provider <name>`) | Switch provider |
| `/providers` | Choose a configured provider with `↑`/`↓` and `Enter` |

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

## Prompt
| Command | Description |
| --- | --- |
| `/prompt` | Composition overview |
| `/prompt preview` / `/prompt composed` | Full preview of the next request |
| `/prompt raw` | Just the raw user message part |
| `/prompt mode <minimal\|balanced\|coding\|strict>` | Set composition mode |
| `/template [list\|use <name>\|clear\|inspect <name>]` | Conversation templates |

## Context
| Command | Description |
| --- | --- |
| `/context` | Window, usage bar, strategy, summary state |
| `/context summary` | Show the current session summary |
| `/context rebuild` | Rebuild the summary from older messages |
| `/context clear-summary` | Drop the summary |
| `/context strategy <none\|truncate\|summarize\|auto>` | Change strategy |

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
| `/memory remove <id>` · `/memory clear` | Remove one record; `clear` clears user preferences only |
| `/memory on` / `/memory off` | Toggle for this session |

## Tools
| Command | Description |
| --- | --- |
| `/tools` · `/tools status` | Workspace tools overlay: state, approval mode, workspace root, limits |
| `/tools on` / `/tools off` | Let the model list/read/search/write files and run commands under the launch directory |
| `/tools ask` / `/tools auto` | Require approval and revoke temporary scoped grants (default), or explicitly run workspace tools unprompted in a fully trusted workspace |

## Skills
| Command | Description |
| --- | --- |
| `/skills` · `/skills status` | Skills overlay: discovered, active, limits, model-driven load state |
| `/skills list` | Arrow-key picker of discovered skills; `Enter` activates/deactivates the selected skill for the session |
| `/skills active` | Active skills in deterministic prompt order |
| `/skills inspect <id>` | Metadata, provenance, hash, recommended tools, content preview |
| `/skills use <id> [--scope run\|session]` | Activate a skill (default: session; model-driven loads are always run-scoped) |
| `/skills disable <id>` | Deactivate it (the file stays on disk) |
| `/skills reload` | Rescan search paths; active snapshots are kept and changes reported |
| `/skills paths` | Discovery paths and whether they exist |

## Plugins
| Command | Description |
| --- | --- |
| `/plugins` · `/plugins list` | Discovered plugin packages and their state |
| `/plugins inspect <id>` | Manifest, source, root, declared skills |
| `/plugins enable <id>` | Register the plugin's skills (activates nothing, runs nothing) |
| `/plugins disable <id>` | Unregister its skills; deactivates any that were active |
| `/plugins reload` | Rescan plugin paths |
| `/plugins paths` | Plugin discovery paths |

## Diagnostics
| Command | Description |
| --- | --- |
| `/doctor [provider [name]]` | Provider/model/network diagnostics |
| `/debug [on\|off\|last]` | Debug drawer for the last request |
| `/keys [raw]` | Interactive key inspector |
| `/config [path\|show\|reload]` | Configuration (secrets redacted) |

## Session
| Command | Description |
| --- | --- |
| `/usage [session\|last\|reset\|export]` | Usage dashboard and stats |
| `/stats` | Per-exchange session table |
| `/save` | Save the session |
| `/history [load <name>\|search <q>\|export md\|json\|clear]` | Saved sessions |
