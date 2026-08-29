package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/cache"
	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
	"github.com/patrikcze/llmtui/internal/tools"
)

func withToolReply(m *Model, reply string) {
	m.session.AddUser("make me a script")
	m.session.AddAssistant(reply)
}

func finishToolBatch(t *testing.T, m *Model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected async tool batch command")
	}
	msg := cmd()
	if _, ok := msg.(mcpToolResultsMsg); !ok {
		t.Fatalf("tool command returned %T, want mcpToolResultsMsg", msg)
	}
	_, next := m.Update(msg)
	return next
}

func TestMaybeRunToolsAsksBeforeWriting(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 64)
	withToolReply(m, "Saving it now:\n```tool write_file hello.sh\n#!/bin/sh\necho hi\n```")

	// Default mode is ask: nothing may touch disk before the user says yes.
	if cmd := m.maybeRunTools(); cmd != nil {
		t.Fatal("write executed without approval")
	}
	if len(m.pendingCalls) != 1 {
		t.Fatalf("pendingCalls = %d, want 1", len(m.pendingCalls))
	}
	if _, err := os.Stat(filepath.Join(root, "hello.sh")); err == nil {
		t.Fatal("file written before approval")
	}

	// Approve with y.
	_, cmd := m.updateToolApproval(tea.KeyPressMsg{Code: 'y', Text: string('y')})
	if cmd == nil {
		t.Fatal("expected an async tool command after approval")
	}
	cmd = finishToolBatch(t, m, cmd)
	if cmd == nil {
		t.Fatal("expected a follow-up dispatch after tool results")
	}
	data, err := os.ReadFile(filepath.Join(root, "hello.sh"))
	if err != nil {
		t.Fatalf("file not written: %v", err)
	}
	if string(data) != "#!/bin/sh\necho hi\n" {
		t.Errorf("content = %q", data)
	}
	// The results message must be in the session for the model to see.
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleUser || !strings.HasPrefix(last.Content, tools.ResultsPrefix) {
		t.Errorf("last message = %+v", last)
	}
	if m.toolDepth != 1 {
		t.Errorf("toolDepth = %d, want 1", m.toolDepth)
	}
	// Tool follow-ups are not user-sent messages.
	if m.exit.sentCount != 0 {
		t.Errorf("sentCount = %d, want 0", m.exit.sentCount)
	}
}

func TestMaybeRunToolsAutoApproveSkipsPrompt(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	withToolReply(m, "```tool write_file a.txt\ndata\n```")

	cmd := m.maybeRunTools()
	if cmd == nil {
		t.Fatal("auto mode should execute without prompting")
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
		t.Fatal("native tool executed on the UI update goroutine")
	}
	finishToolBatch(t, m, cmd)
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Errorf("file not written in auto mode: %v", err)
	}
	if m.toolOK != 1 {
		t.Errorf("toolOK = %d, want 1", m.toolOK)
	}
}

func TestMaybeRunToolsReadOnlyRunsWithoutApproval(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	withToolReply(m, "```tool list_dir\n```")

	if cmd := m.maybeRunTools(); cmd == nil {
		t.Fatal("read-only call should run without approval")
	}
	if len(m.pendingCalls) != 0 {
		t.Errorf("pendingCalls = %d, want 0", len(m.pendingCalls))
	}
}

func TestDenyPendingToolsReportsToModel(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 64)
	withToolReply(m, "```tool write_file a.txt\ndata\n```")
	m.maybeRunTools()

	_, cmd := m.updateToolApproval(tea.KeyPressMsg{Code: 'n', Text: string('n')})
	if cmd == nil {
		t.Fatal("denial must be dispatched back to the model")
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
		t.Fatal("file written despite denial")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if !strings.Contains(last.Content, "denied by the user") {
		t.Errorf("denial not reported: %q", last.Content)
	}
	if m.toolErr != 1 {
		t.Errorf("toolErr = %d, want 1", m.toolErr)
	}
}

// TestPendingApprovalClosesOpenOverlay guards against a silent-approval bug:
// an overlay opened by a non-blocking command (e.g. /help) could stay open
// while a reply streams in. If that reply produces a tool call needing
// approval, pendingCalls used to go non-empty without touching overlayOpen,
// so the very next keypress — the user's attempt to dismiss the overlay —
// was instead routed to updateToolApproval and, on Enter, silently approved
// the pending call. startToolBatch must force the overlay closed so the
// approval prompt is what the user actually sees.
func TestPendingApprovalClosesOpenOverlay(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 64)
	m.overlayOpen = true // simulating /help (or similar) left open while thinking

	withToolReply(m, "Saving it now:\n```tool write_file hello.sh\n#!/bin/sh\necho hi\n```")
	m.maybeRunTools()

	if len(m.pendingCalls) != 1 {
		t.Fatalf("pendingCalls = %d, want 1", len(m.pendingCalls))
	}
	if m.overlayOpen {
		t.Fatal("overlay must be closed once a tool approval is pending")
	}
	if _, err := os.Stat(filepath.Join(root, "hello.sh")); err == nil {
		t.Fatal("file must not be written before the user approves")
	}
}

func TestPendingApprovalClosesKeysInspector(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.keysMode = true

	withToolReply(m, "```tool write_file approval.txt\napproved\n```")
	m.maybeRunTools()

	if len(m.pendingCalls) != 1 {
		t.Fatalf("pendingCalls = %d, want 1", len(m.pendingCalls))
	}
	if m.keysMode {
		t.Fatal("keys inspector must close once a tool approval is pending")
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: string('y')})
	if cmd == nil || len(m.pendingCalls) != 0 {
		t.Fatal("approval key was not routed to the pending tool prompt")
	}
}

func TestApprovalSwallowsOtherKeys(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	withToolReply(m, "```tool write_file a.txt\ndata\n```")
	m.maybeRunTools()

	_, cmd := m.updateToolApproval(tea.KeyPressMsg{Code: 'x', Text: string('x')})
	if cmd != nil || len(m.pendingCalls) != 1 {
		t.Error("stray key must neither execute nor clear the pending batch")
	}
}

func TestMaybeRunToolsDisabledOrNoCalls(t *testing.T) {
	m := newTestModel(t)
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	m.toolsOn = false
	withToolReply(m, "```tool write_file x.txt\ndata\n```")
	if m.maybeRunTools() != nil {
		t.Error("tools ran while disabled")
	}

	m.toolsOn = true
	m.session.AddAssistant("just a normal answer")
	if m.maybeRunTools() != nil {
		t.Error("dispatch triggered without tool blocks")
	}
}

func TestIterationCapAsksUserToContinue(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.cfg.Tools.MaxIterations = 2
	m.toolDepth = 2
	withToolReply(m, "```tool list_dir\n```")

	// Over budget: nothing executes and nothing errors — the user is asked.
	if m.maybeRunTools() != nil {
		t.Fatal("batch ran despite a spent budget")
	}
	if !m.pendingBudget || len(m.pendingCalls) != 1 {
		t.Fatalf("budget prompt not shown: pendingBudget=%v calls=%d", m.pendingBudget, len(m.pendingCalls))
	}
	if m.errText != "" {
		t.Errorf("errText = %q, want empty (no dead-end error)", m.errText)
	}

	// "Yes, continue" renews the budget and executes the batch.
	_, cmd := m.updateToolApproval(tea.KeyPressMsg{Code: 'y', Text: string('y')})
	if cmd == nil {
		t.Fatal("expected execution after granting more rounds")
	}
	if m.pendingBudget || len(m.pendingCalls) != 0 {
		t.Error("budget prompt not cleared")
	}
	if m.toolDepth != 1 {
		t.Errorf("toolDepth = %d, want 1 (reset to 0, then one executed round)", m.toolDepth)
	}
}

func TestIterationCapDeclineAsksModelToWrapUp(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.cfg.Tools.MaxIterations = 2
	m.toolDepth = 2
	withToolReply(m, "```tool list_dir\n```")
	m.maybeRunTools()

	// "No" sends the wrap-up request instead of executing anything.
	_, cmd := m.updateToolApproval(tea.KeyPressMsg{Code: 'n', Text: string('n')})
	if cmd == nil {
		t.Fatal("expected a wrap-up dispatch after declining")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if !strings.Contains(last.Content, "iteration limit") {
		t.Errorf("wrap-up message missing the limit explanation: %q", last.Content)
	}
	if m.toolErr != 1 {
		t.Errorf("toolErr = %d, want 1 (the unexecuted call)", m.toolErr)
	}
	if m.errText != "" {
		t.Errorf("errText = %q, want empty", m.errText)
	}
}

func TestNativeToolCallsExecuteAndContinue(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	m.session.AddUser("what files are here?")
	m.thinking = true

	done := provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{
		{ID: "call_1", Name: "list_dir", Arguments: `{}`},
	}}
	_, cmd := m.handleStreamEvent(streamEventMsg{event: done, ok: true})
	if cmd == nil {
		t.Fatal("native tool calls did not trigger execution")
	}
	finishToolBatch(t, m, cmd)

	n := len(m.session.Messages)
	if n < 2 {
		t.Fatalf("messages = %d, want assistant + tool result", n)
	}
	assistant := m.session.Messages[n-2]
	if assistant.Role != provider.RoleAssistant || len(assistant.ToolCalls) != 1 {
		t.Errorf("assistant message = %+v, want one tool call", assistant)
	}
	result := m.session.Messages[n-1]
	if result.Role != provider.RoleTool || result.ToolCallID != "call_1" || result.ToolName != "list_dir" {
		t.Errorf("tool result = %+v", result)
	}
	if m.toolOK != 1 {
		t.Errorf("toolOK = %d, want 1", m.toolOK)
	}
}

func TestNativeToolCapabilityOverrideAndLearnedRejectionAreModelScoped(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.cfg.Tools.Native = "auto"
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	unsupported := false
	m.prov = provider.WithCapabilityOverrides(m.prov, provider.CapabilityOverrides{NativeTools: &unsupported})
	m.resetNativeToolMode()
	if m.useNativeTools() || len(m.activeToolSpecs()) != 0 {
		t.Fatal("explicit native_tools:false should select fallback without offering schemas")
	}

	supported := true
	m.prov = provider.WithCapabilityOverrides(mock.New(), provider.CapabilityOverrides{NativeTools: &supported})
	m.model = "capable-a"
	m.resetNativeToolMode()
	if !m.useNativeTools() || len(m.activeToolSpecs()) == 0 {
		t.Fatal("explicit native_tools:true should offer native schemas")
	}
	m.nativeToolRejections[m.nativeToolCapabilityKey()] = true
	m.resetNativeToolMode()
	if m.useNativeTools() {
		t.Fatal("learned rejection was not applied to the current provider/model")
	}
	m.setModel("capable-b")
	if !m.useNativeTools() {
		t.Fatal("one model's rejection leaked to a different model")
	}
	m.setModel("capable-a")
	if m.useNativeTools() {
		t.Fatal("returning to the rejected model forgot learned capability state")
	}
}

// TestEmptyCompletionAfterToolExecutionRetriesOnceThenReportsError guards two
// things: a model that samples straight to EOS right after a tool result
// (observed on a small local model deep into a long tool-heavy run) gets one
// fresh attempt at the same round before the run is declared failed, and the
// eventual failure message carries this round's completion-token count so a
// "genuinely produced nothing" empty completion can be told apart from one
// where real output existed but wasn't recognised as text or a tool call.
func TestEmptyCompletionAfterToolExecutionRetriesOnceThenReportsError(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.toolDepth = 1
	messagesBefore := len(m.session.Messages)

	_, cmd := m.handleStreamEvent(streamEventMsg{
		event: provider.ChatEvent{Type: provider.EventDone},
		ok:    true,
		gen:   m.streamGen,
	})
	if cmd == nil {
		t.Fatal("first empty completion must retry once, not fail immediately")
	}
	if m.errText != "" {
		t.Errorf("errText = %q, want empty before the retry has even run", m.errText)
	}
	if !m.emptyContinuationRetried {
		t.Error("emptyContinuationRetried must be set after spending the one retry")
	}
	if !m.thinking {
		t.Fatal("continueChat's retry must leave a request in flight")
	}

	// A round that burned its whole budget on hidden reasoning and never
	// reached a visible answer or a tool call looks identical to a
	// genuinely-empty round from streamBuf/ToolCalls alone; the reasoning
	// char count in the failure message is what tells them apart.
	_, _ = m.handleStreamEvent(streamEventMsg{
		event: provider.ChatEvent{Type: provider.EventReasoning, Delta: "thinking about the missing file"},
		ok:    true,
		gen:   m.streamGen,
	})
	_, cmd = m.handleStreamEvent(streamEventMsg{
		event: provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{CompletionTokens: 2}},
		ok:    true,
		gen:   m.streamGen,
	})
	if cmd != nil {
		t.Fatal("a second empty completion in a row must not retry again")
	}
	if !strings.Contains(m.errText, "twice in a row") || !strings.Contains(m.errText, "2 completion token") {
		t.Errorf("errText = %q, want the retry outcome and completion-token count", m.errText)
	}
	if !strings.Contains(m.errText, "31 reasoning char") {
		t.Errorf("errText = %q, want this round's reasoning char count so a hidden-reasoning-only round is diagnosable from a genuinely empty one", m.errText)
	}
	if !strings.Contains(m.errText, "finish reason reported truncated=false") {
		t.Errorf("errText = %q, want the finish-reason truncation flag", m.errText)
	}
	if m.thinking {
		t.Error("empty completion must finish the stream")
	}
	if len(m.session.Messages) != messagesBefore {
		t.Errorf("messages = %d, want %d (no empty assistant message)", len(m.session.Messages), messagesBefore)
	}
}

// TestTruncatedNativeToolCallIsNotExecuted guards the fix for
// docs/architecture/v1-agent-runtime.md §2: a tool call truncated by
// max_tokens must never be executed, in ordinary tool chat just as much as
// in /agent on. Before this fix, only /agent on recorded truncation as
// evidence (and only for its own verifier, after the fact) — the ordinary
// tool loop had no check at all and would have run the call.
// TestMalformedToolCallRetriesOnceThenReportsError guards against a backend
// (observed: LM Studio's Harmony parser for openai/gpt-oss-20b) failing to
// convert the model's tool-call attempt into structured ToolCalls and
// leaking the raw, still-tokenized attempt into content instead. That text
// is not a real answer: it gets one retry at the same round — same
// accumulated history, nothing resent — before the run is reported failed,
// and it must never land in conversation history (it would only confuse the
// model further on every later turn).
func TestMalformedToolCallRetriesOnceThenReportsError(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	messagesBefore := len(m.session.Messages)

	_, cmd := m.handleStreamEvent(streamEventMsg{
		event: provider.ChatEvent{Type: provider.EventDone, MalformedToolCall: true},
		ok:    true,
		gen:   m.streamGen,
	})
	if cmd == nil {
		t.Fatal("first malformed tool call must retry once, not fail immediately")
	}
	if m.errText != "" {
		t.Errorf("errText = %q, want empty before the retry has even run", m.errText)
	}
	if !m.malformedToolCallRetried {
		t.Error("malformedToolCallRetried must be set after spending the one retry")
	}
	if m.toolsNative {
		t.Error("malformed native tool call must switch the retry to the fenced protocol")
	}
	if !m.nativeToolRejections[m.nativeToolCapabilityKey()] {
		t.Error("malformed native tool call must be remembered for this provider/model")
	}
	if !m.thinking {
		t.Fatal("continueChat's retry must leave a request in flight")
	}
	if len(m.session.Messages) != messagesBefore {
		t.Errorf("messages = %d, want %d (leaked content must never enter history)", len(m.session.Messages), messagesBefore)
	}

	_, cmd = m.handleStreamEvent(streamEventMsg{
		event: provider.ChatEvent{Type: provider.EventDone, MalformedToolCall: true},
		ok:    true,
		gen:   m.streamGen,
	})
	if cmd != nil {
		t.Fatal("a second malformed tool call in a row must not retry again")
	}
	if !strings.Contains(m.errText, "twice in a row") {
		t.Errorf("errText = %q, want the retry outcome explained", m.errText)
	}
	if m.thinking {
		t.Error("malformed tool call failure must finish the stream")
	}
	if len(m.session.Messages) != messagesBefore {
		t.Errorf("messages = %d, want %d (no garbled assistant message)", len(m.session.Messages), messagesBefore)
	}
}

// TestMalformedToolCallDropsLeakedDeltaFromHistory guards the streaming
// path: unlike the non-streaming path, leaked/garbled text arrives as
// ordinary EventDelta chunks before the backend's parser failure is
// recognizable, so it lands in streamBuf. The MalformedToolCall flag on
// EventDone must still cause it to be dropped rather than stored as the
// model's answer.
func TestMalformedToolCallDropsLeakedDeltaFromHistory(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	messagesBefore := len(m.session.Messages)

	m.handleStreamEvent(streamEventMsg{
		event: provider.ChatEvent{Type: provider.EventDelta, Delta: `to=functions.read_file<|constrain|>json<|message|>{"path":"x"}`},
		ok:    true,
		gen:   m.streamGen,
	})
	if m.streamBuf.Len() == 0 {
		t.Fatal("test setup: delta should have landed in streamBuf")
	}

	_, cmd := m.handleStreamEvent(streamEventMsg{
		event: provider.ChatEvent{Type: provider.EventDone, MalformedToolCall: true},
		ok:    true,
		gen:   m.streamGen,
	})
	if cmd == nil {
		t.Fatal("first malformed tool call must retry once")
	}
	if len(m.session.Messages) != messagesBefore {
		t.Errorf("messages = %d, want %d (leaked delta must not enter history)", len(m.session.Messages), messagesBefore)
	}
}

func TestTruncatedNativeToolCallIsNotExecuted(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	m.thinking = true

	done := provider.ChatEvent{Type: provider.EventDone, Truncated: true, ToolCalls: []provider.ToolCall{
		{ID: "call_1", Name: "write_file", Arguments: `{"path":"out.txt","content":"truncated conte`},
	}}
	_, cmd := m.handleStreamEvent(streamEventMsg{event: done, ok: true})

	if cmd != nil {
		t.Fatal("a truncated tool call must not start execution")
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); err == nil {
		t.Fatal("truncated write_file call must not have executed")
	}
	if m.errText == "" || !strings.Contains(m.errText, "cut off") {
		t.Errorf("errText = %q, want an explanation that the call was cut off", m.errText)
	}
	if m.toolOK != 0 {
		t.Errorf("toolOK = %d, want 0: nothing should have run", m.toolOK)
	}
}

func TestEmptyCompletionBeforeToolExecutionRemainsClean(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true

	_, cmd := m.handleStreamEvent(streamEventMsg{
		event: provider.ChatEvent{Type: provider.EventDone},
		ok:    true,
	})

	if cmd != nil {
		t.Fatal("empty completion must not start another command")
	}
	if m.errText != "" {
		t.Errorf("errText = %q, want empty", m.errText)
	}
}

func TestNativeToolCallsRespectApproval(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 64)
	m.session.AddUser("write a file")
	m.thinking = true

	done := provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{
		{ID: "call_1", Name: "write_file", Arguments: `{"path":"a.txt","content":"data"}`},
	}}
	_, cmd := m.handleStreamEvent(streamEventMsg{event: done, ok: true})
	if cmd != nil {
		t.Fatal("write executed without approval")
	}
	if len(m.pendingCalls) != 1 {
		t.Fatalf("pendingCalls = %d, want 1", len(m.pendingCalls))
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
		t.Fatal("file written before approval")
	}

	_, cmd = m.updateToolApproval(tea.KeyPressMsg{Code: 'y', Text: string('y')})
	if cmd == nil {
		t.Fatal("expected a continuation command after approval")
	}
	finishToolBatch(t, m, cmd)
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("file not written after approval: %v", err)
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != "call_1" {
		t.Errorf("tool result = %+v", last)
	}
}

func TestSendResetsToolBudget(t *testing.T) {
	m := newTestModel(t)
	m.toolDepth = 3
	m.input.SetValue("hello")
	if cmd := m.send(); cmd == nil {
		t.Fatal("send returned nil")
	}
	if m.toolDepth != 0 {
		t.Errorf("toolDepth = %d, want 0 after a fresh user turn", m.toolDepth)
	}
	if m.exit.sentCount != 1 {
		t.Errorf("sentCount = %d, want 1", m.exit.sentCount)
	}
}

// TestRetryResetsToolBudget guards against /retry inheriting a spent tool
// budget from the turn it's retrying: retryLast used to skip the toolDepth
// reset that send() applies, so retrying a turn that had used up (or nearly
// used up) tools.max_iterations rounds could immediately hit "tool budget
// spent" on the very first tool call of the retried turn.
func TestRetryResetsToolBudget(t *testing.T) {
	m := newTestModel(t)
	m.lastUserMsg = "hello"
	m.toolDepth = 3
	if cmd := m.retryLast(); cmd == nil {
		t.Fatal("retryLast returned nil")
	}
	if m.toolDepth != 0 {
		t.Errorf("toolDepth = %d, want 0 after retry — a retry is a fresh turn", m.toolDepth)
	}
}

func TestComposeInjectsToolInstructions(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	out, _ := m.compose("hi", nil, true)
	joined := ""
	for _, msg := range out.Messages {
		if msg.Role == provider.RoleSystem {
			joined += msg.Content
		}
	}
	if !strings.Contains(joined, "write_file") {
		t.Error("system prompt missing tool instructions while tools are on")
	}

	m.toolsOn = false
	out, _ = m.compose("hi", nil, true)
	joined = ""
	for _, msg := range out.Messages {
		if msg.Role == provider.RoleSystem {
			joined += msg.Content
		}
	}
	if strings.Contains(joined, "write_file") {
		t.Error("tool instructions leaked into the prompt while tools are off")
	}
}

// TestCacheKeyChangesWithToolState guards against serving a tools-disabled
// cached reply for a tools-enabled request (or vice versa): the actual
// prompt sent to the provider differs (tool instructions are appended to
// the system prompt), so the cache key must differ too, even though
// chat.system_prompt itself and the user message are unchanged.
func TestCacheKeyChangesWithToolState(t *testing.T) {
	m := newTestModel(t)
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	m.toolsOn = false
	keyOff := m.cacheKey("hi", nil)

	m.toolsOn = true
	keyOn := m.cacheKey("hi", nil)

	if keyOff.Hash() == keyOn.Hash() {
		t.Error("cache key must differ between tools-enabled and tools-disabled requests")
	}
}

func TestHistoryFingerprintIncludesProviderVisibleFields(t *testing.T) {
	base := provider.Message{Role: provider.RoleUser, Content: "same text"}
	tests := []struct {
		name    string
		message provider.Message
	}{
		{
			name: "image data",
			message: provider.Message{
				Role: provider.RoleUser, Content: "same text",
				Images: []provider.Image{{MIME: "image/png", Data: []byte("pixels")}},
			},
		},
		{
			name: "image MIME",
			message: provider.Message{
				Role: provider.RoleUser, Content: "same text",
				Images: []provider.Image{{MIME: "image/jpeg", Data: []byte("pixels")}},
			},
		},
		{
			name: "assistant tool call",
			message: provider.Message{
				Role: provider.RoleUser, Content: "same text",
				ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "read_file", Arguments: `{"path":"a"}`}},
			},
		},
		{
			name: "tool result identity",
			message: provider.Message{
				Role: provider.RoleUser, Content: "same text", ToolCallID: "call-1", ToolName: "read_file",
			},
		},
		{
			name: "provider continuation",
			message: provider.Message{
				Role: provider.RoleUser, Content: "same text",
				Continuation: &provider.ProviderContinuation{Reasoning: "private reasoning"},
			},
		},
	}

	baseHash := historyFingerprint([]provider.Message{base})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := historyFingerprint([]provider.Message{tt.message}); got == baseHash {
				t.Error("provider-visible history change did not change fingerprint")
			}
		})
	}

	withDisplay := base
	withDisplay.Display = "UI-only rendered diff"
	if got := historyFingerprint([]provider.Message{withDisplay}); got != baseHash {
		t.Error("UI-only Display field must not affect the provider history fingerprint")
	}
}

func TestNativeToolFallbackBypassesCacheWrite(t *testing.T) {
	m := newTestModel(t)
	m.responseCache = cache.New(t.TempDir(), time.Hour, 16, true)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	key := m.cacheKey("hello", nil)
	m.lastDebug = debugInfo{
		CacheKey:    key,
		CacheStatus: "miss",
		Provider:    m.prov.Name(),
		Model:       m.model,
		Stream:      true,
	}
	m.streamBuf.WriteString("answer produced after dropping native tools")
	m.thinking = true
	stream := make(chan provider.ChatEvent)
	close(stream)

	m.Update(firstStreamMsg{
		stream:        stream,
		event:         provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{PromptTokens: 2, CompletionTokens: 3}},
		ok:            true,
		toolsFellBack: true,
	})

	if m.lastDebug.CacheStatus != "bypass" {
		t.Errorf("CacheStatus = %q, want bypass", m.lastDebug.CacheStatus)
	}
	if _, ok, err := m.responseCache.Get(key); err != nil || ok {
		t.Fatalf("fallback response was cached under pre-fallback key: ok=%v err=%v", ok, err)
	}
}

func TestBuildRequestIncludesConnectedMCPTools(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.mcpRegistry = newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Description: "start a session", Schema: json.RawMessage(`{"type":"object"}`)},
	}, nil)

	req := m.buildRequest(nil)
	found := false
	for _, spec := range req.Tools {
		if spec.Name == "mcp__jiraWorklog__session_start" {
			found = true
		}
	}
	if !found {
		t.Fatalf("req.Tools = %+v, missing the connected MCP server's tool", req.Tools)
	}
}

func TestBuildRequestOmitsMCPToolsWhenToolsOff(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = false
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.mcpRegistry = newConnectedMCPRegistry(t, "jiraWorklog", []mcp.Tool{
		{Server: "jiraWorklog", Name: "session_start", Schema: json.RawMessage(`{"type":"object"}`)},
	}, nil)

	req := m.buildRequest(nil)
	if len(req.Tools) != 0 {
		t.Errorf("req.Tools = %+v, want empty when /tools is off", req.Tools)
	}
}

// TestCacheKeyChangesWithConnectedMCPServer guards the specific gap this
// project's cache-key-completeness invariant calls out: connecting an MCP
// server changes what's actually sent to the provider (req.Tools grows) even
// though the user message, history, and toolsOn/native state are unchanged
// — a request built before connecting must not be served the response
// cached for a request built after.
func TestCacheKeyChangesWithConnectedMCPServer(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	// The mock must actually advertise a tool once connected — without
	// CannedTools, ListTools returns nothing either way and the hashes
	// could never differ, defeating the point of this test.
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, CannedTools: []mcp.Tool{
			{Server: c.Name, Name: "session_start", Description: "start a session", Schema: json.RawMessage(`{"type":"object"}`)},
		}}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: time.Second,
	}}, factory)
	keyDisconnected := m.cacheKey("hi", nil)

	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	keyConnected := m.cacheKey("hi", nil)

	if keyDisconnected.Hash() == keyConnected.Hash() {
		t.Error("cache key must differ once an MCP server connects, even with the same message, history, and tools-on state")
	}
}

func TestMCPCallNeedsApprovalPerServerConfig(t *testing.T) {
	m := newTestModel(t)
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) { return &mcp.MockClient{ServerName: c.Name}, nil }
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "ask-server", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "ask", Timeout: time.Second},
		{Name: "auto-server", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "auto", Timeout: time.Second},
	}, factory)

	askCall := tools.Call{MCPServer: "ask-server", MCPTool: "t"}
	autoCall := tools.Call{MCPServer: "auto-server", MCPTool: "t"}
	if !m.callNeedsApproval(askCall) {
		t.Error("ask-server call should need approval")
	}
	if m.callNeedsApproval(autoCall) {
		t.Error("auto-server call should not need approval")
	}
}

func TestPureNativeBatchRunsAsync(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	withToolReply(m, "```tool write_file a.txt\ndata\n```")

	cmd := m.maybeRunTools()
	if cmd == nil {
		t.Fatal("expected an async tool command")
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
		t.Fatal("pure-native batch froze the update path by executing synchronously")
	}
	msg := cmd()
	if _, ok := msg.(mcpToolResultsMsg); !ok {
		t.Fatalf("cmd() = %T, want tool results", msg)
	}
	m.Update(msg)
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("async pure-native batch did not execute: %v", err)
	}
}

func TestNativeCommandBatchCancelViaEsc(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)

	cmd := m.startToolBatch([]tools.Call{{
		ID: "native-1", Tool: tools.ToolRunCommand,
		Body: "touch started; sleep 30; touch must-not-exist",
	}})
	if cmd == nil || m.mcpBatchCancel == nil {
		t.Fatal("expected cancellable native batch")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.mcpBatchCancel != nil {
		t.Fatal("Esc did not clear the native batch cancellation handle")
	}

	select {
	case msg := <-done:
		if _, ok := msg.(mcpToolResultsMsg); !ok {
			t.Fatalf("cmd() = %T, want tool results", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled native batch did not return promptly")
	}
	if _, err := os.Stat(filepath.Join(root, "must-not-exist")); err == nil {
		t.Fatal("native command continued after Esc cancellation")
	}
}

func TestMixedBatchRunsAsyncAndDeliversResults(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, CannedTools: []mcp.Tool{{
			Server: c.Name, Name: "session_start", Schema: json.RawMessage(`{"type":"object"}`),
		}}, CallFunc: func(name string, input json.RawMessage) (mcp.Result, error) {
			return mcp.Result{Content: "session started"}, nil
		}}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "auto", Timeout: time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	m.session.AddUser("start a session")
	m.thinking = true

	done := provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{
		{ID: "call_1", Name: "mcp__jiraWorklog__session_start", Arguments: `{"issue_key":"DEMO-1"}`},
	}}
	_, cmd := m.handleStreamEvent(streamEventMsg{event: done, ok: true})
	if cmd == nil {
		t.Fatal("expected an async command for the MCP call")
	}
	// Nothing has executed yet — this is the async path.
	if m.toolOK != 0 {
		t.Fatalf("toolOK = %d before the async command ran, want 0", m.toolOK)
	}

	msg := cmd()
	resultsMsg, ok := msg.(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want mcpToolResultsMsg", msg)
	}
	_, cmd2 := m.Update(resultsMsg)
	if cmd2 == nil {
		t.Fatal("expected a continuation command after the results arrive")
	}
	if m.toolOK != 1 {
		t.Errorf("toolOK = %d, want 1", m.toolOK)
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool ||
		!strings.Contains(last.Content, "\nsession started\n<<<LLMTUI_UNTRUSTED_END ") {
		t.Errorf("tool result message = %+v", last)
	}
}

func TestMCPBatchCancelViaEsc(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, Delay: time.Second}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "auto", Timeout: 5 * time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	cmd := m.startToolBatch([]tools.Call{{ID: "c1", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: "{}"}})
	if cmd == nil || m.mcpBatchCancel == nil {
		t.Fatal("expected an in-flight async batch with a cancel func set")
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.mcpBatchCancel != nil {
		t.Error("Esc should have cleared mcpBatchCancel")
	}
	if m.errText == "" {
		t.Error("Esc should report the cancellation")
	}

	// The already-dispatched command still completes (the goroutine isn't
	// killed, just its context) and reports the cancellation as an error.
	msg := cmd()
	resultsMsg, ok := msg.(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want mcpToolResultsMsg", msg)
	}
	if len(resultsMsg.results) != 1 || resultsMsg.results[0].Err == nil {
		t.Fatalf("results = %+v, want one cancelled result", resultsMsg.results)
	}
}

// TestMCPStaleResultsDroppedAfterPlainEsc guards the "worse than originally
// suspected" manifestation of the missing-generation-token bug: a *plain*
// Esc with no resend must actually stop the turn. Before the fix, the
// already-launched goroutine's tea.Cmd still delivered its mcpToolResultsMsg
// after Esc, and the handler was unconditional — it tallied the (usually
// cancelled-error) results and dispatched sendToolResults/continueChat
// anyway, so Esc looked like it worked but the turn silently continued.
func TestMCPStaleResultsDroppedAfterPlainEsc(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, Delay: time.Second}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "auto", Timeout: 5 * time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	cmd := m.startToolBatch([]tools.Call{{ID: "c1", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: "{}"}})
	if cmd == nil || m.mcpBatchCancel == nil {
		t.Fatal("expected an in-flight async batch with a cancel func set")
	}

	messagesBefore := len(m.session.Messages)
	toolOKBefore, toolErrBefore := m.toolOK, m.toolErr

	// Plain Esc — no resend follows.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.mcpBatchCancel != nil {
		t.Fatal("Esc should have cleared mcpBatchCancel")
	}

	// The already-launched goroutine's tea.Cmd still completes (only its
	// context was cancelled) and delivers its now-stale mcpToolResultsMsg.
	msg := cmd()
	resultsMsg, ok := msg.(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want mcpToolResultsMsg", msg)
	}
	_, cmd2 := m.Update(resultsMsg)
	if cmd2 != nil {
		t.Error("a stale mcpToolResultsMsg must not trigger a new dispatch (sendToolResults/continueChat)")
	}
	if m.thinking {
		t.Error("m.thinking must stay false — the stale message must not start a new turn")
	}
	if len(m.session.Messages) != messagesBefore {
		t.Errorf("session.Messages grew from %d to %d — the stale results were fed back to the model",
			messagesBefore, len(m.session.Messages))
	}
	if m.toolOK != toolOKBefore || m.toolErr != toolErrBefore {
		t.Errorf("toolOK/toolErr changed (%d/%d -> %d/%d) — a dropped stale message must not be tallied",
			toolOKBefore, toolErrBefore, m.toolOK, m.toolErr)
	}
}

// TestMCPStaleBatchDoesNotClobberResendBatch guards the cancel-then-resend
// manifestation: after Esc cancels batch A, m.busy() is false again (async
// batches don't set m.thinking), so a fresh batch B can start with its own
// live cancel handle. Before the fix, A's stale mcpToolResultsMsg — arriving
// after B started — unconditionally nil'd m.mcpBatchCancel (destroying B's
// handle) and dispatched sendToolResults for A's stale results, a second,
// concurrent turn that corrupts session.Messages ordering.
func TestMCPStaleBatchDoesNotClobberResendBatch(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, Delay: 50 * time.Millisecond, CallFunc: func(name string, input json.RawMessage) (mcp.Result, error) {
			return mcp.Result{Content: "ok:" + name}, nil
		}}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "auto", Timeout: 5 * time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Batch A starts (turn 1).
	cmdA := m.startToolBatch([]tools.Call{{ID: "cA", MCPServer: "jiraWorklog", MCPTool: "tool_a", MCPArgs: "{}"}})
	if cmdA == nil || m.mcpBatchCancel == nil {
		t.Fatal("expected batch A in flight with a cancel func set")
	}

	// Cancel A via Esc — no resend has happened yet.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.mcpBatchCancel != nil {
		t.Fatal("Esc should have cleared mcpBatchCancel")
	}

	// Batch B starts independently (turn 2, the resend), getting its own
	// live cancel handle.
	cmdB := m.startToolBatch([]tools.Call{{ID: "cB", MCPServer: "jiraWorklog", MCPTool: "tool_b", MCPArgs: "{}"}})
	if cmdB == nil || m.mcpBatchCancel == nil {
		t.Fatal("expected batch B in flight with its own cancel func")
	}
	messagesAfterBStart := len(m.session.Messages)

	// A's stale goroutine finally delivers its result while B is in flight.
	msgA := cmdA()
	resultsA, ok := msgA.(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmdA() = %T, want mcpToolResultsMsg", msgA)
	}
	_, cmd2 := m.Update(resultsA)
	if cmd2 != nil {
		t.Error("A's stale result must not trigger a dispatch")
	}
	if m.mcpBatchCancel == nil {
		t.Fatal("B's live cancel handle must not be clobbered by A's stale message")
	}
	if len(m.session.Messages) != messagesAfterBStart {
		t.Errorf("session.Messages grew from %d to %d — A's stale results were appended while B is in flight",
			messagesAfterBStart, len(m.session.Messages))
	}

	// B's own result resolves normally — the generation check must not
	// false-negative on a batch's own legitimate result.
	msgB := cmdB()
	resultsB, ok := msgB.(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmdB() = %T, want mcpToolResultsMsg", msgB)
	}
	_, cmd3 := m.Update(resultsB)
	if cmd3 == nil {
		t.Fatal("expected a continuation dispatch after B's own results arrive")
	}
	if m.mcpBatchCancel != nil {
		t.Error("mcpBatchCancel should be cleared once B's own results are processed")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "tool_b") {
		t.Errorf("last message = %+v, want B's tool result", last)
	}
}

func TestApprovalAlwaysScopesToExactCapability(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) { return &mcp.MockClient{ServerName: c.Name}, nil }
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "srv", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "ask", Timeout: time.Second},
	}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "srv"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	writeA := tools.Call{ID: "c1", Tool: tools.ToolWriteFile, Path: "a.txt", Body: "x"}
	m.pendingCalls = []tools.Call{writeA}
	m.resolveApproval(approvalAlways)
	if m.callNeedsApproval(writeA) {
		t.Error("matching write did not receive a scoped grant")
	}
	if !m.callNeedsApproval(tools.Call{Tool: tools.ToolWriteFile, Path: "b.txt", Body: "x"}) {
		t.Error("write grant escaped to a different path")
	}
	if !m.callNeedsApproval(tools.Call{MCPServer: "srv", MCPTool: "t", MCPArgs: "{}"}) {
		t.Error("native grant escaped to MCP")
	}

	m.approvalPolicy.Clear()
	mcpCall := tools.Call{ID: "c2", MCPServer: "srv", MCPTool: "t", MCPArgs: "{}"}
	m.pendingCalls = []tools.Call{mcpCall}
	m.resolveApproval(approvalAlways)
	if m.callNeedsApproval(mcpCall) {
		t.Error("matching MCP call did not receive a scoped grant")
	}
	if !m.callNeedsApproval(tools.Call{MCPServer: "srv", MCPTool: "other", MCPArgs: "{}"}) {
		t.Error("MCP grant escaped to a different tool")
	}
}

// TestSendBlockedDuringAsyncMCPBatch guards against a second, concurrent
// dispatch corrupting session.Messages ordering while an async MCP tool
// batch is outstanding. finishStream (called before startToolBatch ever
// runs) clears m.thinking, so the Enter-key guard used to read m.thinking
// alone and let a new send() through mid-batch; send() would then append a
// user message ahead of the batch's eventual tool-result messages and stomp
// m.toolDepth/m.stream/m.cancelStream out from under the in-flight request.
func TestSendBlockedDuringAsyncMCPBatch(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, Delay: time.Second}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "auto", Timeout: 5 * time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	cmd := m.startToolBatch([]tools.Call{{ID: "c1", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: "{}"}})
	if cmd == nil || m.mcpBatchCancel == nil {
		t.Fatal("expected an in-flight async batch with a cancel func set")
	}

	// finishStream (via handleStreamEvent, before startToolBatch runs) has
	// already reset m.thinking to false by the time the batch is outstanding
	// — that's the whole bug. Assert that precondition explicitly so this
	// test fails for the right reason if that ever changes.
	if m.thinking {
		t.Fatal("test setup: m.thinking should be false while the async batch runs")
	}
	if !m.busy() {
		t.Fatal("m.busy() must report true while mcpBatchCancel is set")
	}

	messagesBefore := len(m.session.Messages)
	toolDepthBefore := m.toolDepth

	m.input.SetValue("a second message typed mid-batch")
	if got := m.send(); got != nil {
		t.Error("send() during an in-flight async MCP batch must return nil, not start a new dispatch")
	}
	if len(m.session.Messages) != messagesBefore {
		t.Errorf("session.Messages grew from %d to %d — send() dispatched despite the outstanding batch",
			messagesBefore, len(m.session.Messages))
	}
	if m.toolDepth != toolDepthBefore {
		t.Errorf("toolDepth changed from %d to %d — send() must not touch the in-flight batch's budget",
			toolDepthBefore, m.toolDepth)
	}
	if m.input.Value() == "" {
		t.Error("send() should leave the typed text in the input box when it refuses to dispatch")
	}
}

// TestBusy covers the runtime states used to gate send() and retryLast()
// against a concurrent dispatch.
func TestBusy(t *testing.T) {
	m := newTestModel(t)

	cases := []struct {
		name  string
		state turnState
		want  bool
	}{
		{"idle", turnIdle, false},
		{"model streaming", turnModelStreaming, true},
		{"executing tools", turnExecutingTools, true},
		{"waiting approval", turnWaitingApproval, false},
		{"processing results", turnProcessingResults, false},
		{"completed", turnCompleted, false},
		{"cancelled", turnCancelled, false},
		{"failed", turnFailed, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m.state = c.state
			if got := m.busy(); got != c.want {
				t.Errorf("busy() = %v, want %v", got, c.want)
			}
		})
	}
}

// A backend that omits tool-call IDs (Ollama's native API always does) must
// still produce a session where the stored assistant message and the
// role:"tool" results agree on IDs — a result answering an ID the assistant
// message doesn't carry is protocol-invalid for strict OpenAI-compatible
// backends if the session is later replayed against one.
func TestToolCallIDsBackfilledConsistently(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.session.AddUser("list the files")
	m.thinking = true

	done := provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{
		{Name: "list_dir", Arguments: `{"path":""}`}, // no ID, like Ollama
	}}
	_, cmd := m.handleStreamEvent(streamEventMsg{event: done, ok: true})
	if cmd == nil {
		t.Fatal("native tool call did not trigger execution")
	}
	finishToolBatch(t, m, cmd)

	n := len(m.session.Messages)
	assistant, result := m.session.Messages[n-2], m.session.Messages[n-1]
	if assistant.Role != provider.RoleAssistant || len(assistant.ToolCalls) != 1 {
		t.Fatalf("assistant message = %+v", assistant)
	}
	if assistant.ToolCalls[0].ID == "" {
		t.Fatal("assistant tool call left without an ID")
	}
	if result.Role != provider.RoleTool || result.ToolCallID != assistant.ToolCalls[0].ID {
		t.Fatalf("tool result ID %q does not answer assistant call ID %q",
			result.ToolCallID, assistant.ToolCalls[0].ID)
	}
	firstID := assistant.ToolCalls[0].ID

	// A second round must generate a different ID (no collisions across
	// rounds). Fresh event (EnsureToolCallIDs fills the slice in place) and
	// the current generation (the continuation bumped streamGen).
	m.thinking = true
	done2 := provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{
		{Name: "list_dir", Arguments: `{"path":""}`},
	}}
	_, cmd = m.handleStreamEvent(streamEventMsg{event: done2, ok: true, gen: m.streamGen})
	if cmd == nil {
		t.Fatal("second native tool call did not trigger execution")
	}
	finishToolBatch(t, m, cmd)
	n = len(m.session.Messages)
	second := m.session.Messages[n-2]
	if second.ToolCalls[0].ID == firstID {
		t.Errorf("tool-call ID %q reused across rounds", firstID)
	}
}

// Events stamped with an older stream generation must be ignored: after an
// Esc-cancel, the abandoned stream's final message races the drain goroutine
// and can arrive while the *next* request is already streaming. Without the
// guard, a stale ok=false would finish the live stream prematurely.
func TestStaleStreamEventIsDropped(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.streamGen = 2
	m.streamBuf.WriteString("live partial")

	_, cmd := m.handleStreamEvent(streamEventMsg{ok: false, gen: 1})
	if cmd != nil {
		t.Fatal("stale event must not schedule work")
	}
	if !m.thinking {
		t.Fatal("stale channel-close finished the live stream")
	}
	if m.streamBuf.String() != "live partial" {
		t.Errorf("stale event touched the live stream buffer: %q", m.streamBuf.String())
	}
}

// A firstStreamMsg from a request the user already cancelled (Esc before its
// first event arrived) must not be adopted as the current stream, and its
// side effects (native-tools fallback) must not fire.
func TestStaleFirstStreamMsgNotAdopted(t *testing.T) {
	m := newTestModel(t)
	m.toolsNative = true
	m.streamGen = 2 // a newer request owns the loop

	stale := make(chan provider.ChatEvent)
	close(stale)
	m.Update(firstStreamMsg{
		stream:        stale,
		event:         provider.ChatEvent{Type: provider.EventDelta, Delta: "ghost"},
		ok:            true,
		gen:           1,
		toolsFellBack: true,
	})

	if m.stream != nil {
		t.Fatal("stale stream adopted as the current one")
	}
	if !m.toolsNative {
		t.Error("stale toolsFellBack flipped the native-tools protocol")
	}
	if m.streamBuf.Len() != 0 {
		t.Errorf("stale delta reached the stream buffer: %q", m.streamBuf.String())
	}
}
