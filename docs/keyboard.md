# Keyboard

| Key | Action |
| --- | --- |
| `Enter` | Send message / run command |
| `Shift+Enter` | Newline — only on terminals with distinguishable modified keys (see below) |
| `Alt/Option+Enter` | Newline — needs "Option as Meta" on macOS terminals |
| `Ctrl+J` | Newline — works in **every** terminal |
| `\` + `Enter` | Newline — trailing backslash continues the line, works everywhere |
| `Ctrl+U` | Clear the whole prompt box in one keystroke |
| `Ctrl+C` ×2 | Quit (first press stops generation / clears input) |
| `Esc` | Stop generation · close overlay |
| `↑` / `↓` | Choose an item in `/models` and `/providers` |
| `Enter` in picker | Switch to the selected model or provider |

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
