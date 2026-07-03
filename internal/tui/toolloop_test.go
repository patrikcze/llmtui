package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func withToolReply(m *Model, reply string) {
	m.session.AddUser("make me a script")
	m.session.AddAssistant(reply)
}

func TestMaybeRunToolsExecutesAndFollowsUp(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(root, 64)
	withToolReply(m, "Saving it now:\n```tool write_file hello.sh\n#!/bin/sh\necho hi\n```")

	cmd := m.maybeRunTools()
	if cmd == nil {
		t.Fatal("expected a follow-up dispatch command")
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
