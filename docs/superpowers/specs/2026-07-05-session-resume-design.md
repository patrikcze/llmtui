# Session Resume Design

## Goal

Let a user resume a previously saved chat session directly from the command line, similar to Claude Code's `--resume <id>`:

```bash
llmtui chat --resume session-20260702-163005   # resume that exact saved session
llmtui chat --continue                          # resume the most recently saved session
llmtui chat -c                                  # short form of --continue
```

## Context

llmtui already has most of the machinery this needs:

- Saved sessions are JSON files named with a sortable timestamp, e.g. `session-20260702-163005` (`history.NewSessionName`).
- `llmtui history` (CLI) already lists saved sessions (name, saved-at, provider, model, message count, tokens) via `history.List`.
- `/history load <name>` (in-app slash command) already fully resumes a saved session mid-TUI: it replaces the running conversation's messages, token totals, and session name (so a later save updates the same file instead of creating a new one), and clears the session summary.

What's missing is only the entry point: launching straight into a resumed session from the command line, instead of starting fresh and typing `/history load <name>` once inside the TUI.

This is intentionally scoped as a small, additive feature — no session-ID scheme change, no new interactive picker, no changes to how sessions are saved.

## Non-goals

- **No UUIDs.** Sessions keep their existing timestamp-based names. `--resume` takes that same name; there is no new ID format to migrate to or reconcile with already-saved sessions.
- **No interactive resume picker.** `llmtui chat --resume` with no name is not supported in this pass — `llmtui history` already lists names to copy from. A picker (closer to Claude Code's bare `--resume`) is a possible future addition, not part of this one.
- **Resuming does not change provider/model selection.** It restores conversation content (messages, token stats, session name) exactly like `/history load` does today — it does not force the provider/model the session was originally saved under. Normal `--provider`/`--model`/env/config precedence is unaffected.

## User-facing behavior

- `--resume <name>` and `--continue`/`-c` are flags on the `chat` subcommand only (not global/persistent flags).
- They are mutually exclusive; passing both is a normal Cobra flag-validation error.
- Resolution and validation happen **before** the TUI starts:
  - `--resume <name>`: loads that exact session. Unknown name → clear CLI error, non-zero exit, TUI never opens.
  - `--continue`: loads the most recently saved session (by `SavedAt`, same ordering `llmtui history` already uses). No saved sessions at all → clear CLI error, TUI never opens.
  - Either flag with `chat.history_dir` unconfigured → the same "chat.history_dir is not configured" error `llmtui history` already produces today (via the existing `Root.historyDir()` helper). This check does **not** depend on `chat.save_history` — exactly like `llmtui history` (list) already behaves regardless of that setting, since it's about reading existing files, not writing new ones.
- On success, the TUI opens with the resumed conversation already loaded — same effect as if the user had started fresh and immediately run `/history load <name>`.

## Architecture

### `internal/history`

Add one new function alongside the existing `List`/`Load`:

```go
// Latest returns the most recently saved session (by SavedAt) and its name,
// for --continue. Returns an error if dir has no saved sessions.
func Latest(dir string) (name string, s Session, err error)
```

Implementation: call `List(dir)` (already sorted newest-first), error if empty, then `Load(dir, metas[0].Name)`.

### `internal/tui`

`Options` gains two fields:

```go
type Options struct {
    Config     *config.Config
    Provider   provider.Provider
    Model      string
    ConfigPath string

    // ResumeSession, when non-nil, seeds the new Model with a previously
    // saved session (messages, stats, name) instead of starting empty.
    // ResumeSessionName is the on-disk name it was loaded from.
    ResumeSession     *history.Session
    ResumeSessionName string
}
```

The session-hydration logic currently inlined in `cmdHistory`'s `"load"` case (in `internal/tui/commands_local.go`) is extracted into a shared method:

```go
// adoptSession replaces the running conversation with a previously saved
// one: its messages, token totals, and name (so subsequent saves update the
// same file instead of creating a new one). Used by /history load and by
// --resume/--continue at startup.
func (m *Model) adoptSession(name string, s history.Session) {
    m.session.Messages = s.Messages
    m.session.Stats = nil
    m.session.TotalPromptTokens = s.Prompt
    m.session.TotalCompletionTokens = s.Reply
    m.session.AnyEstimated = s.Estimated
    m.sessionName = name
    m.summary = ""
}
```

`cmdHistory`'s `"load"` case calls this instead of assigning the fields inline, then keeps its own `refreshViewport()`/`notice` calls (those stay case-specific — the exact wording/timing of the confirmation notice differs slightly between an in-app command and a startup resume).

`New(opts Options) *Model` calls `m.adoptSession(opts.ResumeSessionName, *opts.ResumeSession)` right after constructing the fresh `Model`, when `opts.ResumeSession != nil`, and sets a startup notice (e.g. `"resumed <name> (N messages, provider/model)"`) so the user sees confirmation on first render.

### `internal/cli/chat.go`

```go
func newChatCmd(r *Root) *cobra.Command {
    var resumeName string
    var cont bool

    cmd := &cobra.Command{
        Use:   "chat",
        Short: "Start an interactive chat session",
        RunE: func(cmd *cobra.Command, args []string) error {
            prov, err := app.BuildActiveProvider(r.cfg)
            if err != nil {
                return fmt.Errorf("start chat: %w", err)
            }
            cfgPath, _ := r.configPath()
            opts := tui.Options{
                Config:     r.cfg,
                Provider:   prov,
                Model:      r.cfg.ActiveModel(),
                ConfigPath: cfgPath,
            }

            if resumeName != "" || cont {
                dir, err := r.historyDir()
                if err != nil {
                    return fmt.Errorf("resume: %w", err)
                }
                var name string
                var sess history.Session
                if cont {
                    name, sess, err = history.Latest(dir)
                } else {
                    name = resumeName
                    sess, err = history.Load(dir, name)
                }
                if err != nil {
                    return fmt.Errorf("resume: %w", err)
                }
                opts.ResumeSession = &sess
                opts.ResumeSessionName = name
            }

            return tui.Run(opts)
        },
    }

    cmd.Flags().StringVar(&resumeName, "resume", "", "resume a saved session by name (see `llmtui history`)")
    cmd.Flags().BoolVarP(&cont, "continue", "c", false, "resume the most recently saved session")
    cmd.MarkFlagsMutuallyExclusive("resume", "continue")
    return cmd
}
```

This keeps `cli/chat.go` thin: all resolution/validation logic lives in the already-tested `internal/history` package. `internal/cli` has no test files today (its `RunE` functions ultimately launch a real interactive Bubble Tea program via `tui.Run`, which isn't practical to unit test), so putting the testable logic in `history` instead — rather than inline in `chat.go` — matches how the rest of the CLI layer is structured.

## Error handling summary

| Condition | Behavior |
|---|---|
| `--resume` and `--continue` both passed | Cobra flag validation error, non-zero exit, TUI never starts |
| `--resume <name>` where name doesn't exist | `resume: read session: ...` error, non-zero exit, TUI never starts |
| `--continue` with zero saved sessions | `resume: no saved sessions in <dir>` error, non-zero exit, TUI never starts |
| `chat.history_dir` unconfigured, either flag used | `resume: chat.history_dir is not configured` error, non-zero exit, TUI never starts |
| Success | TUI opens with the resumed conversation loaded; startup notice confirms which session |

## Testing plan

- **`internal/history`**: table tests for `Latest` — empty dir (error), one session (returns it), multiple sessions (returns the newest by `SavedAt`, matching `List`'s ordering).
- **`internal/tui`**: a test building `Options{ResumeSession: &history.Session{...}, ResumeSessionName: "..."}`, calling `New`, and asserting the resulting `Model`'s messages/token totals/session name match the saved session. A second test confirms `/history load <name>` still behaves identically post-refactor (same shared `adoptSession` method, no regression).
- No new `internal/cli` tests — consistent with the existing convention (see Architecture above); the resolution logic being tested lives in `internal/history` instead.

## Documentation

- `README.md`: add `--resume`/`--continue` to the CLI flags/usage section.
- `docs/configuration.md`: note that `--resume`/`--continue` read from `chat.history_dir` regardless of `chat.save_history`, mirroring `llmtui history`.
