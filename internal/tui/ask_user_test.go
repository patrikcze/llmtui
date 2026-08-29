package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func nativeAskCall(t *testing.T, id, arguments string) tools.Call {
	t.Helper()
	calls := tools.CallsFromNative([]provider.ToolCall{{ID: id, Name: tools.ToolAskUser, Arguments: arguments}})
	if len(calls) != 1 || calls[0].InputErr != "" {
		t.Fatalf("decode ask_user: %+v", calls)
	}
	return calls[0]
}

func TestAskUserNativeFreeTextPreservesToolCallID(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	call := nativeAskCall(t, "ask-42", `{"question":"Which environment?","allow_text":true}`)
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
		ID: call.ID, Name: call.Tool, Arguments: `{"question":"Which environment?","allow_text":true}`,
	}}})

	if cmd := m.startToolBatch([]tools.Call{call}); cmd != nil {
		t.Fatal("ask_user should pause without starting background work")
	}
	if m.pendingAsk == nil || m.state != turnWaitingUserInput || m.overlayOpen {
		t.Fatalf("pending ask state = %+v, turn=%s overlay=%v", m.pendingAsk, m.state, m.overlayOpen)
	}
	m.input.SetValue("staging")
	if cmd := m.send(); cmd == nil {
		t.Fatal("answer should continue the provider turn")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != "ask-42" || last.ToolName != tools.ToolAskUser {
		t.Fatalf("tool result = %+v", last)
	}
	if !strings.Contains(last.Content, `"answer":"staging"`) || !strings.Contains(last.Content, `"grants_authorization":false`) {
		t.Fatalf("tool result content = %q", last.Content)
	}
	if m.exit.sentCount != 0 {
		t.Fatalf("sentCount = %d, answer must not become a new human turn", m.exit.sentCount)
	}
}

func TestAskUserChoicePickerContinuesOriginalTurn(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	call := nativeAskCall(t, "ask-choice", `{"question":"Which environment?","choices":["development","staging","production"]}`)
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	m.startToolBatch([]tools.Call{call})
	if !m.overlayOpen || m.picker.pickerKind != pickerAgentQuestion || len(m.picker.pickerItems) != 3 {
		t.Fatalf("picker state = open:%v kind:%v items:%v", m.overlayOpen, m.picker.pickerKind, m.picker.pickerItems)
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("choice should continue the provider turn")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.ToolCallID != "ask-choice" || !strings.Contains(last.Content, `"answer":"staging"`) {
		t.Fatalf("choice result = %+v", last)
	}
}

func TestAskUserFencedFreeTextUsesFallbackResults(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	withToolReply(m, "```tool ask_user\n{\"question\":\"PostgreSQL version?\"}\n```")
	if cmd := m.maybeRunTools(); cmd != nil {
		t.Fatal("fenced ask_user should pause")
	}
	m.input.SetValue("16")
	if cmd := m.send(); cmd == nil {
		t.Fatal("fenced answer should continue through the fallback protocol")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleUser || !strings.HasPrefix(last.Content, tools.ResultsPrefix) || !strings.Contains(last.Content, `"answer":"16"`) {
		t.Fatalf("fallback result = %+v", last)
	}
}

func TestAskUserMixedBatchExecutesNothing(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	ask := nativeAskCall(t, "ask-mixed", `{"question":"Continue?"}`)
	write := tools.CallsFromNative([]provider.ToolCall{{ID: "write-mixed", Name: tools.ToolWriteFile, Arguments: `{"path":"unsafe.txt","content":"no"}`}})[0]
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
		{ID: ask.ID, Name: ask.Tool}, {ID: write.ID, Name: write.Tool},
	}})
	if cmd := m.startToolBatch([]tools.Call{ask, write}); cmd == nil {
		t.Fatal("mixed batch errors should be returned to the model")
	}
	if m.pendingAsk != nil || len(m.pendingCalls) != 0 {
		t.Fatal("mixed batch must neither pause nor request approval")
	}
	if _, err := os.Stat(filepath.Join(root, "unsafe.txt")); !os.IsNotExist(err) {
		t.Fatalf("side-effecting sibling executed: %v", err)
	}
	if m.toolDepth != 1 {
		t.Fatalf("rejected batch tool depth = %d, want 1", m.toolDepth)
	}
	results := m.session.Messages[len(m.session.Messages)-2:]
	for _, result := range results {
		if result.Role != provider.RoleTool || !strings.Contains(result.Content, "must be the only call") {
			t.Fatalf("mixed result = %+v", result)
		}
	}
}

func TestAskUserAgentPauseAndLiveResume(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.configureAgentLoop()
	run, err := agent.NewRun("ask-agent", "configure deployment", agent.DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("configure deployment", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	m.agentLoop.run = run
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	call := nativeAskCall(t, "ask-agent-1", `{"question":"Which environment?","choices":["staging","production"],"allow_text":true}`)
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	m.startToolBatch([]tools.Call{call})
	if run.Status != agent.DecisionNeedsUserInput || run.Stage != agent.StageExecutor || run.Cycle != 1 {
		t.Fatalf("paused run = %+v", run)
	}
	if m.agentLoop.verifying {
		t.Fatal("verifier started while ask_user was pending")
	}
	if cmd := m.answerAskUser("staging"); cmd == nil {
		t.Fatal("answer should resume executor")
	}
	if run.Status != agent.DecisionRunning || run.Stage != agent.StageExecutor || run.Cycle != 1 {
		t.Fatalf("live-resumed run = %+v", run)
	}
	if m.agentLoop.liveToolCalls != 0 {
		t.Fatalf("ask_user consumed executable tool budget: %d", m.agentLoop.liveToolCalls)
	}
}

func TestAskUserShutdownCompletesProtocolWithoutResuming(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	call := nativeAskCall(t, "ask-shutdown", `{"question":"Which environment?"}`)
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	m.startToolBatch([]tools.Call{call})
	m.completePendingAskForShutdown()
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != call.ID || !strings.Contains(last.Content, "incomplete interaction must not be replayed") {
		t.Fatalf("shutdown result = %+v", last)
	}
	if m.pendingAsk != nil {
		t.Fatal("pending ask survived shutdown completion")
	}
}

func TestAskUserSessionSnapshotCompletesProtocolWithoutChangingLiveTurn(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	call := nativeAskCall(t, "ask-save", `{"question":"Which environment?"}`)
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	m.startToolBatch([]tools.Call{call})
	liveCount := len(m.session.Messages)
	record := m.sessionRecord()
	if len(record.Messages) != liveCount+1 {
		t.Fatalf("snapshot messages = %d, want %d", len(record.Messages), liveCount+1)
	}
	last := record.Messages[len(record.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != call.ID || !strings.Contains(last.Content, "saved while waiting") {
		t.Fatalf("snapshot completion = %+v", last)
	}
	if len(m.session.Messages) != liveCount || m.pendingAsk == nil {
		t.Fatal("saving the snapshot mutated the live ask_user continuation")
	}
}

func TestAskUserAgentCancelWhileWaiting(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.configureAgentLoop()
	run, err := agent.NewRun("ask-agent-cancel", "configure deployment", agent.DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("configure deployment", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	m.agentLoop.run = run
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	call := nativeAskCall(t, "ask-agent-cancel-1", `{"question":"Which environment?"}`)
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	m.startToolBatch([]tools.Call{call})
	m.input.SetValue("/agent cancel")
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if run.Status != agent.DecisionCancelled || m.pendingAsk != nil {
		t.Fatalf("cancelled ask state: run=%+v pending=%+v", run, m.pendingAsk)
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "cancelled by the user") {
		t.Fatalf("cancel result = %+v", last)
	}
}
