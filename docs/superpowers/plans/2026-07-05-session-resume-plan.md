# Session Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `llmtui chat --resume <name>` and `llmtui chat --continue`/`-c` launch straight into a previously saved session instead of requiring `/history load <name>` once inside the TUI.

**Architecture:** A new `history.Latest` finds the most recently saved session (for `--continue`); `tui.Options` gains `ResumeSession`/`ResumeSessionName` fields that `New()` adopts via a shared `adoptSession` method (also used by the existing `/history load`); `cli/chat.go` resolves and validates the flags before the TUI ever starts, so a bad name or missing history fails with a normal CLI error and non-zero exit.

**Tech Stack:** Go, Cobra (flag parsing, `MarkFlagsMutuallyExclusive`), Bubble Tea (unchanged), existing `internal/history` package.

## Global Constraints

- Session names keep the existing timestamp scheme (`session-20260702-163005`) — no UUIDs, no migration.
- No interactive resume picker in this pass — `--resume` requires an explicit name.
- Resuming restores conversation content only (messages, token totals, session name) — it never overrides provider/model selection; normal `--provider`/`--model`/env/config precedence is untouched.
- `--resume`/`--continue` validate against `chat.history_dir` regardless of `chat.save_history` (matches `llmtui history`'s existing behavior).
- `--resume` and `--continue` are flags on `chat` only, and are mutually exclusive.
- No new `internal/cli` tests — that package has none today because `RunE` launches a real interactive Bubble Tea program via `tui.Run`, which isn't unit-testable. Task 3's verification is a manual smoke-test script instead.

Spec: `docs/superpowers/specs/2026-07-05-session-resume-design.md`

---

### Task 1: `history.Latest`

**Files:**
- Modify: `internal/history/history.go`
- Test: `internal/history/history_test.go`

**Interfaces:**
- Consumes: existing `List(dir string) ([]Meta, error)` and `Load(dir, name string) (Session, error)` (both already in this file).
- Produces: `Latest(dir string) (name string, s Session, err error)` — used by Task 3.

- [ ] **Step 1: Write the failing tests**

Add to the end of `internal/history/history_test.go` (after `TestListNewestFirstAndSkipsForeignFiles`, before `TestListMissingDir`):

```go
func TestLatestReturnsNewestSession(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "old", Session{Model: "m", Prompt: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := Save(dir, "new", Session{Model: "m", Prompt: 2}); err != nil {
		t.Fatal(err)
	}
	name, s, err := Latest(dir)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if name != "new" {
		t.Errorf("name = %q, want new", name)
	}
	if s.Prompt != 2 {
		t.Errorf("loaded session = %+v, want the newest save", s)
	}
}

func TestLatestSingleSession(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "only", Session{Model: "solo"}); err != nil {
		t.Fatal(err)
	}
	name, s, err := Latest(dir)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if name != "only" || s.Model != "solo" {
		t.Errorf("Latest = (%q, %+v), want only/solo", name, s)
	}
}

func TestLatestNoSessionsErrors(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Latest(dir); err == nil {
		t.Fatal("Latest should error when no sessions are saved")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/history/... -run TestLatest -v`
Expected: FAIL with `undefined: Latest` (compile error) for all three.

- [ ] **Step 3: Implement `Latest`**

In `internal/history/history.go`, add this function immediately after `List` (after its closing `}`, currently the last function in the file):

```go
// Latest returns the most recently saved session (by SavedAt) and the name
// it was saved under, for --continue. Returns an error if dir has no saved
// sessions.
func Latest(dir string) (name string, s Session, err error) {
	metas, err := List(dir)
	if err != nil {
		return "", Session{}, err
	}
	if len(metas) == 0 {
		return "", Session{}, fmt.Errorf("no saved sessions in %s", dir)
	}
	name = metas[0].Name
	s, err = Load(dir, name)
	return name, s, err
}
```

No new imports needed — `fmt` is already imported in this file.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/history/... -v`
Expected: PASS for all tests in the package, including the three new ones and every pre-existing test (`TestSaveLoadRoundTrip`, `TestSaveOverwritesSameName`, `TestSaveIsAtomic`, `TestListNewestFirstAndSkipsForeignFiles`, `TestListMissingDir`, `TestUsageAppendReadAggregate`, `TestReadUsageSkipsMalformedLines`, `TestReadUsageMissingFile`, `TestExpandHome`, `TestNamesCannotEscapeHistoryDir`).

- [ ] **Step 5: Commit**

```bash
git add internal/history/history.go internal/history/history_test.go
git commit -m "feat(history): add Latest for resuming the most recent session"
```

---

### Task 2: `tui.Options` resume fields + shared `adoptSession`

**Files:**
- Modify: `internal/tui/app.go:38-43` (the `Options` struct), `internal/tui/app.go:173-225` (`New`)
- Modify: `internal/tui/commands_local.go:847-869` (`cmdHistory`'s `"load"` case)
- Test: `internal/tui/bugfix_test.go`

**Interfaces:**
- Consumes: `history.Session` (existing type).
- Produces: `Options.ResumeSession *history.Session`, `Options.ResumeSessionName string`, and `(m *Model) adoptSession(name string, s history.Session)` — Task 3 sets the two `Options` fields; `/history load` and `New` both call `adoptSession`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/bugfix_test.go` (this file already imports `config`, `history`, and `provider` — add `"github.com/patrikcze/llmtui/internal/provider/mock"` to its import block too). Add these two tests after `TestHistoryLoadAdoptsSession`:

```go
// --resume/--continue (llmtui chat flags) seed a fresh Model with a saved
// session via Options.ResumeSession, using the same adoption logic as
// /history load above (TestHistoryLoadAdoptsSession).
func TestResumeOptionAdoptsSession(t *testing.T) {
	cfg := &config.Config{
		Chat:    config.ChatConfig{Stream: true, MaxTokens: 128, SystemPrompt: "You are a helpful local assistant."},
		Network: config.NetworkConfig{Timeout: "120s", ConnectTimeout: "10s"},
		Cache:   config.CacheConfig{TTL: "1h", MaxSizeMB: 16},
	}
	saved := history.Session{
		Provider: "mock",
		Model:    "demo-model",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "old question"},
			{Role: provider.RoleAssistant, Content: "old answer"},
		},
		Prompt: 11,
		Reply:  22,
	}
	m := New(Options{
		Config:            cfg,
		Provider:          mock.New(),
		Model:             "demo-model",
		ResumeSession:     &saved,
		ResumeSessionName: "session-old",
	})
	if m.sessionName != "session-old" {
		t.Errorf("sessionName = %q, want session-old", m.sessionName)
	}
	if len(m.session.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2", len(m.session.Messages))
	}
	if m.session.TotalPromptTokens != 11 || m.session.TotalCompletionTokens != 22 {
		t.Errorf("totals = %d/%d, want 11/22", m.session.TotalPromptTokens, m.session.TotalCompletionTokens)
	}
	if m.notice == "" || !strings.Contains(m.notice, "session-old") {
		t.Errorf("notice = %q, want a resume confirmation mentioning session-old", m.notice)
	}
}

// A fresh Model with no ResumeSession must be unaffected: it starts with
// just the configured system prompt and no leftover notice.
func TestNoResumeOptionStartsFreshSession(t *testing.T) {
	m := newTestModel(t)
	if len(m.session.Messages) != 1 || m.session.Messages[0].Role != provider.RoleSystem {
		t.Errorf("Messages = %+v, want just the configured system prompt", m.session.Messages)
	}
	if m.notice != "" {
		t.Errorf("notice = %q, want empty when nothing was resumed", m.notice)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/... -run 'TestResumeOptionAdoptsSession|TestNoResumeOptionStartsFreshSession' -v`
Expected: FAIL — `TestResumeOptionAdoptsSession` won't compile (`unknown field ResumeSession in struct literal`); `TestNoResumeOptionStartsFreshSession` compiles but fails its assertions aren't reachable yet either way since the package won't build.

- [ ] **Step 3: Add the `Options` fields**

In `internal/tui/app.go`, replace the `Options` struct (lines 38-43):

```go
type Options struct {
	Config     *config.Config
	Provider   provider.Provider
	Model      string
	ConfigPath string // path of the loaded config file, for /config
}
```

with:

```go
type Options struct {
	Config     *config.Config
	Provider   provider.Provider
	Model      string
	ConfigPath string // path of the loaded config file, for /config

	// ResumeSession, when non-nil, seeds the new Model with a previously
	// saved session (messages, stats, name) instead of starting empty.
	// ResumeSessionName is the on-disk name it was loaded from. Set by
	// `llmtui chat --resume <name>` / `--continue`.
	ResumeSession     *history.Session
	ResumeSessionName string
}
```

- [ ] **Step 4: Wire `New` to adopt the resume session, and add `adoptSession`**

In `internal/tui/app.go`, find `New`'s closing lines:

```go
	m.rebuildFromConfig()
	return m
}
```

Replace with:

```go
	m.rebuildFromConfig()
	if opts.ResumeSession != nil {
		m.adoptSession(opts.ResumeSessionName, *opts.ResumeSession)
		m.notice = fmt.Sprintf("resumed %s (%d messages, %s/%s)",
			opts.ResumeSessionName, len(opts.ResumeSession.Messages),
			opts.ResumeSession.Provider, opts.ResumeSession.Model)
	}
	return m
}

// adoptSession replaces the running conversation with a previously saved
// one: its messages, token totals, and name (so subsequent saves update the
// same file instead of creating a new one), and clears the session summary
// since it described the old conversation. Used by /history load and by
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

No new imports needed — `history` and `fmt` are already imported in `internal/tui/app.go`.

- [ ] **Step 5: Refactor `/history load` to use `adoptSession`**

In `internal/tui/commands_local.go`, replace the `"load"` case body (lines 847-869):

```go
	case "load":
		if m.thinking {
			return m.fail("/history load is unavailable while a reply is streaming — esc to stop it first")
		}
		if rest == "" || m.historyDir == "" {
			return m.fail("usage: /history load <name> (see /history)")
		}
		s, err := history.Load(m.historyDir, rest)
		if err != nil {
			return m.fail(err.Error())
		}
		// Adopt the loaded session wholesale: its name (so saves update the
		// same file instead of duplicating it) and its token totals.
		m.session.Messages = s.Messages
		m.session.Stats = nil
		m.session.TotalPromptTokens = s.Prompt
		m.session.TotalCompletionTokens = s.Reply
		m.session.AnyEstimated = s.Estimated
		m.sessionName = rest
		m.summary = ""
		m.refreshViewport()
		m.notice = fmt.Sprintf("loaded %s (%d messages, %s/%s)", rest, len(s.Messages), s.Provider, s.Model)
```

with:

```go
	case "load":
		if m.thinking {
			return m.fail("/history load is unavailable while a reply is streaming — esc to stop it first")
		}
		if rest == "" || m.historyDir == "" {
			return m.fail("usage: /history load <name> (see /history)")
		}
		s, err := history.Load(m.historyDir, rest)
		if err != nil {
			return m.fail(err.Error())
		}
		m.adoptSession(rest, s)
		m.refreshViewport()
		m.notice = fmt.Sprintf("loaded %s (%d messages, %s/%s)", rest, len(s.Messages), s.Provider, s.Model)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/tui/... -v -run 'TestResumeOptionAdoptsSession|TestNoResumeOptionStartsFreshSession|TestHistoryLoadAdoptsSession'`
Expected: PASS for all three — the pre-existing `TestHistoryLoadAdoptsSession` must still pass unchanged, confirming the refactor didn't alter `/history load`'s behavior.

Then run the full package to catch any other regression:

Run: `go test ./internal/tui/... 2>&1 | tail -20`
Expected: `ok  	github.com/patrikcze/llmtui/internal/tui	...`

- [ ] **Step 7: Commit**

```bash
git add internal/tui/app.go internal/tui/commands_local.go internal/tui/bugfix_test.go
git commit -m "feat(tui): add Options.ResumeSession, shared with /history load"
```

---

### Task 3: `llmtui chat --resume` / `--continue`

**Files:**
- Modify: `internal/cli/chat.go`

**Interfaces:**
- Consumes: `history.Latest` (Task 1), `history.Load` (existing), `tui.Options.ResumeSession`/`ResumeSessionName` (Task 2), `Root.historyDir() (string, error)` (existing, `internal/cli/history.go`).
- Produces: the `--resume`/`--continue` CLI surface — nothing downstream depends on new exported symbols from this task.

No automated test for this task (see Global Constraints) — `go build` plus the manual verification script in Step 3 is the deliverable check.

- [ ] **Step 1: Replace `internal/cli/chat.go`**

Replace the full file content with:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/tui"
)

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

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: no output, exit code 0.

- [ ] **Step 3: Manual verification script**

This drives the real interactive TUI, so it can't be scripted end-to-end — run it yourself in a terminal:

```bash
go build -o /tmp/llmtui-resume-test ./cmd/llmtui
export LLMTUI_CHAT_HISTORY_DIR=$(mktemp -d)
echo "history dir: $LLMTUI_CHAT_HISTORY_DIR"

# 1. Start a session, send one message, save, quit.
/tmp/llmtui-resume-test chat --provider mock --model demo-model
#    - type: hello
#    - press Enter, wait for the mock reply
#    - press Ctrl+S to save (or /save), note the session name it reports
#    - /quit

# 2. Confirm it's listed.
/tmp/llmtui-resume-test history
#    Expected: one row, NAME matching what Ctrl+S reported.

# 3. Resume it by name.
/tmp/llmtui-resume-test chat --provider mock --model demo-model --resume <name-from-step-2>
#    Expected: TUI opens with the "hello" exchange already in the transcript,
#    and a status-line notice reading "resumed <name> (2 messages, mock/demo-model)".
#    /quit

# 4. Resume it via --continue.
/tmp/llmtui-resume-test chat --provider mock --model demo-model --continue
#    Expected: same result as step 3, no name needed.
/tmp/llmtui-resume-test chat --provider mock --model demo-model -c
#    Expected: same, via the short flag.

# 5. Unknown name fails before the TUI opens.
/tmp/llmtui-resume-test chat --provider mock --model demo-model --resume does-not-exist
echo "exit code: $?"
#    Expected: a "resume: read session: ..." error printed, non-zero exit,
#    no full-screen TUI ever appeared.

# 6. Mutually exclusive flags are rejected.
/tmp/llmtui-resume-test chat --resume x --continue
echo "exit code: $?"
#    Expected: cobra's mutually-exclusive-flags error, non-zero exit.

rm -f /tmp/llmtui-resume-test
```

- [ ] **Step 4: Commit**

```bash
git add internal/cli/chat.go
git commit -m "feat(cli): add chat --resume <name> / --continue"
```

---

### Task 4: Docs

**Files:**
- Modify: `README.md`
- Modify: `docs/configuration.md`

**Interfaces:** none — documentation only, describing the flags Task 3 added.

- [ ] **Step 1: Update the README commands table**

In `README.md`, find this row (around line 100):

```markdown
| `llmtui chat` | Interactive full-screen chat |
```

Replace with:

```markdown
| `llmtui chat` | Interactive full-screen chat (`--resume <name>` / `--continue` to resume a saved session) |
```

- [ ] **Step 2: Add a "Resuming a session" subsection**

In `README.md`, find the end of the "History & usage stats" section:

```markdown
Set `chat.save_history: false` to disable both.
```

Replace with:

```markdown
Set `chat.save_history: false` to disable both.

### Resuming a session

```bash
./llmtui chat --resume session-20260702-163005   # resume that exact saved session
./llmtui chat --continue                          # resume the most recently saved session
./llmtui chat -c                                  # short form of --continue
```

Both read from `chat.history_dir` regardless of `chat.save_history` (like
`llmtui history` does) — they only need the directory to contain saved
sessions, not future saving to be enabled. Resuming restores the
conversation's messages and token totals and adopts the session's name (so
`/save` / `Ctrl+S` update the same file); it does not change which
provider/model you're using — that still follows the normal
`--provider`/`--model` precedence.
```

- [ ] **Step 3: Note the behavior in `docs/configuration.md`**

In `docs/configuration.md`, find this row in the `chat` table (around line 61):

```markdown
| `model_profile` | auto | Pin a model profile by name |
```

Keep it as-is, but add this paragraph immediately after the table (before the `### tools` heading):

```markdown
`llmtui chat --resume <name>` and `--continue` read saved sessions from
`history_dir` the same way `llmtui history` does — regardless of the current
`save_history` value, since they only read existing files rather than write
new ones.
```

- [ ] **Step 4: Commit**

```bash
git add README.md docs/configuration.md
git commit -m "docs: document chat --resume / --continue"
```
