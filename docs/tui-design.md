# TUI Design

llmtui aims for a calm, premium, terminal-native feel: keyboard-first,
readable at a glance, no flicker, no noise. It is inspired by modern
terminal coding assistants without copying any of them.

## Layout

Top to bottom, always full-screen (alt-screen mode):

```text
┌ chat viewport ──────────────── fills remaining height, scrolls
├ usage panel ────────────────── sparkline + prompt/reply/total tokens
├ suggestion popup ───────────── only while typing a / command
├ input box ──────────────────── grows 1→6 rows with content, then scrolls
├ status bar ─────────────────── ● status · provider · model · profile ·
│                                context use · session tokens · tok/s
└ help footer ────────────────── key hints; replaced by notices, errors,
                                 or the animated working/stop buttons
```

Overlays (`/help`, `/usage`, `/doctor`, pickers, …) render inside the
viewport area, scroll with `↑`/`↓`/`PgUp`/`PgDn`, and close with `Esc`,
`Enter`, or `q`. While an overlay is open, async events (stream progress,
health results) never overwrite it; the chat re-renders on close.

## Components

Status bar, provider/model badges, token meter, usage sparkline, bar chart
and GitHub-style heatmap (`/usage`), spinner + pulsing working/stop buttons,
attachment chips, error text, and the command suggestion popup all live in
`internal/tui/components` and are pure functions of theme + data, which
keeps them testable without a terminal.

## Theme and fallbacks

- One built-in theme today (`claude_inspired`), defined in
  `internal/tui/styles` as a `Theme` struct of Lip Gloss styles — new themes
  are a single constructor plus a `ByName` case.
- Every color is a lipgloss `AdaptiveColor` (light + dark variant), and Lip
  Gloss degrades TrueColor to 256/16-color terminals automatically.
- Charts fall back from Unicode block-eighths to plain ASCII when needed.
- No Nerd Font is required; the few symbols used (`●`, `▸`, `⌗`) are plain
  Unicode. Recommended fonts for best results: JetBrains Mono (Nerd Font),
  MesloLGS NF, Berkeley Mono, SF Mono.
- Markdown rendering uses a fixed Glamour style chosen once from the
  detected background — never a per-frame terminal query, which can stall
  odd terminals or SSH sessions.

## Behavior rules

- Streaming renders token-by-token into the viewport; the whole frame is
  composed with `lipgloss.JoinVertical`, so Bubble Tea diffs cleanly and
  nothing flickers.
- Reasoning models show a live `thinking…` indicator (with a running token
  estimate) while they produce hidden reasoning, so a long pre-answer pause
  reads as active work rather than a frozen screen.
- Resize recomputes every panel height; the viewport never collapses below
  three rows and the markdown renderer rebuilds only when the width changed.
- Mouse support (wheel scrolling) is an enhancement only; `Ctrl+O` releases
  the mouse so the terminal's native text selection works.
- Animations are subtle by design: one spinner, a pulsing working button,
  and nothing else moves while you read.
