# Local Memory

Local memory is user-curated and **disabled by default**
(`memory.enabled: false`). Nothing is extracted or saved automatically.

Two durable tiers are available:

| Tier | Kinds | Storage |
| --- | --- | --- |
| User | `user_preference` | YAML at `memory.path` |
| Project | `project_architecture`, `project_convention`, `project_decision` | Versioned JSON under `memory/projects/<workspace-id>.json`, next to `memory.path` |

The project workspace ID is a SHA-256 hash of the canonical,
symlink-resolved launch directory. Project records therefore stay isolated:
starting llmtui in workspace A never retrieves workspace B's records. Project
files are replaced atomically and use owner-only directory/file permissions
(`0700`/`0600`).

## Commands

```text
/memory add <text>                              # backward-compatible user preference
/memory add user <text>
/memory add project architecture <text>
/memory add project convention <text>
/memory add project decision <text>
/memory list [user|project|episode|run]
/memory inspect <id>
/memory remove <id>
/memory search <query>
/memory explain <query>
/memory on | /memory off
```

`/memory clear` remains available for backward compatibility and clears user
preferences only. Episode capture and durable agent-run promotion are not
implemented yet; listing those tiers reports their current status.

When memory is enabled, lexical retrieval adds at most three relevant records
per implemented tier. User preferences keep the existing `Relevant Memory`
section. Typed project records include kind, scope, source, trust, and freshness
metadata, and every record body is structurally framed as reference data that
cannot override the current request or grant permissions. `/prompt preview`
shows exactly what is included.

Project records created by commands are user-authored and approved. The store
can hold model proposals in a pending-review state, but pending records are not
searchable or injected into prompts.

Likely credentials are redacted before a project record is returned or written,
but memory is not a secret manager. **Do not store secrets or sensitive personal
data in any memory tier.**
