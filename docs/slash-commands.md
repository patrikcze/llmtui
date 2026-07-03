# Slash Commands

Type `/` in the chat input to open the suggestion popup. `↑`/`↓` navigate,
`Tab` completes, `Enter` runs the highlighted command, `Esc` dismisses.
An exactly typed command always runs itself even when a longer command is
suggested. `/help` shows everything grouped by category; `/help <category>`
filters.

Commands that would change what an in-flight request depends on (`/clear`,
`/provider`, `/model`, `/config reload`, `/history load|clear`) are
unavailable while a reply is streaming — press `Esc` to stop it first.

## Chat
| Command | Description |
| --- | --- |
| `/help [topic]` | Keys and commands, grouped by category |
| `/copy` | Copy the last reply to the clipboard |
| `/clear` | Clear the conversation (and session summary) |
| `/retry` | Retry the last user message with current settings |
| `/quit` (alias `/exit`) | Save the session and exit |

## Provider
| Command | Description |
| --- | --- |
| `/provider` · `/provider list` | Show configured providers |
| `/provider switch <name>` (or `/provider <name>`) | Switch provider |
| `/providers` | List providers with status |

## Model
| Command | Description |
| --- | --- |
| `/models` | List models on the current provider |
| `/model <id>` | Switch model |
| `/profile [list\|auto\|set <name>\|inspect]` | Model profiles |

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
| `/memory` · `/memory list` | List snippets |
| `/memory add <text>` | Remember a preference (never secrets) |
| `/memory remove <id>` / `/memory clear` | Forget |
| `/memory on` / `/memory off` | Toggle for this session |

## Tools
| Command | Description |
| --- | --- |
| `/tools` · `/tools status` | Workspace tools overlay: state, workspace root, limits |
| `/tools on` / `/tools off` | Let the model list/read/write files under the launch directory |

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
