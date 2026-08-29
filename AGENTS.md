# AGENTS.md

`llmtui` — a Go terminal UI and bounded agent runtime for local LLMs.

**`CLAUDE.md` in this directory is the single source of truth** for commands,
architecture, conventions, gotchas, and what you must not change. Read it before
your first edit. Nothing from it is restated here.

- The code wins over any doc. `CLAUDE.md` and `docs/` describe the repo as of
  the last commit that touched them — verify before acting.
- If your change makes `CLAUDE.md` wrong, fix it in the same commit.
- Claude Code: `.claude/skills/build-local-llm-tui/` holds the task-level
  playbooks (per-area workflows, extension recipes, test matrix);
  `.claude/agents/` holds the review/implementation subagent definitions. Both
  are tracked — local settings and task notes under `.claude/` are not.
