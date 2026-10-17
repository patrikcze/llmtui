package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
)

func agentCompactionModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	m.agentLoop.run = &agent.AgentRun{
		ID: "compaction-test", Request: "complete the task", Objective: "complete the task",
		Cycle: 1, Stage: agent.StageExecutor, Status: agent.DecisionRunning,
	}
	m.ctxStrategy = "auto"
	m.cfg.Context.SummarizeAfterMessages = 4
	m.cfg.Context.KeepLastMessages = 2
	m.cfg.Context.MaxContextTokens = 100_000
	m.session.Messages = benchmarkToolRounds(7, 32)
	m.session.Messages[1].ToolCalls[0].Name = "ask_user"
	m.session.Messages[2].ToolName = "ask_user"
	m.session.Messages[2].Content = `{"answer":"Multiply","grants_authorization":false}`
	m.session.Messages[3].ToolCalls[0].Name = "edit_file"
	m.session.Messages[4].ToolName = "edit_file"
	m.session.Messages[4].Content = "edited src/math/add.go: replaced 1 exact occurrence"
	return m
}

// The live Gemma E4B run compacted every two-message tool exchange despite
// using only about 10k of a 61k-token budget. Earlier successful edits and
// ask_user answers disappeared from the executor's verbatim history.
func TestAgentKeepsToolHistoryWhileItFits(t *testing.T) {
	m := agentCompactionModel(t)
	prepared, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.decision.Compress || prepared.compacted != 0 {
		t.Fatalf("unnecessary agent compaction: decision=%+v compacted=%d", prepared.decision, prepared.compacted)
	}
	var results int
	var sawAnswer, sawEdit bool
	for _, message := range prepared.composed.Messages {
		if message.Role == provider.RoleTool {
			results++
			sawAnswer = sawAnswer || strings.Contains(message.Content, `"answer":"Multiply"`)
			sawEdit = sawEdit || strings.Contains(message.Content, "edited src/math/add.go")
		}
	}
	if results != 7 || !sawAnswer || !sawEdit {
		t.Fatalf("retained %d tool results, answer=%t edit=%t; want all 7 and the earlier decisions", results, sawAnswer, sawEdit)
	}
}

// A real budget limit must still compact an active agent cycle and report the
// lost tool history to the executor.
func TestAgentCompactsToolHistoryAtBudget(t *testing.T) {
	m := agentCompactionModel(t)
	full, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.Context.MaxContextTokens = full.estimate.Total + m.cfg.Context.ReserveResponseTokens - 100
	prepared, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.decision.Compress || prepared.decision.Strategy != "summarize" || prepared.compacted == 0 || prepared.compactedToolResults == 0 {
		t.Fatalf("budget did not compact tool history: decision=%+v compacted=%d results=%d", prepared.decision, prepared.compacted, prepared.compactedToolResults)
	}
	m.noteAgentCompaction(prepared)
	var recorded bool
	for _, event := range m.agentLoop.run.Events {
		recorded = recorded || event.Kind == "context_compressed"
	}
	if !recorded || !strings.Contains(m.agentDirective(nil), "tool result(s) from this cycle were compacted out") {
		t.Fatal("agent did not record compression and explain the lost tool history")
	}
}
