package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func withToolReply(m *Model, reply string) {
	m.session.AddUser("make me a script")
	m.session.AddAssistant(reply)
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
	_, cmd := m.updateToolApproval(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("expected a follow-up dispatch command after approval")
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
	if m.sentCount != 0 {
		t.Errorf("sentCount = %d, want 0", m.sentCount)
	}
}

func TestMaybeRunToolsAutoApproveSkipsPrompt(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	withToolReply(m, "```tool write_file a.txt\ndata\n```")

	if cmd := m.maybeRunTools(); cmd == nil {
		t.Fatal("auto mode should execute without prompting")
	}
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

	_, cmd := m.updateToolApproval(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
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

func TestApprovalSwallowsOtherKeys(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	withToolReply(m, "```tool write_file a.txt\ndata\n```")
	m.maybeRunTools()

	_, cmd := m.updateToolApproval(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
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
	_, cmd := m.updateToolApproval(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
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
	_, cmd := m.updateToolApproval(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
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
		{ID: "call_1", Name: "list_dir", Arguments: `{"path":""}`},
	}}
	_, cmd := m.handleStreamEvent(streamEventMsg{event: done, ok: true})
	if cmd == nil {
		t.Fatal("native tool calls did not trigger execution")
	}

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

	_, cmd = m.updateToolApproval(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("expected a continuation command after approval")
	}
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
	if m.sentCount != 1 {
		t.Errorf("sentCount = %d, want 1", m.sentCount)
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
