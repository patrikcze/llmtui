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

func TestMaybeRunToolsIterationCap(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.cfg.Tools.MaxIterations = 2
	m.toolDepth = 2
	withToolReply(m, "```tool list_dir\n```")

	if m.maybeRunTools() != nil {
		t.Fatal("loop exceeded max_iterations")
	}
	if !strings.Contains(m.errText, "tool loop stopped") {
		t.Errorf("errText = %q", m.errText)
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
