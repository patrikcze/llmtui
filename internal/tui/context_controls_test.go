package tui

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestContextCommandSurfaceAndStatusAlias(t *testing.T) {
	m := newTestModel(t)
	runCommand(m, "/context")
	if !m.overlayOpen || !strings.Contains(m.viewport.View(), "context") {
		t.Fatal("/context should open the context status overlay")
	}
	m.overlayOpen = false
	runCommand(m, "/context status")
	if !m.overlayOpen || !strings.Contains(m.viewport.View(), "context") {
		t.Fatal("/context status should open the context status overlay")
	}

	var contextCommand slashCommand
	for _, command := range slashCommands() {
		if command.name == "context" {
			contextCommand = command
			break
		}
	}
	for _, want := range []string{"status", "summary", "summarize", "compact", "rebuild", "preview", "refresh", "clear-summary", "strategy"} {
		if !strings.Contains(contextCommand.usage, want) {
			t.Errorf("context usage missing %q: %s", want, contextCommand.usage)
		}
	}
}

func TestContextAliasesSummarizeOlderMessages(t *testing.T) {
	for _, command := range []string{"/context summarize", "/context compact", "/context rebuild", "/compact"} {
		t.Run(command, func(t *testing.T) {
			m := newTestModel(t)
			m.cfg.Context.KeepLastMessages = 1
			m.session.AddUser("old request")
			m.session.AddAssistant("old answer")
			m.session.AddUser("current request")

			runCommand(m, command)
			if m.summary == "" {
				t.Fatalf("%s did not rebuild the session summary", command)
			}
		})
	}
}

func TestContextSnapshotIsReadOnly(t *testing.T) {
	m := newTestModel(t)
	m.summary = "session summary"
	m.ctxStrategy = "auto"
	m.ragLast = nil
	m.session.AddUser("first")
	m.session.AddAssistant("answer")
	m.session.AddUser("second")
	beforeMessages := append([]provider.Message(nil), m.session.Messages...)

	snapshot := m.contextSnapshot()
	if snapshot.Strategy != "auto" || snapshot.Scope != "session" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if m.summary != "session summary" || m.ctxStrategy != "auto" {
		t.Fatal("context snapshot changed session context state")
	}
	if len(m.ragLast) != 0 {
		t.Fatal("context snapshot changed last RAG results")
	}
	if !reflect.DeepEqual(m.session.Messages, beforeMessages) {
		t.Fatal("context snapshot changed session messages")
	}
}

func TestContextSummaryLabelsAgentScopedProjection(t *testing.T) {
	m := newTestModel(t)
	m.summary = "ordinary session summary"
	m.agentLoop = &agentLoopState{run: &agent.AgentRun{
		ID: "run-123", Cycle: 2, Status: agent.DecisionRunning, Stage: agent.StageExecutor,
		Limits: agent.DefaultLimits(),
	}}
	m.agentContextSummary = agentScopedSummary{RunID: "run-123", Cycle: 2, Summary: "agent projection"}

	snapshot := m.contextSnapshot()
	if snapshot.SummaryKind != "agent-scoped request summary" || snapshot.SummaryPreview != "agent projection" {
		t.Fatalf("agent summary snapshot = %+v", snapshot)
	}
	if m.summary != "ordinary session summary" {
		t.Fatal("agent-scoped snapshot overwrote ordinary session summary")
	}
}

func TestContextSnapshotShowsCompletedAgentRunUnderSessionScope(t *testing.T) {
	m := newTestModel(t)
	m.agentLoop = &agentLoopState{run: &agent.AgentRun{
		ID: "run-complete", Cycle: 2, Status: agent.DecisionDone, Stage: agent.StageStopCheck,
		Limits: agent.DefaultLimits(),
	}}

	snapshot := m.contextSnapshot()
	if snapshot.Scope != "session" || snapshot.Agent == nil {
		t.Fatalf("completed run snapshot = %+v", snapshot)
	}
	if snapshot.Agent.Status != string(agent.DecisionDone) {
		t.Fatalf("completed run status = %q", snapshot.Agent.Status)
	}
}

func TestContextSnapshotDoesNotAlterAgentRun(t *testing.T) {
	m := newTestModel(t)
	run := &agent.AgentRun{
		ID: "run-immutable", Cycle: 2, Status: agent.DecisionRunning, Stage: agent.StageExecutor,
		Limits: agent.DefaultLimits(), Objective: "keep this objective", StartContextCaptured: true,
		StartSummary: "captured start summary", StartTurns: []agent.ContextTurn{{Role: "user", Content: "prior request"}},
		Criteria: []agent.Criterion{{ID: "c1", Text: "preserve state", Status: agent.CriterionPending}},
	}
	m.agentLoop = &agentLoopState{run: run, historyStart: 0}
	before, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal before snapshot: %v", err)
	}
	_ = m.contextSnapshot()
	after, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal after snapshot: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("context snapshot mutated AgentRun:\nbefore %s\nafter  %s", before, after)
	}
}

func TestCommitPreparedKeepsAgentSummarySeparate(t *testing.T) {
	m := newTestModel(t)
	m.summary = "ordinary session summary"
	m.agentLoop = &agentLoopState{run: &agent.AgentRun{
		ID: "run-summary", Cycle: 2, Status: agent.DecisionRunning, Limits: agent.DefaultLimits(),
	}}
	m.commitPrepared(preparedRequest{agentScoped: true, summary: "agent-only summary"})
	if m.summary != "ordinary session summary" {
		t.Fatalf("session summary = %q, want unchanged", m.summary)
	}
	if m.agentContextSummary.RunID != "run-summary" || m.agentContextSummary.Summary != "agent-only summary" {
		t.Fatalf("agent summary = %+v", m.agentContextSummary)
	}
}

func TestContextMutationBlockedDuringUnsafeLifecycle(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*Model)
		want  string
	}{
		{"streaming", func(m *Model) { m.state = turnModelStreaming }, "model response is streaming"},
		{"native batch", func(m *Model) { m.state = turnExecutingTools }, "tool batch is running"},
		{"approval", func(m *Model) { m.pendingCalls = []tools.Call{{ID: "call-1"}} }, "tool approval is pending"},
		{"budget", func(m *Model) { m.pendingCalls = []tools.Call{{ID: "call-1"}}; m.pendingBudget = true }, "tool-round budget extension is pending"},
		{"ask user", func(m *Model) { m.pendingAsk = &pendingAskUser{} }, "ask_user response is pending"},
		{"verifier", func(m *Model) {
			m.agentLoop = &agentLoopState{run: &agent.AgentRun{Cycle: 2, Status: agent.DecisionRunning}, verifying: true}
		}, "verifier is active"},
		{"agent", func(m *Model) {
			m.agentLoop = &agentLoopState{run: &agent.AgentRun{Cycle: 2, Status: agent.DecisionRunning}}
		}, "agent cycle 2 is executing"},
		{"agent input", func(m *Model) {
			m.agentLoop = &agentLoopState{run: &agent.AgentRun{Cycle: 2, Status: agent.DecisionNeedsUserInput}}
		}, "agent is waiting for required user input"},
		{"harmony continuation", func(m *Model) { m.streamContinuation = &provider.ProviderContinuation{} }, "Harmony tool continuation is incomplete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, command := range []string{"summarize", "compact", "rebuild", "clear-summary", "strategy none"} {
				t.Run(command, func(t *testing.T) {
					m := newTestModel(t)
					m.summary = "unchanged"
					m.ctxStrategy = "auto"
					test.setup(m)
					cmdContext(m, command)
					if !strings.Contains(m.errText, test.want) {
						t.Fatalf("error = %q, want %q", m.errText, test.want)
					}
					if m.summary != "unchanged" || m.ctxStrategy != "auto" {
						t.Fatal("blocked context mutation changed context state")
					}
				})
			}
		})
	}
}

func TestContextMutationBlockedByThinkingMirror(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	cmdContext(m, "clear-summary")
	if !strings.Contains(m.errText, "model response is streaming") {
		t.Fatalf("error = %q", m.errText)
	}
}

func TestContextReadOnlyInspectionRemainsAvailableDuringUnsafeLifecycle(t *testing.T) {
	m := newTestModel(t)
	m.summary = "unchanged"
	m.state = turnModelStreaming
	cmdContext(m, "status")
	if !m.overlayOpen || m.errText != "" {
		t.Fatalf("status should stay available while streaming: overlay=%t error=%q", m.overlayOpen, m.errText)
	}
	m.overlayOpen = false
	cmdContext(m, "preview")
	if !m.overlayOpen || m.summary != "unchanged" {
		t.Fatal("preview should be read-only while streaming")
	}
}

func TestContextPreviewAndRefreshDoNotRebuildSummary(t *testing.T) {
	m := newTestModel(t)
	m.summary = "existing summary"
	m.cfg.Context.KeepLastMessages = 1
	m.session.AddUser("old request")
	m.session.AddAssistant("old answer")
	m.session.AddUser("current request")
	for _, command := range []string{"preview", "refresh", "summary"} {
		m.overlayOpen = false
		cmdContext(m, command)
		if m.summary != "existing summary" {
			t.Fatalf("/context %s rebuilt or changed the summary", command)
		}
		if !m.overlayOpen {
			t.Fatalf("/context %s did not open an overlay", command)
		}
	}
}
