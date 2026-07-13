# Qwen Reasoning Stability (Client-Side) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make llmtui robust against reasoning-model instability (Qwen 3.5/3.6, DeepSeek-R1, …) with two small model-agnostic client-side features, and document that the froggeric fixed chat template must be installed backend-side.

**Architecture:** Chat templates render **server-side** (LM Studio / llama.cpp / vLLM / Ollama), so llmtui must NOT implement any Jinja templating. Client-side we add: (1) a streaming `ThinkFilter` that reroutes a leaked leading `<think>…</think>` block out of the visible answer so it is never stored in history, re-sent, rendered, or cached; (2) an opt-in reasoning toggle (`chat.reasoning: auto|on|off`, `/think` command) sent as `chat_template_kwargs.enable_thinking` (OpenAI-compatible) / `think` (Ollama), omitted entirely in `auto` so no other model or backend is affected.

**Tech Stack:** Go 1.26+, existing packages only (`internal/provider`, `internal/tui`, `internal/config`, `internal/cache`). No new dependencies.

## Global Constraints

- Default behavior must be unchanged for non-reasoning models: `chat.reasoning` defaults to `"auto"` which omits all new wire fields; `chat.strip_leaked_thinking` defaults to `true` but only triggers on a literal `<think>` opening the reply.
- Cache-key completeness invariant (CLAUDE.md): the reasoning mode changes the request, so it MUST enter `cache.Key` and its `Hash`.
- Provider streams must still emit exactly one terminal event and close the channel — the filter lives in the TUI, not in providers' stream loops.
- Config precedence: flags > `LLMTUI_*` env > YAML > defaults. The new field uses the existing viper plumbing (no new flag needed).
- Run `go fmt ./... && go test ./... && go vet ./...` before every commit; `go test -race ./internal/tui/... ./internal/provider/...` after Tasks 2 and 4.
- Do NOT implement client-side chat templating, raw `/completions` calls, or an Ollama Modelfile generator. Out of scope.

---

### Task 1: `provider.ThinkFilter`

**Files:**
- Create: `internal/provider/thinkfilter.go`
- Test: `internal/provider/thinkfilter_test.go`

**Interfaces:**
- Consumes: nothing (pure state machine).
- Produces: `type ThinkFilter struct`, `func (f *ThinkFilter) Feed(delta string) (answer, reasoning string)`, `func (f *ThinkFilter) Flush() (answer, unclosedReasoning string)`. Task 2 constructs `&provider.ThinkFilter{}` per request and calls these.

- [x] **Step 1: Write the failing tests**

```go
package provider

import "testing"

// feedAll drives a filter with a sequence of deltas and returns the
// concatenated answer and reasoning outputs plus the flush results.
func feedAll(t *testing.T, deltas []string) (answer, reasoning, flushAnswer, unclosed string) {
	t.Helper()
	f := &ThinkFilter{}
	for _, d := range deltas {
		a, r := f.Feed(d)
		answer += a
		reasoning += r
	}
	fa, un := f.Flush()
	return answer, reasoning, fa, un
}

func TestThinkFilterPassthroughWithoutTags(t *testing.T) {
	a, r, fa, un := feedAll(t, []string{"Hello ", "world"})
	if a+fa != "Hello world" || r != "" || un != "" {
		t.Fatalf("got answer=%q reasoning=%q unclosed=%q", a+fa, r, un)
	}
}

func TestThinkFilterStripsLeadingThinkBlock(t *testing.T) {
	a, r, fa, un := feedAll(t, []string{"<think>\nstep one\n</think>\n\nThe answer is 4."})
	if a+fa != "The answer is 4." {
		t.Fatalf("answer = %q", a+fa)
	}
	if r == "" || un != "" {
		t.Fatalf("reasoning=%q unclosed=%q", r, un)
	}
}

func TestThinkFilterHandlesTagsSplitAcrossDeltas(t *testing.T) {
	a, r, fa, _ := feedAll(t, []string{"<thi", "nk>reason", "ing</thi", "nk>done"})
	if a+fa != "done" {
		t.Fatalf("answer = %q", a+fa)
	}
	if r != "reasoning" {
		t.Fatalf("reasoning = %q", r)
	}
}

func TestThinkFilterMidAnswerTagPassesThrough(t *testing.T) {
	a, _, fa, _ := feedAll(t, []string{"Use the <think> tag like this."})
	if a+fa != "Use the <think> tag like this." {
		t.Fatalf("answer = %q", a+fa)
	}
}

func TestThinkFilterLeadingWhitespaceThenThink(t *testing.T) {
	a, r, fa, _ := feedAll(t, []string{"\n <think>hm</think>ok"})
	if a+fa != "ok" || r != "hm" {
		t.Fatalf("answer=%q reasoning=%q", a+fa, r)
	}
}

func TestThinkFilterUnclosedBlockRecoveredOnFlush(t *testing.T) {
	a, _, fa, un := feedAll(t, []string{"<think>all budget spent thinking"})
	if a != "" || fa != "" {
		t.Fatalf("answer should be empty, got %q", a+fa)
	}
	if un != "all budget spent thinking" {
		t.Fatalf("unclosed = %q", un)
	}
}

func TestThinkFilterFlushReturnsUndecidedPrefix(t *testing.T) {
	// A reply that is only "<thi" (stream died mid-tag) must not be lost.
	_, _, fa, _ := feedAll(t, []string{"<thi"})
	if fa != "<thi" {
		t.Fatalf("flush answer = %q", fa)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/provider/ -run TestThinkFilter -v`
Expected: FAIL — `undefined: ThinkFilter`

- [x] **Step 3: Implement the filter**

```go
package provider

import "strings"

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// ThinkFilter separates a leaked leading <think>…</think> block from
// assistant answer deltas. Reasoning models served through a misconfigured
// backend chat template emit chain-of-thought inline in content instead of
// the dedicated reasoning_content/thinking channel; unfiltered it would be
// rendered as the answer, stored in session history, re-sent to the backend
// on every later turn, and cached. Only a block that opens the reply (after
// optional whitespace) is treated as reasoning — a literal "<think>" later
// in an answer passes through untouched.
type ThinkFilter struct {
	state    thinkState
	pending  strings.Builder // undecided prefix, or a held-back partial close tag
	thinkBuf strings.Builder // full reasoning text, kept for unclosed-block recovery
}

type thinkState int

const (
	thinkDeciding thinkState = iota
	thinkInside
	thinkPassthrough
)

// Feed consumes one streamed delta and returns the portion that is visible
// answer text and the portion that is reasoning. Either may be empty while
// the filter buffers a possible partial tag.
func (f *ThinkFilter) Feed(delta string) (answer, reasoning string) {
	if f.state == thinkPassthrough {
		return delta, ""
	}
	f.pending.WriteString(delta)
	if f.state == thinkDeciding {
		buf := f.pending.String()
		trimmed := strings.TrimLeft(buf, " \t\r\n")
		switch {
		case trimmed == "":
			return "", ""
		case strings.HasPrefix(trimmed, thinkOpen):
			f.state = thinkInside
			f.pending.Reset()
			f.pending.WriteString(trimmed[len(thinkOpen):])
		case strings.HasPrefix(thinkOpen, trimmed):
			// Could still become "<think>"; keep buffering.
			return "", ""
		default:
			f.state = thinkPassthrough
			f.pending.Reset()
			return buf, ""
		}
	}
	buf := f.pending.String()
	if i := strings.Index(buf, thinkClose); i >= 0 {
		reasoning = buf[:i]
		f.thinkBuf.WriteString(reasoning)
		answer = strings.TrimLeft(buf[i+len(thinkClose):], "\n")
		f.pending.Reset()
		f.state = thinkPassthrough
		return answer, reasoning
	}
	// Hold back the longest suffix that could be the start of "</think>"
	// split across deltas; release the rest as reasoning immediately so the
	// activity indicator keeps moving.
	hold := 0
	for n := min(len(buf), len(thinkClose)-1); n > 0; n-- {
		if strings.HasPrefix(thinkClose, buf[len(buf)-n:]) {
			hold = n
			break
		}
	}
	reasoning = buf[:len(buf)-hold]
	f.thinkBuf.WriteString(reasoning)
	f.pending.Reset()
	f.pending.WriteString(buf[len(buf)-hold:])
	return "", reasoning
}

// Flush ends the stream. It returns any buffered undecided text as answer,
// and — when the stream ended inside an unclosed think block — the complete
// reasoning text so the caller can salvage it instead of showing nothing.
func (f *ThinkFilter) Flush() (answer, unclosedReasoning string) {
	switch f.state {
	case thinkDeciding:
		answer = f.pending.String()
	case thinkInside:
		f.thinkBuf.WriteString(f.pending.String())
		unclosedReasoning = strings.TrimSpace(f.thinkBuf.String())
	}
	f.pending.Reset()
	f.state = thinkPassthrough
	return answer, unclosedReasoning
}
```

- [x] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/provider/ -run TestThinkFilter -v`
Expected: PASS (all 7)

- [x] **Step 5: Commit**

```bash
go fmt ./... && go vet ./internal/provider/
git add internal/provider/thinkfilter.go internal/provider/thinkfilter_test.go
git commit -m "feat(provider): add ThinkFilter for leaked inline <think> blocks"
```

---

### Task 2: Wire ThinkFilter into the TUI stream

**Files:**
- Modify: `internal/tui/app.go` (Model struct; `EventDelta` case near line 1425; `finishStream` near line 1513; `streamFailed` near line 1480)
- Modify: `internal/tui/pipeline.go` (`dispatch` near line 361 and `continueChat` near line 442, where `m.streamBuf.Reset()` / `m.reasoningLen = 0` already happen)
- Modify: `internal/config/config.go` (add `StripLeakedThinking` to `ChatConfig` near line 40; `v.SetDefault` block near line 440)
- Test: `internal/tui/think_test.go` (new)

**Interfaces:**
- Consumes: `provider.ThinkFilter` from Task 1.
- Produces: `Model.thinkFilter *provider.ThinkFilter` field; `func (m *Model) resetThinkFilter()`; `func (m *Model) flushThinkFilter()`; config field `Chat.StripLeakedThinking bool` (`strip_leaked_thinking`, default `true`).

- [x] **Step 1: Write the failing test**

Follow the existing pattern in `internal/tui/bugfix_test.go` for constructing a test `Model` (look at how those tests build a Model and feed `streamEventMsg`/call handlers — reuse the same helper style; do not invent a new harness). The behaviors to pin down:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// Leaked think block: content deltas containing <think>…</think> must not
// reach the visible answer or the stored session message.
func TestLeakedThinkBlockIsStrippedFromReplyAndHistory(t *testing.T) {
	m := newTestModel(t) // reuse/adapt the existing test-model constructor in this package
	m.resetThinkFilter()
	for _, d := range []string{"<think>because", " reasons</think>", "42"} {
		feedDelta(t, m, d) // adapt to the package's streamEventMsg plumbing
	}
	m.finishStream(&provider.Usage{})
	msgs := m.session.Messages
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant || last.Content != "42" {
		t.Fatalf("stored assistant content = %q", last.Content)
	}
	if m.reasoningLen == 0 {
		t.Fatal("reasoning activity was not counted")
	}
}

// Unclosed think block: the reply must be salvaged, not dropped.
func TestUnclosedThinkBlockIsSalvaged(t *testing.T) {
	m := newTestModel(t)
	m.resetThinkFilter()
	feedDelta(t, m, "<think>ran out of tokens mid-thought")
	m.finishStream(&provider.Usage{})
	msgs := m.session.Messages
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "ran out of tokens mid-thought") {
		t.Fatalf("unclosed reasoning lost: %q", last.Content)
	}
}

// Filter disabled by config: content passes through verbatim.
func TestStripLeakedThinkingCanBeDisabled(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Chat.StripLeakedThinking = false
	m.resetThinkFilter()
	feedDelta(t, m, "<think>x</think>y")
	m.finishStream(&provider.Usage{})
	msgs := m.session.Messages
	if last := msgs[len(msgs)-1]; last.Content != "<think>x</think>y" {
		t.Fatalf("content = %q", last.Content)
	}
}
```

If `newTestModel`/`feedDelta` helpers don't exist under those names, adapt the test to whatever `bugfix_test.go` and `app_test.go` actually use — the assertions are the contract, not the helper names.

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run "ThinkBlock|StripLeakedThinking" -v`
Expected: FAIL — `undefined: m.resetThinkFilter` (and friends)

- [x] **Step 3: Implement the wiring**

Config (`internal/config/config.go`): add to `ChatConfig`:

```go
	// StripLeakedThinking reroutes a leading <think>…</think> block that a
	// misconfigured backend leaks into content, so reasoning is never stored
	// in history, re-sent, or cached. Safe for non-reasoning models: it only
	// triggers on a literal <think> opening the reply.
	StripLeakedThinking bool `mapstructure:"strip_leaked_thinking" yaml:"strip_leaked_thinking"`
```

and in the defaults block: `v.SetDefault("chat.strip_leaked_thinking", true)`.

`internal/tui/app.go` — Model struct: add `thinkFilter *provider.ThinkFilter` next to `streamBuf`/`reasoningLen`. Add helpers:

```go
// resetThinkFilter arms a fresh filter for the next stream (nil when the
// user disabled stripping).
func (m *Model) resetThinkFilter() {
	if m.cfg.Chat.StripLeakedThinking {
		m.thinkFilter = &provider.ThinkFilter{}
	} else {
		m.thinkFilter = nil
	}
}

// flushThinkFilter drains the filter into streamBuf at end of stream. An
// unclosed think block is salvaged as the visible reply rather than dropped.
func (m *Model) flushThinkFilter() {
	if m.thinkFilter == nil {
		return
	}
	answer, unclosed := m.thinkFilter.Flush()
	m.thinkFilter = nil
	if answer != "" {
		m.streamBuf.WriteString(answer)
	}
	if m.streamBuf.Len() == 0 && unclosed != "" {
		m.streamBuf.WriteString("_(the model spent its reply inside an unclosed <think> block — showing it as-is)_\n\n")
		m.streamBuf.WriteString(unclosed)
	}
}
```

`EventDelta` case (app.go ~1425) becomes:

```go
	case provider.EventDelta:
		delta := msg.event.Delta
		if m.thinkFilter != nil {
			answer, reasoning := m.thinkFilter.Feed(delta)
			if reasoning != "" {
				m.reasoningLen += len(reasoning)
			}
			delta = answer
		}
		if delta != "" {
			m.streamBuf.WriteString(delta)
		}
		// A token arrived: the stream is healthy, so push the idle deadline out.
		if m.idleWatchdog != nil {
			m.idleWatchdog.Reset(m.idleTimeout)
		}
		m.refreshViewport()
		return m, waitForEvent(m.stream)
```

`finishStream` (~1513): insert `m.flushThinkFilter()` as the first statement after `m.thinking = false`, before `reply := m.streamBuf.String()`.

`streamFailed` (~1480): insert `m.flushThinkFilter()` before `if partial := m.streamBuf.String(); partial != "" {`.

`internal/tui/pipeline.go`: in `dispatch` and `continueChat`, directly after each existing `m.reasoningLen = 0`, add `m.resetThinkFilter()`. Grep `reasoningLen = 0` to catch every reset site.

- [x] **Step 4: Run tests**

Run: `go test ./internal/tui/ -v -run "ThinkBlock|StripLeakedThinking"` then `go test -race ./internal/tui/...`
Expected: PASS, no regressions in the full package

- [x] **Step 5: Commit**

```bash
go fmt ./... && go vet ./internal/tui/ ./internal/config/
git add internal/tui/ internal/config/config.go
git commit -m "feat(tui): strip leaked <think> blocks from replies, history, and cache"
```

---

### Task 3: Reasoning mode — config, request field, cache key

**Files:**
- Modify: `internal/config/config.go` (`ChatConfig` ~line 40, defaults ~line 440)
- Modify: `internal/provider/provider.go` (`ChatRequest` ~line 78)
- Modify: `internal/cache/cache.go` (`Key` ~line 31 and its `Hash` method — add the field exactly the way `Template` is hashed)
- Modify: `internal/tui/pipeline.go` (`buildRequest` ~line 424, `cacheKey` ~line 244)
- Modify: `internal/tui/app.go` (Model struct: session override field)
- Test: `internal/tui/think_test.go` (extend), `internal/cache` existing key-completeness test file (extend)

**Interfaces:**
- Consumes: nothing new.
- Produces: `ChatConfig.Reasoning string` (`reasoning`, default `"auto"`); `provider.ChatRequest.Reasoning string` (`""` = omit, `"on"`, `"off"`); `Model.reasoningMode string` session override; `func (m *Model) effectiveReasoning() string` returning `"auto"|"on"|"off"`; `cache.Key.Reasoning string`. Task 4 reads `ChatRequest.Reasoning`; Task 5 sets `Model.reasoningMode`.

- [x] **Step 1: Write the failing tests**

In `internal/tui/think_test.go`:

```go
func TestEffectiveReasoningPrecedence(t *testing.T) {
	m := newTestModel(t)
	if got := m.effectiveReasoning(); got != "auto" {
		t.Fatalf("default = %q, want auto", got)
	}
	m.cfg.Chat.Reasoning = "off"
	if got := m.effectiveReasoning(); got != "off" {
		t.Fatalf("config = %q, want off", got)
	}
	m.reasoningMode = "on" // session override wins
	if got := m.effectiveReasoning(); got != "on" {
		t.Fatalf("override = %q, want on", got)
	}
	m.cfg.Chat.Reasoning = "bogus"
	m.reasoningMode = ""
	if got := m.effectiveReasoning(); got != "auto" {
		t.Fatalf("invalid config value = %q, want auto", got)
	}
}

func TestBuildRequestCarriesReasoning(t *testing.T) {
	m := newTestModel(t)
	m.reasoningMode = "off"
	req := m.buildRequest(nil)
	if req.Reasoning != "off" {
		t.Fatalf("req.Reasoning = %q", req.Reasoning)
	}
	m.reasoningMode = "auto"
	if req := m.buildRequest(nil); req.Reasoning != "" {
		t.Fatalf("auto must map to empty, got %q", req.Reasoning)
	}
}
```

In the cache package's existing key test file, extend the key-completeness test (there is one per CLAUDE.md — find it with `grep -rn "Key{" internal/cache/*_test.go`) so two keys differing only in `Reasoning` produce different hashes:

```go
func TestKeyHashVariesWithReasoning(t *testing.T) {
	a := Key{Provider: "p", Model: "m", UserMessage: "hi"}
	b := a
	b.Reasoning = "off"
	if a.Hash() == b.Hash() {
		t.Fatal("Reasoning must vary the cache key")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ ./internal/cache/ -run "Reasoning" -v`
Expected: FAIL — undefined fields/methods

- [x] **Step 3: Implement**

`internal/config/config.go` — add to `ChatConfig`:

```go
	// Reasoning controls the thinking mode of reasoning models (Qwen,
	// DeepSeek-R1, …): "auto" sends nothing and leaves it to the backend;
	// "on"/"off" request or suppress thinking explicitly (OpenAI-compatible
	// chat_template_kwargs.enable_thinking, Ollama think).
	Reasoning string `mapstructure:"reasoning" yaml:"reasoning"`
```

Default: `v.SetDefault("chat.reasoning", "auto")`.

`internal/provider/provider.go` — add to `ChatRequest` after `Stream`:

```go
	// Reasoning, when "on" or "off", explicitly requests or suppresses a
	// reasoning model's thinking phase. Empty means backend default: the
	// provider must omit the corresponding wire field entirely.
	Reasoning string
```

`internal/cache/cache.go` — add `Reasoning string` to `Key` and hash it in `Hash` exactly like the neighboring string fields (same `writeField`-style call the other fields use — mirror `Template`).

`internal/tui/app.go` — add `reasoningMode string` to Model near `template`.

`internal/tui/pipeline.go`:

```go
// effectiveReasoning resolves the reasoning mode: session override first,
// then config, normalizing anything unknown to "auto".
func (m *Model) effectiveReasoning() string {
	v := m.reasoningMode
	if v == "" {
		v = m.cfg.Chat.Reasoning
	}
	switch v {
	case "on", "off":
		return v
	}
	return "auto"
}
```

In `buildRequest`, add to the literal:

```go
	reasoning := m.effectiveReasoning()
	if reasoning == "auto" {
		reasoning = ""
	}
```

…and set `Reasoning: reasoning` in the `provider.ChatRequest`. In `cacheKey`, add `Reasoning: m.effectiveReasoning(),` to the `cache.Key` literal.

- [x] **Step 4: Run tests**

Run: `go test ./internal/tui/ ./internal/cache/ ./internal/config/ -v -run "Reasoning|Key"`
Expected: PASS

- [x] **Step 5: Commit**

```bash
go fmt ./... && go vet ./internal/...
git add internal/config/config.go internal/provider/provider.go internal/cache/cache.go internal/tui/
git commit -m "feat(chat): add reasoning mode (auto/on/off) through config, request, and cache key"
```

---

### Task 4: Providers send the reasoning field on the wire

**Files:**
- Modify: `internal/provider/openai/openai.go` (`chatRequest` struct ~line 170 and the site that fills it)
- Modify: `internal/provider/ollama/ollama.go` (`chatRequest` struct ~line 135 and the site that fills it)
- Test: `internal/provider/openai/openai_test.go`, `internal/provider/ollama/ollama_test.go` (extend, using the existing httptest patterns in those files)

**Interfaces:**
- Consumes: `provider.ChatRequest.Reasoning` from Task 3.
- Produces: OpenAI-compatible body gains `"chat_template_kwargs":{"enable_thinking":bool}` only when Reasoning is set; Ollama body gains `"think":bool` only when set.

- [x] **Step 1: Write the failing tests**

Follow the existing httptest capture pattern in each `*_test.go` (they already decode request bodies). Add to `internal/provider/openai/openai_test.go`:

```go
func TestChatSendsEnableThinkingWhenReasoningSet(t *testing.T) {
	for _, tc := range []struct {
		reasoning string
		want      string // substring of the raw body, or "" for absent
	}{
		{"", ""},
		{"on", `"chat_template_kwargs":{"enable_thinking":true}`},
		{"off", `"chat_template_kwargs":{"enable_thinking":false}`},
	} {
		body := captureChatBody(t, provider.ChatRequest{Model: "m", Reasoning: tc.reasoning}) // adapt to this file's existing capture helper
		if tc.want == "" {
			if strings.Contains(body, "chat_template_kwargs") {
				t.Fatalf("reasoning=%q: body must omit chat_template_kwargs: %s", tc.reasoning, body)
			}
			continue
		}
		if !strings.Contains(body, tc.want) {
			t.Fatalf("reasoning=%q: body missing %s: %s", tc.reasoning, tc.want, body)
		}
	}
}
```

Mirror for Ollama in `internal/provider/ollama/ollama_test.go` asserting `"think":true` / `"think":false` / absent.

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/provider/... -run "Thinking|Think" -v`
Expected: FAIL — field not sent

- [x] **Step 3: Implement**

OpenAI (`internal/provider/openai/openai.go`), in `chatRequest`:

```go
	// ChatTemplateKwargs passes template variables to backends that support
	// them (vLLM, llama.cpp server). Only enable_thinking is set, and only
	// when the user chose an explicit reasoning mode; backends that don't
	// know the field ignore it.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
```

Where the struct is populated from `provider.ChatRequest`:

```go
	switch req.Reasoning {
	case "on":
		body.ChatTemplateKwargs = map[string]any{"enable_thinking": true}
	case "off":
		body.ChatTemplateKwargs = map[string]any{"enable_thinking": false}
	}
```

Ollama (`internal/provider/ollama/ollama.go`), in `chatRequest`:

```go
	// Think toggles a reasoning model's thinking phase (Ollama native
	// field). nil omits it so non-reasoning models are unaffected.
	Think *bool `json:"think,omitempty"`
```

Population:

```go
	switch req.Reasoning {
	case "on":
		think := true
		body.Think = &think
	case "off":
		think := false
		body.Think = &think
	}
```

- [x] **Step 4: Run tests**

Run: `go test -race ./internal/provider/...`
Expected: PASS

- [x] **Step 5: Commit**

```bash
go fmt ./... && go vet ./internal/provider/...
git add internal/provider/openai/ internal/provider/ollama/
git commit -m "feat(provider): send explicit reasoning toggle (chat_template_kwargs / think)"
```

---

### Task 5: `/think` slash command

**Files:**
- Modify: `internal/tui/commands.go` (register in the command table, category "Model", after `/profile` ~line 123)
- Modify or create the handler alongside the other `cmd*` funcs (`internal/tui/commands_local.go` or `commands.go`, matching where `cmdProfile` lives)
- Test: `internal/tui/commands_test.go` (extend, following its existing command-dispatch test pattern)

**Interfaces:**
- Consumes: `Model.reasoningMode`, `effectiveReasoning()` from Task 3.
- Produces: `/think [on|off|auto|status]` command; `func cmdThink(m *Model, args string) tea.Cmd`.

- [x] **Step 1: Write the failing test**

```go
func TestThinkCommand(t *testing.T) {
	m := newTestModel(t)
	runSlash(t, m, "/think off") // adapt to this file's existing dispatch helper
	if m.reasoningMode != "off" {
		t.Fatalf("reasoningMode = %q", m.reasoningMode)
	}
	runSlash(t, m, "/think auto")
	if m.reasoningMode != "auto" {
		t.Fatalf("reasoningMode = %q", m.reasoningMode)
	}
	runSlash(t, m, "/think banana")
	if m.errText == "" {
		t.Fatal("invalid mode must set an error")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestThinkCommand -v`
Expected: FAIL — unknown command

- [x] **Step 3: Implement**

Registration (category "Model", `blockWhileThinking: true`):

```go
	{name: "think", usage: "/think [on|off|auto|status]", desc: "reasoning models: request, suppress, or default the thinking phase", category: "Model", blockWhileThinking: true, run: cmdThink},
```

Handler (mirror the style and system-notice mechanism of neighboring handlers — look at how `cmdCache` reports status back into the transcript):

```go
func cmdThink(m *Model, args string) tea.Cmd {
	mode := strings.TrimSpace(strings.ToLower(args))
	switch mode {
	case "", "status":
		note := fmt.Sprintf("reasoning mode: %s (session %q, config %q) — auto sends nothing and leaves it to the backend",
			m.effectiveReasoning(), m.reasoningMode, m.cfg.Chat.Reasoning)
		m.addSystemNotice(note) // adapt to the actual notice helper used by sibling commands
		return nil
	case "on", "off", "auto":
		m.reasoningMode = mode
		m.addSystemNotice("reasoning mode set to " + mode + " for this session")
		return nil
	default:
		m.errText = "usage: /think [on|off|auto|status]"
		return nil
	}
}
```

- [x] **Step 4: Run tests**

Run: `go test ./internal/tui/ -run TestThinkCommand -v` then `go test ./internal/tui/...`
Expected: PASS

- [x] **Step 5: Commit**

```bash
go fmt ./... && go vet ./internal/tui/
git add internal/tui/
git commit -m "feat(tui): add /think command to control reasoning mode per session"
```

---

### Task 6: Documentation

**Files:**
- Modify: `docs/providers.md` (new "Reasoning models (Qwen 3.5/3.6, DeepSeek-R1)" section)
- Modify: `docs/configuration.md` (`chat.reasoning`, `chat.strip_leaked_thinking`)
- Modify: `docs/slash-commands.md` (`/think`)
- Modify: `README.md` (one troubleshooting bullet)

**Interfaces:** none — prose only, but it must match the exact names shipped in Tasks 2–5.

- [x] **Step 1: Write the providers doc section**

Add to `docs/providers.md`:

```markdown
## Reasoning models (Qwen 3.5 / 3.6, DeepSeek-R1)

llmtui talks to every backend through structured chat APIs
(`/v1/chat/completions`, Ollama `/api/chat`). The **chat template — the
Jinja program that turns messages into the model's token stream — is
applied by the backend, never by llmtui.** If a Qwen 3.5/3.6 model is slow
or unstable (degenerate reasoning loops, stalled tool calls, KV-cache
thrash making every turn slower), fix the template in the backend:

The official Qwen 3.5/3.6 templates have known bugs; the community-fixed
drop-in replacement is
[froggeric/Qwen-Fixed-Chat-Templates](https://huggingface.co/froggeric/Qwen-Fixed-Chat-Templates):

- **LM Studio**: My Models → model settings → Prompt tab → replace the
  template with the contents of `chat_template.jinja` → Save.
- **llama.cpp / koboldcpp**: `--jinja --chat-template-file chat_template.jinja`
- **vLLM**: replace `chat_template` in `tokenizer_config.json`; serve with
  `--tool-call-parser qwen3_coder`.
- **Ollama**: not supported — Ollama uses Go templates, not Jinja. Rely on
  Ollama's own model templates and keep them updated (`ollama pull`).

What llmtui does client-side, for any reasoning model:

- Strips a leaked leading `<think>…</think>` block out of the answer
  (`chat.strip_leaked_thinking`, default `true`), so broken-template
  reasoning is never stored in history, re-sent each turn, or cached.
- `/think on|off|auto` (or `chat.reasoning`) requests or suppresses the
  thinking phase explicitly: OpenAI-compatible backends receive
  `chat_template_kwargs: {"enable_thinking": …}` (honored by vLLM and
  llama.cpp server, ignored elsewhere), Ollama receives `think`. `auto`
  sends nothing. Note: Ollama returns an error if `think` is set for a
  model without thinking support — use `auto` there.
```

- [x] **Step 2: Update configuration, slash-command, and README docs**

`docs/configuration.md`, in the `chat:` section table/list:

```markdown
- `reasoning` (default `auto`): `auto` | `on` | `off` — explicit thinking
  toggle for reasoning models; `auto` sends nothing.
- `strip_leaked_thinking` (default `true`): reroute a leading
  `<think>…</think>` block leaked into content by a misconfigured backend
  template out of the visible answer, history, and cache.
```

`docs/slash-commands.md`: add `/think [on|off|auto|status]` under the Model category with the same description as the command registration.

`README.md` troubleshooting: add one bullet:

```markdown
- **Qwen 3.5/3.6 slow or looping?** The fix is usually the backend's chat
  template, not llmtui — see "Reasoning models" in `docs/providers.md`.
```

- [x] **Step 3: Verify and commit**

Run: `go build ./... && go test ./...` (docs don't compile, but confirm nothing else broke) — Expected: PASS

```bash
git add docs/providers.md docs/configuration.md docs/slash-commands.md README.md
git commit -m "docs: reasoning-model guidance (Qwen chat template fix is backend-side)"
```

---

## Final validation (after all tasks)

```bash
go fmt ./...
go vet ./...
go test -race ./...
```

All green, working tree clean, 6 commits.
