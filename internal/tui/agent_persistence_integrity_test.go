package tui

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestVerifiedAgentNoProgressPersistsPartialExecution is the TUI regression
// for audit P2-1: a run stopped by the progress ledger mid-cycle must persist
// what that cycle did — including the file it changed — not only its status.
func TestVerifiedAgentNoProgressPersistsPartialExecution(t *testing.T) {
	steps := []agentScriptStep{
		{toolCalls: []provider.ToolCall{{ID: "w-1", Name: tools.ToolWriteFile, Arguments: `{"path":"notes.txt","content":"draft\n"}`}}},
	}
	for i := 0; i < 8; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{{ID: "l-" + string(rune('a'+i)), Name: tools.ToolListDir, Arguments: `{}`}}})
	}
	m, _ := configureAgentTestModel(t, steps...)
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	store := agent.NewMemoryStore()
	m.agentLoop.store = store
	driveAgentCommands(t, m, m.startVerifiedRun("write notes.txt and then check the workspace", nil))

	live := m.agentLoop.run
	if live.Status != agent.DecisionNoProgress {
		t.Fatalf("run status = %s (%q), want no_progress", live.Status, live.StopReason)
	}
	saved, err := store.Load(context.Background(), live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Status != agent.DecisionNoProgress {
		t.Fatalf("persisted status = %s, want no_progress", saved.Status)
	}
	execution := saved.LatestCycle().Execution
	if execution == nil || !execution.Partial {
		t.Fatalf("persisted execution = %+v, want a partial record", execution)
	}
	if !slices.Contains(execution.ChangedFiles, "notes.txt") {
		t.Errorf("persisted changed files = %v, want notes.txt", execution.ChangedFiles)
	}
	if len(execution.ToolCalls) < 2 || saved.ToolCalls != len(execution.ToolCalls) {
		t.Errorf("persisted tool calls = %d (run total %d), want the cycle's receipts counted", len(execution.ToolCalls), saved.ToolCalls)
	}
	if saved.Revision == 0 {
		t.Error("persisted run has no persistence revision")
	}
}

// TestAgentResumeRestoresLiveToolBudget is the regression for audit P2-3:
// resuming a persisted run used to keep whatever live tool-call count the
// process's previous run left behind (or zero in a fresh process), so the
// live budget disagreed with the persisted total that Decide enforces.
func TestAgentResumeRestoresLiveToolBudget(t *testing.T) {
	now := time.Now()
	run, err := agent.NewRun("resume-budget", "inspect the workspace", agent.DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.BeginContract(now)
	_ = run.CompleteContract([]string{"workspace inspected"}, now)
	_ = run.BeginCycle(run.Request, nil, now)
	calls := []agent.ToolCallRecord{
		{Name: "list_dir", Succeeded: true, Status: agent.ActionExecuted},
		{Name: "read_file", Detail: "a.md", Succeeded: true, Status: agent.ActionExecuted},
		{Name: "read_file", Detail: "b.md", Succeeded: true, Status: agent.ActionExecuted},
	}
	_ = run.CompleteExecution(agent.ExecutionResult{Summary: "blocked on a missing credential", ToolCalls: calls}, now)
	_ = run.CompleteVerification(agent.VerificationResult{Verdict: agent.VerificationBlocked, Summary: "needs a credential"}, now)
	_ = run.WriteMemory(now)
	_ = run.ApplyStop(agent.Decide(run, now), now)
	if run.Status != agent.DecisionParked || run.ToolCalls != 3 {
		t.Fatalf("fixture status=%s toolCalls=%d, want parked with 3", run.Status, run.ToolCalls)
	}

	m, _ := configureAgentTestModel(t)
	m.agentLoop.liveToolCalls = 30 // left behind by an earlier run in this process
	m.agentLoop.cycleBoundaries = []int{4, 9}
	m.handleAgentResume(agentResumeMsg{run: run})

	if m.agentLoop.liveToolCalls != 3 {
		t.Fatalf("live tool calls after resume = %d, want the persisted 3", m.agentLoop.liveToolCalls)
	}
	// Resume begins a fresh cycle immediately, which records exactly one
	// boundary of its own; the previous run's boundaries must be gone.
	if len(m.agentLoop.cycleBoundaries) != 1 || slices.Contains(m.agentLoop.cycleBoundaries, 4) || slices.Contains(m.agentLoop.cycleBoundaries, 9) {
		t.Errorf("cycle boundaries = %v, want only the resumed run's new cycle", m.agentLoop.cycleBoundaries)
	}
}
