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
`Enter`, or `q`. The `/models` and `/providers` pickers instead use `↑`/`↓`
to move the selection, `Enter` to switch, and `Esc` to cancel; navigating a
long picker list keeps the selected row scrolled into view instead of
resetting to the top. While an overlay is open, async events (stream
progress, health results) never overwrite it; a resize rebuilds its content
at the new width instead of leaving it stale, and the chat re-renders on
close. `/usage` additionally splits into three tabs — Activity (heatmap),
All time (bar chart + stats), Models (per-model breakdown) — cycled with
`←`/`→` (wrapping both directions, resetting scroll to the top on switch),
and binds `r` to cycle a date range shared across all three tabs (full
history / last 7 days / last 30 days). Every tab's content is re-sliced
from data fetched once at open time, not re-read from disk on every
keypress or resize.

Ordinary typed keys, including `PgUp`/`PgDn`, go to the composer, not the
chat transcript — the transcript is normally reachable only by mouse wheel.
`F6` toggles a keyboard-only transcript navigation mode: `↑`/`↓`/`PgUp`/
`PgDn`/`Home`/`End` scroll the chat instead, until `F6` (or any other key)
returns them to the composer. A pending tool approval and busy-state
cancellation (`Esc`) still take priority over this mode.

Each reasoning block's `+`/`-` `Thought` header is clickable: it toggles
`ui.show_reasoning` for the whole session, the same as `/thoughts show|hide`.
The header renders in the accent color (bold, not underlined — see
`renderReasoning`'s comment for why) so it reads as interactive against the
muted reasoning body. A click there sits inside the same viewport region
click-drag text selection uses, so only a plain click (no movement between
press and release) toggles; a real drag that happens to pass over or end on
a header still finalizes as a text selection.

Tool results use the same bold caption style. Click the `⎿  +` output
summary to expand that result and inspect its full arguments and output;
click again to collapse it. Other results stay unchanged. `/tools output`
resets individual choices and toggles all output. Dragging across a caption
still selects text, and expansion keeps the current scroll position.

## Components

Status bar, provider/model badges, token meter, usage sparkline, bar chart
and GitHub-style heatmap (`/usage`), spinner + pulsing working/stop buttons,
attachment chips, error text, and the command suggestion popup all live in
`internal/tui/components` and are pure functions of theme + data, which
keeps them testable without a terminal.

## Theme and fallbacks

- Three built-in themes, defined in `internal/tui/styles` as `Theme`
  structs of Lip Gloss styles: `claude_inspired` (default, warm orange
  accent), `midnight` (cool indigo accent, cyan user rail), and `forest`
  (mossy-olive accent, warm amber user rail). Select with `ui.theme` in
  config or `--theme`. A new theme is a single constructor (built on the
  shared `newTheme` helper, which derives every style from eight base
  colors) plus a `ByName` case.
- Every color is a lipgloss `AdaptiveColor` (light + dark variant), and Lip
  Gloss degrades TrueColor to 256/16-color terminals automatically.
- Charts fall back from Unicode block-eighths to plain ASCII when needed.
- No Nerd Font is required; the few symbols used (`●`, `▸`, `⌗`) are plain
  Unicode. Recommended fonts for best results: JetBrains Mono (Nerd Font),
  MesloLGS NF, Berkeley Mono, SF Mono.
- Markdown rendering uses a fixed Glamour style chosen once from the
  detected background — never a per-frame terminal query, which can stall
  odd terminals or SSH sessions.
- Answer rendering pipeline: raw response → `terminaltext.Sanitize` →
  (optional) `terminalmath.ExpandMarkdown` when `ui.math.enabled` → Glamour →
  viewport. Math expansion is display-only and only runs on settled messages,
  not mid-stream; an incomplete `$…` simply shows raw until the message
  finishes. See [configuration.md](configuration.md).

## Exit summary

Quitting the chat (`Ctrl+C` twice or `/quit`) leaves a session report in the
terminal scrollback after the alt screen closes, the way modern agent CLIs
sign off: session ID, messages sent/replies received, cache hits, wall time
vs. API time, average generation speed, and a per-model table of requests
with input/output token totals (marked `~` when the provider returned no
usage and the counts are estimated). When the session was auto-saved, a
final hint points at `llmtui history`. The renderer lives in
`internal/tui/exitsummary.go` as a pure function of theme + data.

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
- Composer history (`internal/tui/composer_history.go`): every accepted
  submission — prompt or slash command — is appended to a bounded
  (`maxComposerHistoryEntries`), session-local, in-memory buffer, captured
  just before the textarea is reset in `send()` / `runSlashCommand()`.
  Arbitration of `↑` / `↓` runs in `Model.Update` *after* the modal owners
  (approval, `/keys`, overlays and pickers) and *before* the slash-suggestion
  popup: only bare `up`/`down` are eligible; while browsing a recalled entry
  the keys move the textarea cursor until it hits the top/bottom visual row
  (`LineInfo`-based, soft-wrap aware) and then step to the older/newer entry;
  while not browsing, `↑` on an *empty* composer enters history at the newest
  entry and every other case is left to the textarea. Any edit to a recalled
  entry detaches from browsing. Nothing is persisted; the buffer is unrelated
  to `internal/history` and survives `/clear`.
