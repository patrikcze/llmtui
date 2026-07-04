# Local RAG (workspace retrieval)

llmtui can index the files in a workspace and retrieve keyword-matched
snippets to add as **labeled reference context** to your prompts. It is
**optional and disabled by default** — nothing is indexed or retrieved until
you enable it and run `/rag index`.

This first version is deliberately simple:

- **Local keyword retrieval only.** Scoring is BM25-lite over tokenized
  terms. There are **no embeddings, no vector database, and no external
  services**. Everything runs on your machine.
- **Reference, not instruction.** Retrieved snippets are added to the system
  prompt under a "Retrieved Workspace Context" section that explicitly tells
  the model to treat them as reference material, to prefer the user request
  on any conflict, and to flag possible staleness.
- **Your message is never rewritten.** Retrieval never modifies or replaces
  the raw user message; it only adds a separate, clearly-labeled section.

## Quick start

```text
/rag index            # build the index from the configured workspace root
/rag on               # use retrieval for subsequent messages
/rag search <query>   # preview what would be retrieved, with scores
/rag sources          # list the indexed files
/rag status           # show index size, workspace, top_k, and budget
/rag clear            # delete the index
/rag off              # stop retrieving
```

While RAG is on and an index exists, a banner in the chat notes it, and every
message shows its retrieved snippets in `/debug last`. The retrieved section
is visible in `/prompt preview`.

## What gets indexed

Indexing walks the configured workspace root and applies these rules:

- Only files matching `rag.workspace.include` globs are considered; files
  matching `rag.workspace.exclude` (plus `.git`, `node_modules`, `vendor`,
  `dist`, `build` by default) are pruned.
- **Binary files are skipped** (detected by a NUL byte in the first 8 KB).
- **Likely secret files are never indexed** — `.env`, `*.pem`, `*.key`,
  `id_rsa`, `.netrc`, credential-named files, and the contents of `.ssh` /
  `.gnupg`.
- **Nothing outside the workspace root is indexed.** Symlinks that resolve
  outside the root are rejected.
- Files larger than `rag.workspace.max_file_kb` are skipped, and indexing
  stops once `rag.workspace.max_total_mb` of content has been read.

Files are split into line-windowed chunks; each retrieval hit reports its
source path and line range.

## Configuration

See [configuration.md](configuration.md) for the full `rag.*` reference. The
defaults keep RAG off:

```yaml
rag:
  enabled: false
  index_path: "~/.local/share/llmtui/rag"
  workspace:
    enabled: false
    root: "."
    include: ["**/*.go", "**/*.md", "**/*.txt", "**/*.yaml", "**/*.yml", "**/*.json"]
    exclude: [".git/**", "node_modules/**", "vendor/**", "dist/**", "build/**"]
    max_file_kb: 512
    max_total_mb: 256
  retrieval:
    top_k: 6
    max_context_tokens: 3000
    strategy: "keyword"
```

The on-disk index (`index_path/index.json`) stores workspace source excerpts
and is written with owner-only permissions. Delete it any time with
`/rag clear`.

## Disabling everything

RAG is off unless you turn it on. To ensure it never runs, keep
`rag.enabled: false` (the default) or run `/rag off`; to remove any stored
index, run `/rag clear`.
