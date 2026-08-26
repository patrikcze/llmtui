# Local Memory

Local memory is user-curated and **disabled by default**
(`memory.enabled: false`). Nothing is extracted or saved automatically.

Four memory tiers are available:

| Tier | Kinds | Storage |
| --- | --- | --- |
| User | `user_preference` | YAML at `memory.path` |
| Project | `project_architecture`, `project_convention`, `project_decision` | Versioned JSON under `memory/projects/<workspace-id>.json`, next to `memory.path` |
| Episode | `episode` | Compact summary embedded in an explicitly saved session under `chat.history_dir` |
| Agent run | objective, criteria, failures, evidence | Bounded live/persisted `AgentRun`; never automatically promoted |

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
preferences only. `/memory list episode` shows project-scoped summaries from
saved sessions; `/memory list run` shows bounded state from the current/latest
run.

`/memory off` disables user/project/episode retrieval and verified-outcome
promotion prompts for the current session. It does not delete stored records,
disable agent verification, or remove the agent's bounded cycle memory and run
persistence; those are controller state used to execute and resume `/agent on`.
Explicit list, inspect, search, remove, and add commands remain available while
automatic prompt injection is off.

Explicit `/save` or Ctrl+S creates or refreshes a compact episode. Automatic
quit saves refresh it only when `memory.episodic.capture: true`; otherwise a
previously loaded episode is preserved. Episodes include bounded visible goal
and outcome text plus status/provider/model metadata. They never include full
transcripts, reasoning, images, or tool-call arguments.

When unified retrieval is enabled, each source is normalized independently,
then ranked with bounded scope/trust/recency metadata boosts. Exact content is
deduplicated, overlapping source chunks are collapsed, kinds are capped for
diversity, and selected records are packed under per-tier soft caps plus one
hard total token budget. Unused tier budget may be reassigned without exceeding
the total. Retrieval is deterministic, local, and lexical; no embedding model,
vector database, or external retrieval API is used.

Selected records appear in one versioned `Active Context` prompt section. Every
record carries kind, scope, source, trust, and freshness metadata and has its
own collision-resistant untrusted frame. Context cannot override the current
request, prove success, or grant permissions. `/prompt composed` shows the
exact block, `/memory explain` shows score components/budget rejections, and
`/debug last` shows content-free timing/count/token diagnostics.

Project records created by commands are user-authored and approved. The store
can hold model proposals in a pending-review state, but pending records are not
searchable or injected into prompts.

When memory is enabled, a verifier-passed agent run offers a picker that defaults
to `skip`. Choosing architecture, convention, or decision explicitly promotes
one bounded outcome. The durable record remains `model_proposed` trust with
approved review state and preserves source run/cycle provenance. Memory-off,
failed, parked, cancelled, or unverified runs are never promoted automatically.

Default retrieval settings:

```yaml
memory:
  episodic:
    capture: false
  retrieval:
    enabled: true
    max_context_tokens: 1800
    top_k: 10
    user_tokens: 256
    project_tokens: 512
    episodic_tokens: 384
    agent_tokens: 512
    source_tokens: 768
```

Likely credentials are redacted before a project record is returned or written,
but memory is not a secret manager. **Do not store secrets or sensitive personal
data in any memory tier.**
