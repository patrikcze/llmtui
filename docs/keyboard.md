# Keyboard

| Key | Action |
| --- | --- |
| `Enter` | Send message / run command |
| `Shift+Enter` | Newline — only on terminals with distinguishable modified keys (see below) |
| `Alt/Option+Enter` | Newline — needs "Option as Meta" on macOS terminals |
| `Ctrl+J` | Newline — works in **every** terminal |
| `\` + `Enter` | Newline — trailing backslash continues the line, works everywhere |
| `Ctrl+U` | Clear the whole prompt box in one keystroke |
| `Ctrl+S` | Save the session to the history directory |
| `Ctrl+Y` | Copy the last assistant reply to the clipboard (raw Markdown) |
| `Ctrl+O` | Toggle text-selection mode (releases the mouse so your terminal can select/copy) |
| `Ctrl+V` | Paste an image from the clipboard (vision models) |
| `Ctrl+X` | Remove the last pasted image |
| `Ctrl+L` | Clear the conversation |
| `PgUp` / `PgDn` | Scroll the chat (mouse wheel works too); typing never scrolls it |
| `Ctrl+C` ×2 | Quit (first press stops generation / clears input) |
| `Esc` | Stop generation (keeps partial reply) · close overlay |
| `↑` / `↓` | Choose an item in `/models`, `/providers`, `/skills list`, `/plugins list`, and other pickers; navigate command suggestions |
| `↑` / `↓` in the composer | Recall previously submitted prompts and slash commands when the composer is empty; otherwise move the cursor between (soft-wrapped) input lines |
| `Enter` in picker | Switch to the selected model/provider, or toggle enable/disable for a skill/plugin |

The in-app `/help` overlay always reflects the current build's exact
keybindings if this table ever drifts.

## The Shift+Enter reality

Legacy terminal input sends the identical byte for Enter and Shift+Enter —
no application can tell them apart. llmtui enables the `modifyOtherKeys`
keyboard protocol at startup, which makes Shift+Enter report distinctly in
**iTerm2, VS Code, WezTerm, Ghostty, Alacritty, xterm**. macOS Terminal.app
and (unmapped) Kitty do not support it — use the fallbacks above.

`Cmd+Enter` can never work: macOS terminals consume Cmd shortcuts
themselves.

## Verifying with /keys

Run `/keys`, then press the key you care about. If Shift+Enter shows as
plain `enter`, your terminal does not expose it — use `Alt+Enter`, `Ctrl+J`
or `\` + `Enter`. `/keys raw` additionally shows the escape sequences.

tmux, SSH hops, and terminal emulator settings can all strip modified-key
protocols; `/keys` shows what actually arrives after all of them.

Multiline pastes are inserted into the input box with newlines preserved
(bracketed paste); pasting never submits line by line.

## Composer history

`↑` on an empty composer recalls the most recent submission (prompt or slash
command); `↑` again steps to older entries, `↓` steps back toward newer ones,
and `↓` past the newest entry restores whatever draft you had before browsing.
Recalled text is fully editable and re-runs through the normal send / command
path on `Enter`. History is ordered (no wrap-around), capped at the last 100
submissions, session-local, and never written to disk — it is unrelated to
`/history` (saved chat sessions) and survives `/clear`. When a picker,
approval prompt, autocomplete popup, or overlay is active, or when the
composer already holds a multi-line or soft-wrapped draft, `↑` / `↓` keep
their usual meaning and do not trigger recall.
