package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/decision"
	"github.com/patrikcze/llmtui/internal/entity"
)

func fakeYieldActionResult(choice string) decision.Result {
	return decision.Result{Answers: map[string]decision.Answer{
		"yield_action": {Type: decision.QuestionChoice, Choice: choice, Confidence: 0.9},
	}}
}

func newYieldShadowRun(t *testing.T) (*Model, *agent.AgentRun) {
	t.Helper()
	m := newTestModel(t)
	m.cfg.DecisionEngine.Enabled = true
	m.cfg.DecisionEngine.YieldShadow = true
	run, err := agent.NewRun("yield-shadow-run", "inspect the bounded objective", agent.DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle(run.Request, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	m.agentLoop.run = run
	return m, run
}

func TestAgentYieldShadowRequiresExplicitOptIn(t *testing.T) {
	m, run := newYieldShadowRun(t)
	m.cfg.DecisionEngine.YieldShadow = false
	m.decisionShadow = &decisionShadowService{service: decision.NewService(true, &fakeDecisionEngine{
		yieldResult: ptrDecisionResult(fakeYieldActionResult("finish")),
	}), model: "fake"}
	if cmd := m.dispatchAgentYieldShadow(run, agent.ExecutionResult{Objective: run.Objective}, agentVerificationPlan{Route: agentVerificationPlanSemantic}); cmd != nil {
		t.Fatal("dispatchAgentYieldShadow returned a command without yield_shadow opt-in")
	}
	if len(m.yieldShadowCorrelations) != 0 || m.yieldShadowMetrics.Total != 0 {
		t.Fatalf("disabled yield shadow changed state: correlations=%d metrics=%+v", len(m.yieldShadowCorrelations), m.yieldShadowMetrics)
	}
}

func ptrDecisionResult(result decision.Result) *decision.Result { return &result }

func TestAgentYieldShadowRecordsAvailableSampleWithoutAuthority(t *testing.T) {
	m, run := newYieldShadowRun(t)
	fake := &fakeDecisionEngine{yieldResult: ptrDecisionResult(fakeYieldActionResult("finish"))}
	m.decisionShadow = &decisionShadowService{service: decision.NewService(true, fake), model: "fake"}
	m.observedFileVersions = map[string]observedFileVersion{
		"path:report.md": {ResourceID: "resource-1", Version: entity.FileVersion{Path: "report.md", Digest: "digest-1", SizeBytes: 12, Complete: true}},
	}
	execution := agent.ExecutionResult{Objective: run.Objective, NewEvidence: true, ToolCalls: []agent.ToolCallRecord{{
		Name: "read_file", Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted,
	}}}
	plan := agentVerificationPlan{Route: agentVerificationPlanSemantic}
	cmd := m.dispatchAgentYieldShadow(run, execution, plan)
	if cmd == nil {
		t.Fatal("dispatchAgentYieldShadow returned nil with explicit opt-in")
	}
	msg, ok := cmd().(agentYieldShadowMsg)
	if !ok {
		t.Fatalf("yield command returned %T, want agentYieldShadowMsg", cmd())
	}
	if _, _ = m.handleAgentYieldShadow(msg); len(m.yieldShadowCorrelations) != 1 {
		t.Fatal("prediction should remain correlated until the authoritative action arrives")
	}
	statusBefore := run.Status
	m.recordAgentYieldShadowActual(run.ID, run.Cycle, "continue")
	if run.Status != statusBefore {
		t.Fatalf("yield shadow changed run status from %s to %s", statusBefore, run.Status)
	}
	if got := m.yieldShadowMetrics.Available; got != 1 {
		t.Fatalf("available = %d, want 1", got)
	}
	if len(m.yieldShadowSamples) != 1 || m.yieldShadowLast.Advice != "finish" || m.yieldShadowLast.ActualAction != "continue" {
		t.Fatalf("sample = %+v, want recorded advice and existing action", m.yieldShadowLast)
	}
}

func TestAgentYieldShadowSeparatesMissingCensoredAndLate(t *testing.T) {
	t.Run("missing prediction", func(t *testing.T) {
		m, run := newYieldShadowRun(t)
		m.decisionShadow = &decisionShadowService{service: decision.NewService(true, &fakeDecisionEngine{err: errors.New("worker unavailable")}), model: "fake"}
		cmd := m.dispatchAgentYieldShadow(run, agent.ExecutionResult{Objective: run.Objective}, agentVerificationPlan{Route: agentVerificationPlanSemantic})
		msg := cmd().(agentYieldShadowMsg)
		m.handleAgentYieldShadow(msg)
		m.recordAgentYieldShadowActual(run.ID, run.Cycle, "verify")
		if m.yieldShadowMetrics.Missing != 1 || m.yieldShadowLast.Outcome != agentYieldShadowOutcomeMissing {
			t.Fatalf("metrics=%+v last=%+v, want one missing outcome", m.yieldShadowMetrics, m.yieldShadowLast)
		}
	})

	t.Run("censored pending prediction", func(t *testing.T) {
		m, run := newYieldShadowRun(t)
		m.decisionShadow = &decisionShadowService{service: decision.NewService(true, &fakeDecisionEngine{yieldResult: ptrDecisionResult(fakeYieldActionResult("verify"))}), model: "fake"}
		if cmd := m.dispatchAgentYieldShadow(run, agent.ExecutionResult{Objective: run.Objective}, agentVerificationPlan{Route: agentVerificationPlanSemantic}); cmd == nil {
			t.Fatal("want pending yield shadow")
		}
		m.censorPendingAgentYieldShadows()
		if m.yieldShadowMetrics.Censored != 1 || m.yieldShadowLast.Outcome != agentYieldShadowOutcomeCensored {
			t.Fatalf("metrics=%+v last=%+v, want one censored outcome", m.yieldShadowMetrics, m.yieldShadowLast)
		}
	})

	t.Run("late valid pair", func(t *testing.T) {
		m, run := newYieldShadowRun(t)
		fake := &fakeDecisionEngine{yieldResult: ptrDecisionResult(fakeYieldActionResult("continue"))}
		m.decisionShadow = &decisionShadowService{service: decision.NewService(true, fake), model: "fake"}
		cmd := m.dispatchAgentYieldShadow(run, agent.ExecutionResult{Objective: run.Objective}, agentVerificationPlan{Route: agentVerificationPlanSemantic})
		m.recordAgentYieldShadowActual(run.ID, run.Cycle, "verify")
		run.Cycle++
		m.handleAgentYieldShadow(cmd().(agentYieldShadowMsg))
		if m.yieldShadowMetrics.Late != 1 || m.yieldShadowLast.Outcome != agentYieldShadowOutcomeLate {
			t.Fatalf("metrics=%+v last=%+v, want one late outcome", m.yieldShadowMetrics, m.yieldShadowLast)
		}
	})
}

func TestAgentYieldShadowProjectionIsBoundedAndVersioned(t *testing.T) {
	m, run := newYieldShadowRun(t)
	m.observedFileVersions = map[string]observedFileVersion{
		"path:report.md": {Version: entity.FileVersion{Path: "report.md", Digest: "digest", SizeBytes: 4, Complete: true}},
	}
	execution := agent.ExecutionResult{Objective: run.Objective, NewEvidence: true, ToolCalls: []agent.ToolCallRecord{{
		Name: "read_file", Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted,
	}}}
	run.Criteria = make([]agent.Criterion, maxAgentYieldShadowCriteria+4)
	for i := range run.Criteria {
		run.Criteria[i].ID = "c"
		run.Criteria[i].Kind = agent.CriterionSemantic
	}
	state := m.buildAgentYieldShadowState(run, execution, agentVerificationPlan{Route: agentVerificationPlanSemantic}, 7)
	if state.SchemaVersion != agentYieldShadowSchemaVersion || len(state.Criteria) != maxAgentYieldShadowCriteria || len(state.ResourceRefs) != 1 {
		t.Fatalf("state = %+v, want versioned bounded projection", state)
	}
	if state.ResourceRefs[0].Digest != "digest" || !state.ResourceRefs[0].Complete {
		t.Fatalf("resource refs = %+v, want delivered file version", state.ResourceRefs)
	}
}

var _ decision.Engine = (*fakeDecisionEngine)(nil)
