# Thoughts OpenCode Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give captured reasoning an OpenCode-style header — `+ Thought: 4.4s` collapsed, `- Thought: 4.4s` expanded — by tracking how long each reasoning phase actually took, and add `/thinking` as an alias for `/thoughts`. No change to `/think` (backend reasoning request) or to the global (not per-message) show/hide model.

**Architecture:** Duration is derived, not requested: the TUI already knows when reasoning text starts arriving (`EventReasoning`, or the `ThinkFilter`-extracted portion of an `EventDelta`) and when it stops (the first non-empty visible-answer delta). Two new `time.Time` fields on `Model` bracket that window; a small helper turns it into a `time.Duration` that keeps ticking live while still streaming and freezes once the turn finishes. The frozen value rides along on `provider.Message` (UI-only, like the existing `Reasoning` field) so history scrollback shows the real duration, not just the live one. `renderReasoning` gets that duration as a parameter and does the formatting; no new theme tokens, no per-message expand state (explicitly deferred — see Global Constraints).

**Tech Stack:** Go 1.26+, existing packages only (`internal/provider`, `internal/tui`). No new dependencies.

## Global Constraints

- Visibility stays **global** (`/thoughts show|hide|toggle`), not per-message. Per-answer expand/collapse would need transcript selection or mouse interaction — later enhancement, not this plan.
- `/think on|off|auto` (backend reasoning request) is untouched. `/thoughts` only ever affects local display.
- `provider.Message.ReasoningDuration` follows the same rule as `Reasoning`: UI-only, `json:"-" yaml:"-"`, never serialized, cached, persisted to history, or sent back to a backend.
- Collapsed and expanded headers both show the duration once it's known (confirmed design choice — not "duration only on expand"). While a duration genuinely isn't known yet (message loaded from before this feature, or a turn that produced no reasoning), fall back to a bare `Thought` with no colon.
- Header glyph: `+` when the current turn's reasoning is collapsed (`m.showReasoning == false`), `-` when expanded. Duration format is one decimal place in seconds (`%.1fs`), matching OpenCode — reuse `internal/tui/components.FormatElapsed` is wrong here (it rounds to whole seconds); write a small dedicated formatter instead.
- While still streaming, append `…` after the duration (e.g. `- Thought: 2.1s…`) so a live, ticking number reads as "still going" without reviving the old `· streaming` suffix text.
- Run `go fmt ./... && go vet ./... && go test ./...` before every commit.

---

## Task 1: Track reasoning-phase duration — done

**Files:**
- Modify: `internal/provider/provider.go:103-122` (`Message` struct)
- Modify: `internal/tui/app.go:113-129` (`Model` fields), `internal/tui/app.go:1836-1868` (`EventReasoning`/`EventDelta` capture), `internal/tui/app.go:2005-2036` (`streamFailed`), `internal/tui/app.go:2060-2088` (`finishStream`)
- Modify: `internal/tui/pipeline.go:751-761` (dispatch), `internal/tui/pipeline.go:893-902` (continueChat)
- Test: `internal/tui/think_test.go`

**Interfaces:**
- Produces: `provider.Message.ReasoningDuration time.Duration` (new field). `func (m *Model) reasoningDuration() time.Duration` — 0 if no reasoning happened this turn; otherwise elapsed time from the first captured reasoning byte to either the moment visible answer text started (finished turn) or `time.Now()` (still in progress). Task 2 calls this from the live-render path in `refreshViewport` and reads `msg.ReasoningDuration` for persisted messages.
- Consumes: nothing new — reuses the existing `EventReasoning`/`EventDelta`/`ThinkFilter` plumbing.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/think_test.go`:

```go
func TestReasoningDurationCapturedOnFinish(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventReasoning, Delta: "thinking...",
	}, ok: true, gen: m.streamGen})
	time.Sleep(5 * time.Millisecond)
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventDelta, Delta: "final answer",
	}, ok: true, gen: m.streamGen})
	m.finishStream(&provider.Usage{}, false)

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.ReasoningDuration < 5*time.Millisecond {
		t.Fatalf("ReasoningDuration = %v, want >= 5ms", last.ReasoningDuration)
	}
}

func TestReasoningDurationResetsBetweenTurns(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventReasoning, Delta: "first turn thinking",
	}, ok: true, gen: m.streamGen})
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventDelta, Delta: "first answer",
	}, ok: true, gen: m.streamGen})
	m.finishStream(&provider.Usage{}, false)

	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventDelta, Delta: "second answer, no reasoning this time",
	}, ok: true, gen: m.streamGen})
	m.finishStream(&provider.Usage{}, false)

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.ReasoningDuration != 0 {
		t.Fatalf("ReasoningDuration = %v, want 0 (this turn had no reasoning, and finishStream must reset the prior turn's window)", last.ReasoningDuration)
	}
}

func TestReasoningDurationCapturedViaThinkFilter(t *testing.T) {
	m := newTestModel(t)
	m.resetThinkFilter()
	feedDelta(t, m, "<think>because")
	time.Sleep(5 * time.Millisecond)
	feedDelta(t, m, " reasons</think>42")
	m.finishStream(&provider.Usage{}, false)

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.ReasoningDuration < 5*time.Millisecond {
		t.Fatalf("ReasoningDuration = %v, want >= 5ms (leaked <think> block path)", last.ReasoningDuration)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run TestReasoningDuration -v`
Expected: FAIL — `last.ReasoningDuration` undefined (no such field on `provider.Message` yet).

- [ ] **Step 3: Add the field to `provider.Message`**

In `internal/provider/provider.go`, right after the existing `Reasoning` field (around line 121):

```go
	// ReasoningDuration is UI-only timing for the captured Reasoning text —
	// wall-clock time from the first reasoning byte to the first visible
	// answer byte. Same rule as Reasoning: never serialized, cached,
	// persisted, or sent back to a backend.
	ReasoningDuration time.Duration `json:"-" yaml:"-"`
```

- [ ] **Step 4: Add tracking fields to `Model`**

In `internal/tui/app.go`, right after `reasoningBuf strings.Builder` (line 114):

```go
	reasoningBuf         strings.Builder
	reasoningStart       time.Time // zero until the first reasoning byte of the current turn
	reasoningEnd         time.Time // zero until the first visible-answer byte after reasoning
```

- [ ] **Step 5: Capture start/end in the stream handlers**

In `internal/tui/app.go`, the `EventReasoning` case (around line 1836):

```go
	case provider.EventReasoning:
		// The model is thinking (reasoning_content). It produces no visible
		// answer yet, but it is active — reset the idle deadline and show a
		// live indicator so a long thinking phase never looks frozen or times
		// out.
		reasoning := terminaltext.Sanitize(msg.event.Delta)
		if m.reasoningStart.IsZero() {
			m.reasoningStart = time.Now()
		}
		m.reasoningLen += len(reasoning)
		m.reasoningBuf.WriteString(reasoning)
```

(Only the two lines around `m.reasoningStart` are new; the rest of the case is unchanged.)

The `EventDelta` case (around line 1849) — note `m.reasoningEnd` is only set once real answer text exists, never just because a delta arrived (a `ThinkFilter` chunk can be pure reasoning with no answer output yet):

```go
	case provider.EventDelta:
		delta := terminaltext.Sanitize(msg.event.Delta)
		if m.thinkFilter != nil {
			answer, reasoning := m.thinkFilter.Feed(delta)
			if reasoning != "" {
				if m.reasoningStart.IsZero() {
					m.reasoningStart = time.Now()
				}
				m.reasoningLen += len(reasoning)
				m.filteredReasoningLen += len(reasoning)
				m.reasoningBuf.WriteString(reasoning)
			}
			delta = answer
		}
		if delta != "" {
			if !m.reasoningStart.IsZero() && m.reasoningEnd.IsZero() {
				m.reasoningEnd = time.Now()
			}
			m.streamBuf.WriteString(delta)
		}
```

- [ ] **Step 6: Add the `reasoningDuration` helper**

In `internal/tui/app.go`, right before `func (m *Model) finishStream(...)` (around line 2060):

```go
// reasoningDuration reports how long the current turn's reasoning phase has
// taken. It returns 0 if no reasoning was captured this turn. While
// reasoning is still in progress (reasoningEnd not yet set) it measures up
// to now, so a live re-render ticks the number up instead of freezing it.
func (m *Model) reasoningDuration() time.Duration {
	if m.reasoningStart.IsZero() {
		return 0
	}
	end := m.reasoningEnd
	if end.IsZero() {
		end = time.Now()
	}
	if end.Before(m.reasoningStart) {
		return 0
	}
	return end.Sub(m.reasoningStart)
}
```

- [ ] **Step 7: Wire the duration into `finishStream` and `streamFailed`, and reset it there**

In `internal/tui/app.go`, `finishStream` (around line 2063):

```go
	reply := m.streamBuf.String()
	m.streamBuf.Reset()
	reasoning := m.reasoningBuf.String()
	reasoningDuration := m.reasoningDuration()
	m.reasoningBuf.Reset()
	m.reasoningStart = time.Time{}
	m.reasoningEnd = time.Time{}
	m.filteredReasoningLen = 0
```

and its `session.AddMessage` call (around line 2081):

```go
		m.session.AddMessage(provider.Message{
			Role:              provider.RoleAssistant,
			Content:           reply,
			ToolCalls:         toolCalls,
			Reasoning:         reasoning,
			ReasoningDuration: reasoningDuration,
		})
```

In `internal/tui/app.go`, `streamFailed` (around line 2009):

```go
	// Preserve partial streamed output instead of discarding it.
	if partial := m.streamBuf.String(); partial != "" {
		m.session.AddMessage(provider.Message{
			Role:              provider.RoleAssistant,
			Content:           partial,
			Reasoning:         m.reasoningBuf.String(),
			ReasoningDuration: m.reasoningDuration(),
		})
		m.replyCount++
		m.streamBuf.Reset()
		m.errText += " (partial reply kept)"
	}
	m.reasoningBuf.Reset()
	m.reasoningStart = time.Time{}
	m.reasoningEnd = time.Time{}
	m.filteredReasoningLen = 0
```

- [ ] **Step 8: Reset at the start of a turn**

In `internal/tui/pipeline.go`, both reset blocks — after `m.reasoningBuf.Reset()` at line 756 and again at line 897 — add:

```go
	m.reasoningLen = 0
	m.reasoningBuf.Reset()
	m.reasoningStart = time.Time{}
	m.reasoningEnd = time.Time{}
	m.filteredReasoningLen = 0
```

(`time` is already imported in both `app.go` and `pipeline.go`.)

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/tui/ -run "TestReasoningDuration|TestLeakedThinkBlock|TestDedicatedReasoningEvent|TestProviderProgressIsNotCapturedAsReasoning" -v`
Expected: PASS (including the pre-existing reasoning tests — this step must not regress them).

- [ ] **Step 10: Commit**

```bash
git add internal/provider/provider.go internal/tui/app.go internal/tui/pipeline.go internal/tui/think_test.go
git commit -m "feat(tui): track reasoning-phase duration for the thoughts header"
```

---

## Task 2: OpenCode-style `+`/`-` header with duration — done

**Files:**
- Modify: `internal/tui/transcript_styles.go` (`renderReasoning`, new `formatThoughtDuration`)
- Modify: `internal/tui/app.go:2223-2226` (`appendReasoning` closure), `internal/tui/app.go:2309-2310`, `internal/tui/app.go:2369-2374` (call sites)
- Test: `internal/tui/think_test.go` (new tests + update `TestPromptRailAndReasoningVisibility`)

**Interfaces:**
- Consumes: `m.reasoningDuration()` and `provider.Message.ReasoningDuration` from Task 1.
- Produces: `func (m *Model) renderReasoning(reasoning string, streaming bool, duration time.Duration) string` (signature change — was `(reasoning string, streaming bool)`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/think_test.go`:

```go
func TestReasoningHeaderShowsDurationWhenKnown(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "answer text",
		Reasoning: "because X", ReasoningDuration: 4400 * time.Millisecond,
	})

	m.showReasoning = false
	m.refreshViewport()
	view := m.viewport.View()
	if !strings.Contains(view, "+ Thought: 4.4s") {
		t.Fatalf("collapsed header missing duration:\n%s", view)
	}
	if strings.Contains(view, "because X") {
		t.Fatalf("collapsed view must not leak the reasoning body:\n%s", view)
	}

	m.showReasoning = true
	m.refreshViewport()
	view = m.viewport.View()
	if !strings.Contains(view, "- Thought: 4.4s") {
		t.Fatalf("expanded header missing duration:\n%s", view)
	}
	if !strings.Contains(view, "because X") {
		t.Fatalf("expanded view missing reasoning body:\n%s", view)
	}
}

func TestReasoningHeaderTicksWhileStreaming(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.showReasoning = true
	m.thinking = true
	m.reasoningStart = time.Now().Add(-3 * time.Second)
	m.reasoningBuf.WriteString("still working on it")
	m.refreshViewport()

	view := m.viewport.View()
	if !strings.Contains(view, "- Thought: 3.") || !strings.Contains(view, "s…") {
		t.Fatalf("streaming header missing live ticking duration with ellipsis:\n%s", view)
	}
}
```

Also **update** the existing `TestPromptRailAndReasoningVisibility` (around line 102) — it predates the header format change:

```go
	view := m.viewport.View()
	for _, want := range []string{"┃ How does this work?", "Thought", "private scratch work", "It works carefully."} {
```

(was `"thought"`, now `"Thought"` — the message in this test has no `ReasoningDuration` set, so the header is the bare-fallback `- Thought` with no colon; `"Thought"` still matches it as a substring)

```go
	m.showReasoning = false
	m.refreshViewport()
	view = m.viewport.View()
	if strings.Contains(view, "private scratch work") {
		t.Fatalf("hidden reasoning remained visible:\n%s", view)
	}
	if !strings.Contains(view, "+ Thought · /thoughts show") {
		t.Fatalf("hidden reasoning lacks recovery hint:\n%s", view)
	}
```

(was `"thought · hidden · /thoughts show"`)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run "TestReasoningHeader|TestPromptRailAndReasoningVisibility" -v`
Expected: FAIL. The test file still compiles fine at this point (these tests only call `m.refreshViewport()`, never `renderReasoning` directly), and `renderReasoning` is still the old two-argument version from Task 1 — so the assertions fail on content: the view still contains the old `thought · hidden · /thoughts show` / bare `thought` text instead of the new `+`/`-` `Thought: …` header.

- [ ] **Step 3: Rewrite `renderReasoning` and add `formatThoughtDuration`**

Replace all of `internal/tui/transcript_styles.go`:

```go
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// renderPrompt marks only human prompts with a heavy left rail. Keeping the
// rest of the transcript borderless avoids spending rows on decorative UI and
// lets answers read like ordinary terminal output.
func (m *Model) renderPrompt(body string) string {
	body = strings.Trim(body, "\n")
	width := m.viewport.Width - m.theme.PromptRail.GetHorizontalFrameSize()
	if width < 1 {
		width = 1
	}
	return m.theme.PromptRail.Copy().Width(width).Render(body)
}

// renderReasoning renders a captured reasoning block's header (and, when
// expanded, its body). The header follows OpenCode's convention: a "+"
// (collapsed) or "-" (expanded) toggle glyph, the word "Thought", and — once
// known — the reasoning phase's duration to one decimal place ("Thought:
// 4.4s"). streaming appends "…" to a still-ticking duration; duration == 0
// means "not known yet" and falls back to a bare "Thought" with no colon
// (e.g. a message saved before this feature existed, or a turn that
// produced no reasoning at all).
func (m *Model) renderReasoning(reasoning string, streaming bool, duration time.Duration) string {
	glyph := "-"
	if !m.showReasoning {
		glyph = "+"
	}
	header := glyph + " Thought"
	if formatted := formatThoughtDuration(duration); formatted != "" {
		header += ": " + formatted
		if streaming {
			header += "…"
		}
	}
	if !m.showReasoning {
		return m.theme.ReasoningText.Render(header + " · /thoughts show")
	}
	body := strings.Trim(terminaltext.Sanitize(reasoning), "\n")
	if body == "" {
		return m.theme.ReasoningText.Render(header)
	}
	return m.theme.ReasoningText.Render(header + "\n" + body)
}

// formatThoughtDuration renders a reasoning-phase duration the way OpenCode
// does: one decimal place, always in seconds ("4.4s"). Returns "" for a
// non-positive/unknown duration so the caller can fall back to a bare label.
func formatThoughtDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func (m *Model) renderAnswer(answer string) string {
	return m.theme.AnswerText.Render(strings.TrimRight(answer, "\n"))
}
```

- [ ] **Step 4: Update the three call sites in `internal/tui/app.go`**

The `appendReasoning` closure inside `refreshViewport` (around line 2223):

```go
	appendReasoning := func(reasoning string, streaming bool, duration time.Duration) {
		b.WriteString(m.renderReasoning(reasoning, streaming, duration))
		b.WriteString("\n\n")
	}
```

The persisted-message call site (around line 2309):

```go
			if msg.Reasoning != "" {
				appendReasoning(msg.Reasoning, false, msg.ReasoningDuration)
			}
```

The two live-streaming call sites (around lines 2369-2374):

```go
		if m.reasoningBuf.Len() > 0 {
			appendReasoning(m.reasoningBuf.String(), true, m.reasoningDuration())
		} else if m.reasoningLen > 0 {
			// Reasoning model is still thinking; show progress so the wait
			// is visible rather than a frozen screen.
			appendReasoning(fmt.Sprintf("thinking… (%s of reasoning so far)", components.FormatTokens(m.reasoningLen/4)), true, m.reasoningDuration())
		}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tui/... -v -run "Reasoning|PromptRail|Think"`
Expected: PASS, all of them — including the older `TestLeakedThinkBlockIsStrippedFromReplyAndHistory`, `TestDedicatedReasoningEventIsCapturedSeparately`, etc. from before this plan.

Then run the full package to catch anything else touching `renderReasoning`'s old two-argument signature:

Run: `go build ./... && go test ./internal/tui/...`
Expected: builds clean, all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/transcript_styles.go internal/tui/app.go internal/tui/think_test.go
git commit -m "feat(tui): OpenCode-style +/- Thought header with duration"
```

---

## Task 3: `/thinking` alias for `/thoughts` — done

**Files:**
- Modify: `internal/tui/commands.go:131`
- Test: `internal/tui/commands_test.go`

**Interfaces:**
- Consumes: the existing `slashCommand.aliases []string` mechanism (already used by `/skills`↔`/skill` and `/plugins`↔`/plugin`; see `commands.go:154,157`). No new mechanism needed.

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/commands_test.go`:

```go
func TestThinkingIsAnAliasForThoughts(t *testing.T) {
	m := newTestModel(t)
	m.showReasoning = false

	runCommand(m, "/thinking show")

	if !m.showReasoning {
		t.Fatal("/thinking show should resolve via alias to /thoughts show")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestThinkingIsAnAliasForThoughts -v`
Expected: FAIL — `/thinking` is not recognized, `m.showReasoning` stays `false`.

- [ ] **Step 3: Add the alias**

In `internal/tui/commands.go`, line 131:

```go
		{name: "thoughts", aliases: []string{"thinking"}, usage: "/thoughts [show|hide|toggle|status]", desc: "show or hide captured reasoning without changing model behavior", category: "Model", run: cmdThoughts},
```

(only the `aliases: []string{"thinking"}, ` addition is new)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -run TestThinkingIsAnAliasForThoughts -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/commands.go internal/tui/commands_test.go
git commit -m "feat(tui): add /thinking as an alias for /thoughts"
```

---

## Acceptance criteria

- `go fmt ./... && go vet ./... && go test ./...` all pass after every task.
- A message with known `ReasoningDuration` renders `+ Thought: 4.4s` collapsed and `- Thought: 4.4s` (plus body) expanded — verified by `TestReasoningHeaderShowsDurationWhenKnown`.
- A message with no captured duration (pre-feature history, or a turn with no reasoning) falls back to a bare `+ Thought` / `- Thought`, never a stray `: 0.0s` — verified by the updated `TestPromptRailAndReasoningVisibility`.
- Live reasoning still in progress shows a ticking duration with a trailing `…` — verified by `TestReasoningHeaderTicksWhileStreaming`.
- Duration is captured correctly from both the native `EventReasoning` channel and the inline `<think>` leak path (`ThinkFilter`) — verified by `TestReasoningDurationCapturedOnFinish` and `TestReasoningDurationCapturedViaThinkFilter`.
- Duration resets cleanly between turns — verified by `TestReasoningDurationResetsBetweenTurns`.
- `/thinking show|hide|toggle|status` behaves identically to `/thoughts` — verified by `TestThinkingIsAnAliasForThoughts`.
- `/think on|off|auto` (backend reasoning request) and the global (not per-message) show/hide model are unchanged — no test should need to change for those.
- `ReasoningDuration` never appears in a saved session file or a request sent to a backend (it carries `json:"-" yaml:"-"`, same as `Reasoning`).

## Explicitly out of scope (per user's own follow-up note)

- Per-answer expand/collapse (would need transcript selection or mouse interaction).
- Any change to `/think`'s backend-facing behavior.
- A `question` tool / interactive agent pause-for-input mechanism — unrelated feature, discussed separately.
