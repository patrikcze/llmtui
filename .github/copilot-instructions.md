# Copilot instructions

`llmtui` — a Go terminal UI and bounded agent runtime for local LLMs.

**Read `CLAUDE.md` at the repository root before suggesting changes.** It is the
source of truth for commands, architecture, conventions, gotchas, and
out-of-scope areas. Nothing from it is repeated here.

- Charm imports are `charm.land/…/v2`, never `github.com/charmbracelet/*` — the
  older paths still autocomplete but are not this module's dependencies.
- Never suggest `testify`, a logging library, or `github.com/pkg/errors`: none
  are dependencies and all three contradict existing code.
- Commit messages: `type(scope):`, no AI-attribution trailer.
