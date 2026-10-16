package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func thinkingModel(t *testing.T, enabled bool) *Model {
	t.Helper()
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.toolRunner.SequentialThinking = enabled
	return m
}

func TestSequentialThinkingIsOptIn(t *testing.T) {
	if _, ok := specByName(thinkingModel(t, false).eligibleToolSpecs(), tools.ToolSequentialThinking); ok {
		t.Fatal("sequential_thinking offered while disabled")
	}
	if _, ok := specByName(thinkingModel(t, true).eligibleToolSpecs(), tools.ToolSequentialThinking); !ok {
		t.Fatal("sequential_thinking not offered while enabled")
	}
	off := thinkingModel(t, true)
	off.toolsOn = false
	if off.sequentialThinkingAvailable() {
		t.Fatal("sequential_thinking available with tools off")
	}
}

func TestSequentialThinkingIsNeverAgentEvidence(t *testing.T) {
	m := thinkingModel(t, true)
	m.agentLoop = &agentLoopState{}
	call := tools.CallsFromNative([]provider.ToolCall{{ID: "1", Name: tools.ToolSequentialThinking,
		Arguments: `{"thought":"plan","thought_number":1,"total_thoughts":2,"next_thought_needed":true}`}})[0]
	res := m.toolRunner.Execute(call)
	if res.Err != nil {
		t.Fatalf("execute: %v", res.Err)
	}
	if m.isNewAgentEvidence(res, agent.ToolCallRecord{Name: call.Tool, Succeeded: true}) {
		t.Fatal("a thinking step counted as new agent evidence")
	}
	if detail := toolCallDetail(call); strings.Contains(detail, "plan") {
		t.Fatalf("tool call detail leaks thought text: %q", detail)
	}
}

func TestResetEntitiesClearsThinkingLog(t *testing.T) {
	m := thinkingModel(t, true)
	call := tools.CallsFromNative([]provider.ToolCall{{ID: "1", Name: tools.ToolSequentialThinking,
		Arguments: `{"thought":"plan","thought_number":1,"total_thoughts":2,"next_thought_needed":true}`}})[0]
	m.toolRunner.Execute(call)
	m.resetEntities()
	res := m.toolRunner.Execute(call)
	var status struct {
		Length int `json:"thought_history_length"`
	}
	if err := json.Unmarshal([]byte(res.Output), &status); err != nil || status.Length != 1 {
		t.Fatalf("log survived a session reset: %s", res.Output)
	}
}
