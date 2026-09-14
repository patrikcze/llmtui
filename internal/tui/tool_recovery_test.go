package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestVisiblePseudoCallRecoveryIsBoundedAndUsesStructuredRetry(t *testing.T) {
	const pseudo = `<tool_call>{"name":"write_file","arguments":{"path":"unsafe.txt"}}`
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: pseudo, toolCallDiagnostics: provider.ObserveToolCallResponse("test-model", true, pseudo, nil, false, false)},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "retry-write", Name: tools.ToolWriteFile, Arguments: `{"path":"safe.txt","content":"approved later"}`}}},
	)
	m.agentOn = false
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = false
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	driveAgentCommands(t, m, m.dispatch("write the file", nil))

	if len(prov.requests) != 2 {
		t.Fatalf("provider requests = %d, want one bounded reissue", len(prov.requests))
	}
	if m.toolRecoveryAttempts != 1 || m.toolRecoveryReason != provider.ToolRecoveryVisiblePseudoCall {
		t.Fatalf("recovery = attempts:%d reason:%q", m.toolRecoveryAttempts, m.toolRecoveryReason)
	}
	if len(m.pendingCalls) != 1 || m.pendingCalls[0].ID != "retry-write" {
		t.Fatalf("structured retry did not reach approval: %+v", m.pendingCalls)
	}
	for _, message := range m.session.Messages {
		if strings.Contains(message.Content, pseudo) {
			t.Fatalf("pseudo-call leaked into history: %+v", message)
		}
	}
	if !strings.Contains(prov.requests[1].Messages[0].Content, "Tool-call recovery:") {
		t.Fatal("reissue did not receive bounded schema feedback")
	}
	if m.toolRecoveryFeedback != "" {
		t.Fatalf("recovery feedback persisted after request: %q", m.toolRecoveryFeedback)
	}
}

func TestVisiblePseudoCallNeverOverridesNativeToolCall(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = false
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	call := provider.ToolCall{ID: "native-read", Name: tools.ToolReadFile, Arguments: `{"path":"README.md"}`}
	_, cmd := m.handleStreamEvent(streamEventMsg{gen: m.streamGen, ok: true, event: provider.ChatEvent{
		Type:      provider.EventDone,
		ToolCalls: []provider.ToolCall{call},
		ToolCallDiagnostics: []provider.ToolCallDiagnostic{{
			Stage: provider.ToolCallStageIntentSuspected, Classification: provider.ToolCallSuspectedCensored,
		}},
	}})

	if cmd == nil {
		t.Fatal("native tool call was not routed to the ordinary tool path")
	}
	if m.toolRecoveryAttempts != 0 || m.toolRecoveryFeedback != "" {
		t.Fatalf("native call incorrectly triggered recovery: attempts=%d feedback=%q", m.toolRecoveryAttempts, m.toolRecoveryFeedback)
	}
	if len(m.session.Messages) == 0 || len(m.session.Messages[len(m.session.Messages)-1].ToolCalls) != 1 {
		t.Fatalf("native call was not preserved in history: %+v", m.session.Messages)
	}
}

func TestVisiblePseudoCallCannotAlternateWithHiddenMCPRecovery(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	m.toolsNative = true
	m.toolRecoveryAttempts = 1
	m.session.AddUser("create an issue")
	m.transition(turnModelStreaming, turnOutcomeNone)
	m.thinking = true

	_, cmd := m.handleStreamEvent(streamEventMsg{gen: m.streamGen, ok: true, event: provider.ChatEvent{
		Type: provider.EventDone,
		ToolCallDiagnostics: []provider.ToolCallDiagnostic{{
			Stage: provider.ToolCallStageIntentSuspected, Classification: provider.ToolCallSuspectedCensored,
		}},
	}})
	if cmd != nil {
		t.Fatal("exhausted shared recovery budget allowed a second reissue")
	}
	if !strings.Contains(m.notice, "nothing was executed") {
		t.Fatalf("exhausted pseudo-call notice = %q", m.notice)
	}
}
