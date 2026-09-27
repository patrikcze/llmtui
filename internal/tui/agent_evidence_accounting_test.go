package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func activeExecutorModel(t *testing.T) *Model {
	t.Helper()
	m, _ := configureAgentTestModel(t)
	run, err := agent.NewRun("evidence", "inspect the notes", agent.DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("inspect the notes", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	m.agentLoop.run = run
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	return m
}

// readResult mirrors a real read: its typed metadata carries the source
// digest the progress ledger's identity is built from.
func readResult(output string) tools.Result {
	return tools.Result{
		Call:   tools.Call{ID: "r", Tool: tools.ToolReadFile, Path: "notes.txt"},
		Output: output,
		Meta:   tools.ResultMeta{Outcome: tools.OutcomeOK, Effect: tools.EffectNone, SourceDigest: "sha256:" + output},
	}
}

// TestNewEvidenceMeansNewInformation is the regression for audit P2-4:
// NewEvidence used to be set by any executed call, so rereading unchanged
// content or failing looked like progress to the stop policy's retry gate.
func TestNewEvidenceMeansNewInformation(t *testing.T) {
	executed := uniformActionStatuses(1, agent.ActionExecuted)

	t.Run("first read is new, an unchanged reread is not, a changed result is", func(t *testing.T) {
		m := activeExecutorModel(t)
		m.recordAgentToolResultsCount([]tools.Result{readResult("hello")}, false, executed)
		if !m.agentLoop.execution.NewEvidence {
			t.Fatal("first read: NewEvidence = false")
		}
		m.agentLoop.execution.NewEvidence = false // a new cycle starts from false
		m.recordAgentToolResultsCount([]tools.Result{readResult("hello")}, false, executed)
		if m.agentLoop.execution.NewEvidence {
			t.Fatal("unchanged reread: NewEvidence = true")
		}
		m.recordAgentToolResultsCount([]tools.Result{readResult("hello, changed")}, false, executed)
		if !m.agentLoop.execution.NewEvidence {
			t.Fatal("changed content: NewEvidence = false")
		}
	})

	t.Run("a failed call is not new evidence", func(t *testing.T) {
		m := activeExecutorModel(t)
		failed := readResult("")
		failed.Err = errors.New("permission problem")
		failed.Meta = tools.ResultMeta{Outcome: tools.OutcomeFailed, Error: &tools.ErrorInfo{Code: "unsupported_content"}}
		m.recordAgentToolResultsCount([]tools.Result{failed}, false, executed)
		if m.agentLoop.execution.NewEvidence {
			t.Fatal("failed call: NewEvidence = true")
		}
	})

	t.Run("an observational failure is new once", func(t *testing.T) {
		m := activeExecutorModel(t)
		missing := readResult("")
		missing.Err = errors.New("not found")
		missing.Meta = tools.ResultMeta{Outcome: tools.OutcomeFailed, Error: &tools.ErrorInfo{Code: "not_found"}}
		m.recordAgentToolResultsCount([]tools.Result{missing}, false, executed)
		if !m.agentLoop.execution.NewEvidence {
			t.Fatal("first not_found: NewEvidence = false")
		}
		m.agentLoop.execution.NewEvidence = false
		m.recordAgentToolResultsCount([]tools.Result{missing}, false, executed)
		if m.agentLoop.execution.NewEvidence {
			t.Fatal("repeated not_found: NewEvidence = true")
		}
	})

	t.Run("a changed file is always new", func(t *testing.T) {
		m := activeExecutorModel(t)
		write := tools.Result{Call: tools.Call{Tool: tools.ToolWriteFile, Path: "a.txt"}, Meta: tools.ResultMeta{Outcome: tools.OutcomeOK, Effect: tools.EffectChanged}}
		m.recordAgentToolResultsCount([]tools.Result{write}, false, executed)
		m.agentLoop.execution.NewEvidence = false
		m.recordAgentToolResultsCount([]tools.Result{write}, false, executed)
		if !m.agentLoop.execution.NewEvidence {
			t.Fatal("changed file: NewEvidence = false")
		}
	})
}

// TestRejectedCallsAreNotEvidenceOrBudget covers the invalid-argument half
// of P2-4: a call refused for its arguments ran nothing, so it must not
// consume the live tool budget or count as evidence — while still being
// recorded, and still observed by the progress ledger.
func TestRejectedCallsAreNotEvidenceOrBudget(t *testing.T) {
	m := activeExecutorModel(t)
	calls := []tools.Call{
		{ID: "a", Tool: tools.ToolReadFile, InputErr: "unknown argument(s) \"start_line\""},
		{ID: "b", Tool: tools.ToolReadFile, InputErr: "\"limit\" must be an integer, got string"},
	}
	plan := newToolBatchPlan(calls)
	executed := make([]tools.Result, len(calls))
	for i, call := range calls {
		executed[i] = tools.Result{Call: call, Err: errors.New("invalid arguments"), Meta: tools.ResultMeta{Outcome: tools.OutcomeFailed, Error: &tools.ErrorInfo{Code: "invalid_arguments"}}}
	}
	merged, observed, statuses := plan.mergeResults(executed)
	if len(observed) != 2 {
		t.Fatalf("observed = %d, want rejected calls still observed by the progress ledger", len(observed))
	}
	for _, status := range statuses {
		if status != agent.ActionRejected {
			t.Fatalf("statuses = %v, want rejected", statuses)
		}
	}
	m.recordAgentToolResultsCount(merged, false, statuses)
	if m.agentLoop.liveToolCalls != 0 || m.agentLoop.execution.NewEvidence {
		t.Fatalf("liveToolCalls=%d NewEvidence=%v, want 0/false", m.agentLoop.liveToolCalls, m.agentLoop.execution.NewEvidence)
	}
	if got := len(m.agentLoop.execution.ToolCalls); got != 2 {
		t.Fatalf("recorded receipts = %d, want both rejected calls kept", got)
	}
	if n := agent.ExecutedToolCalls(m.agentLoop.execution.ToolCalls); n != 0 {
		t.Fatalf("ExecutedToolCalls = %d, want 0", n)
	}
}

func TestTruncationIsNotNewEvidence(t *testing.T) {
	m := activeExecutorModel(t)
	m.recordAgentTruncation()
	if m.agentLoop.execution.NewEvidence {
		t.Fatal("truncation set NewEvidence")
	}
}

func TestAskUserToolNameMatchesToolsConstant(t *testing.T) {
	if agent.AskUserToolName() != tools.ToolAskUser {
		t.Fatalf("agent ask_user name %q != tools %q", agent.AskUserToolName(), tools.ToolAskUser)
	}
}

// TestVerifiedAgentDifferentlyInvalidCallsDoNotEarnRetries is the end-to-end
// shape of the failure P2-4 described: an executor that only emits invalid
// calls — differently wrong each time — kept earning retries, because every
// invalid call set NewEvidence. The trailing failure still earns its one
// controller recovery cycle; after that, with nothing new observed, the stop
// policy ends the run, and no tool budget is spent at any point.
func TestVerifiedAgentDifferentlyInvalidCallsDoNotEarnRetries(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "c1", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt","limit":"all"}`}}},
		agentScriptStep{text: "I could not read it."},
		agentScriptStep{text: verifierJSON("failed", "the file was never read", "", true, false)},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "c2", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt","offset":"first"}`}}},
		agentScriptStep{text: "Still could not read it."},
		agentScriptStep{text: verifierJSON("failed", "the file was never read", "", true, false)},
		agentScriptStep{text: "must never be requested"},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("read a.txt", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionFailed || run.Cycle != 2 {
		t.Fatalf("status=%s cycle=%d stop=%q, want failed after the single recovery cycle", run.Status, run.Cycle, run.StopReason)
	}
	if run.ToolCalls != 0 || m.agentLoop.liveToolCalls != 0 {
		t.Fatalf("tool budget used: run=%d live=%d, want 0", run.ToolCalls, m.agentLoop.liveToolCalls)
	}
	if len(prov.requests) != 7 {
		t.Fatalf("provider requests = %d, want contract + 3 per cycle", len(prov.requests))
	}
}
