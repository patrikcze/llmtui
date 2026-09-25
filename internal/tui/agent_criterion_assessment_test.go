package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/decision"
	"github.com/patrikcze/llmtui/internal/tools"
)

type criterionAssessmentEngine struct {
	states    []criterionAssessmentState
	questions []map[string]decision.Question
}

func (e *criterionAssessmentEngine) Name() string { return "criterion-test" }

func (e *criterionAssessmentEngine) Predict(_ context.Context, state any, questions map[string]decision.Question, _ decision.PredictOptions) (decision.Result, error) {
	snapshot, ok := state.(criterionAssessmentState)
	if !ok {
		return decision.Result{}, context.Canceled
	}
	e.states = append(e.states, snapshot)
	e.questions = append(e.questions, questions)
	answers := make(map[string]decision.Answer, len(questions))
	for id, question := range questions {
		answers[id] = decision.Answer{Type: question.Type, Probability: 0.8}
	}
	return decision.Result{Answers: answers, Routing: decision.Routing{Revision: "criterion-test-revision"}}, nil
}

func (e *criterionAssessmentEngine) Close() error { return nil }

func assessmentCriterion(kind agent.CriterionAssessmentEvidenceKind, target string) agent.Criterion {
	return agent.Criterion{
		ID: "c1", Text: "the report contains the required result", Kind: agent.CriterionSemantic,
		Status: agent.CriterionPending,
		Assessment: &agent.CriterionAssessmentSpec{
			Version: agent.CriterionAssessmentVersion, Proposition: "the report supports the required result",
			EvidenceKind: kind, Target: target,
		},
	}
}

func TestCompileCriterionQuestionsUsesFixedInstructions(t *testing.T) {
	questions := compileCriterionQuestions("c1")
	if len(questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(questions))
	}
	if questions["c1.support"].Type != decision.QuestionNoul || questions["c1.contradiction"].Type != decision.QuestionNoul {
		t.Fatalf("questions = %+v, want two noul questions", questions)
	}
	for id, question := range questions {
		if strings.Contains(question.Instructions, "the report") {
			t.Fatalf("%s instructions contain criterion data: %q", id, question.Instructions)
		}
		if !strings.Contains(question.Instructions, "untrusted") || !strings.Contains(question.Instructions, "Missing evidence") {
			t.Fatalf("%s instructions lost trust/absence guard: %q", id, question.Instructions)
		}
	}
}

func TestBuildCriterionAssessmentStateAdmitsExactCurrentReadOnly(t *testing.T) {
	m := newTestModel(t)
	run := &agent.AgentRun{ID: "criterion-run", Cycle: 2, Status: agent.DecisionRunning}
	m.agentLoop.run = run
	m.agentLoop.observations = agent.NewObservationCache()
	m.agentLoop.observations.Put(tools.ToolReadFile, "report.md", 2, "result: password=secret-value", true)
	execution := agent.ExecutionResult{ToolCalls: []agent.ToolCallRecord{{
		Name: tools.ToolReadFile, Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted,
	}}}

	state, availability := m.buildCriterionAssessmentState(execution, assessmentCriterion(agent.CriterionAssessmentLocalRead, "report.md"))
	if availability != criterionAssessmentAvailable {
		t.Fatalf("availability = %q, want available (state=%+v)", availability, state)
	}
	if !state.Complete || !state.Fresh || !state.Successful || state.Ambiguous {
		t.Fatalf("state completeness = %+v", state)
	}
	if strings.Contains(state.Observation, "secret-value") {
		t.Fatal("observation retained an unredacted secret-shaped value")
	}
	if !strings.Contains(state.Observation, "LLMTUI_UNTRUSTED_BEGIN") {
		t.Fatal("observation is not framed as untrusted data")
	}
}

func TestBuildCriterionAssessmentStateAbstainsOnAmbiguousOrStaleRead(t *testing.T) {
	cases := []struct {
		name       string
		calls      []agent.ToolCallRecord
		wantStatus string
	}{
		{
			name: "duplicate read", calls: []agent.ToolCallRecord{
				{Name: tools.ToolReadFile, Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted},
				{Name: tools.ToolReadFile, Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted},
			}, wantStatus: criterionAssessmentAmbiguous,
		},
		{
			name: "later write", calls: []agent.ToolCallRecord{
				{Name: tools.ToolReadFile, Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted},
				{Name: tools.ToolWriteFile, Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted},
			}, wantStatus: criterionAssessmentStaleEvidence,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.agentLoop.run = &agent.AgentRun{ID: "criterion-run", Cycle: 2, Status: agent.DecisionRunning}
			m.agentLoop.observations = agent.NewObservationCache()
			m.agentLoop.observations.Put(tools.ToolReadFile, "report.md", 2, "complete", true)
			_, got := m.buildCriterionAssessmentState(agent.ExecutionResult{ToolCalls: tc.calls}, assessmentCriterion(agent.CriterionAssessmentLocalRead, "report.md"))
			if got != tc.wantStatus {
				t.Fatalf("availability = %q, want %q", got, tc.wantStatus)
			}
		})
	}
}

func TestCriterionAssessmentShadowRecordsOnlyDiagnostics(t *testing.T) {
	m := newTestModel(t)
	m.cfg.DecisionEngine.Enabled = true
	m.cfg.DecisionEngine.Mode = config.DecisionEngineModeCriterionShadow
	m.agentLoop.run = &agent.AgentRun{
		ID: "criterion-run", Cycle: 2, Status: agent.DecisionRunning,
		Criteria: []agent.Criterion{assessmentCriterion(agent.CriterionAssessmentLocalRead, "report.md")},
	}
	m.agentLoop.observations = agent.NewObservationCache()
	m.agentLoop.observations.Put(tools.ToolReadFile, "report.md", 2, "complete report", true)
	engine := &criterionAssessmentEngine{}
	m.decisionShadow = &decisionShadowService{service: decision.NewService(true, engine), model: "criterion-model"}
	execution := agent.ExecutionResult{Summary: "executor summary must not enter criterion state", ToolCalls: []agent.ToolCallRecord{{
		Name: tools.ToolReadFile, Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted,
	}}}

	cmd := m.dispatchCriterionAssessment(m.agentLoop.run, execution)
	if cmd == nil {
		t.Fatal("criterion shadow command is nil in explicit criterion_shadow mode")
	}
	msg, ok := cmd().(agentCriterionAssessmentMsg)
	if !ok {
		t.Fatalf("message = %T, want agentCriterionAssessmentMsg", msg)
	}
	if len(engine.states) != 1 || len(engine.questions) != 1 || len(engine.questions[0]) != 2 {
		t.Fatalf("engine calls = states %d questions %d, want one state with two questions", len(engine.states), len(engine.questions))
	}
	if strings.Contains(engine.states[0].Observation, execution.Summary) {
		t.Fatal("executor summary leaked into criterion assessment state")
	}
	if m.agentLoop.run.Criteria[0].Status != agent.CriterionPending {
		t.Fatal("criterion shadow changed authoritative criterion status")
	}
	m.handleAgentCriterionAssessment(msg)
	if m.lastDebug.DecisionCriterionAssessmentSignal != criterionAssessmentAdvisoryAbstain {
		t.Fatal("both high support/contradiction should remain an advisory abstention")
	}
	if m.criterionAssessmentMetrics.Total != 1 || m.criterionAssessmentMetrics.Available != 1 {
		t.Fatalf("metrics = %+v, want one available diagnostic", m.criterionAssessmentMetrics)
	}
}

func TestCriterionAssessmentDisabledModeDoesNotDispatch(t *testing.T) {
	m := newTestModel(t)
	m.cfg.DecisionEngine.Enabled = true
	m.cfg.DecisionEngine.Mode = config.DecisionEngineModeShadow
	m.agentLoop.run = &agent.AgentRun{ID: "criterion-run", Cycle: 1, Status: agent.DecisionRunning}
	engine := &criterionAssessmentEngine{}
	m.decisionShadow = &decisionShadowService{service: decision.NewService(true, engine), model: "criterion-model"}
	if cmd := m.dispatchCriterionAssessment(m.agentLoop.run, agent.ExecutionResult{}); cmd != nil {
		t.Fatal("ordinary shadow mode unexpectedly dispatched criterion assessment")
	}
	if len(engine.states) != 0 {
		t.Fatalf("engine states = %d, want zero", len(engine.states))
	}
}

func TestCriterionEvidenceViewsUseStableCriterionOrderAndExactBounds(t *testing.T) {
	m := newTestModel(t)
	criteria := make([]agent.Criterion, 0, 5)
	calls := make([]agent.ToolCallRecord, 0, 5)
	m.agentLoop.observations = agent.NewObservationCache()
	for i := 0; i < 5; i++ {
		target := "report-" + string(rune('a'+i)) + ".md"
		criterion := assessmentCriterion(agent.CriterionAssessmentLocalRead, target)
		criterion.ID = "c" + string(rune('1'+i))
		criteria = append(criteria, criterion)
		calls = append(calls, agent.ToolCallRecord{Name: tools.ToolReadFile, Detail: target, Succeeded: true, Status: agent.ActionExecuted})
		m.agentLoop.observations.Put(tools.ToolReadFile, target, 3, strings.Repeat(string(rune('a'+i)), 512), true)
	}
	run := &agent.AgentRun{ID: "bounded-run", Cycle: 3, Status: agent.DecisionRunning, Criteria: criteria}
	m.agentLoop.run = run
	views := m.criterionEvidenceViews(run, agent.ExecutionResult{ToolCalls: calls})
	if len(views) != verifierObservationMaxViews {
		t.Fatalf("views = %d, want %d", len(views), verifierObservationMaxViews)
	}
	for i, view := range views {
		want := "report-" + string(rune('a'+i)) + ".md"
		if view.Detail != want {
			t.Fatalf("view %d detail = %q, want stable criterion-order detail %q", i, view.Detail, want)
		}
	}
	bytes := 0
	for _, view := range views {
		bytes += len(view.Excerpt)
	}
	if bytes != verifierObservationMaxBytes {
		t.Fatalf("excerpt bytes = %d, want exact cap %d", bytes, verifierObservationMaxBytes)
	}
	if len(run.UnresolvedSemanticCriteria()) != 5 {
		t.Fatal("selecting proof views changed the authoritative unresolved criterion set")
	}
}

func TestCriterionEvidenceViewsOmitOversizedAndResumedObservations(t *testing.T) {
	t.Run("truncated cache entries are omitted without affecting later proof", func(t *testing.T) {
		m := newTestModel(t)
		m.agentLoop.observations = agent.NewObservationCache()
		first := assessmentCriterion(agent.CriterionAssessmentLocalRead, "first.md")
		second := assessmentCriterion(agent.CriterionAssessmentLocalRead, "second.md")
		first.ID, second.ID = "c1", "c2"
		run := &agent.AgentRun{ID: "bounded-run", Cycle: 1, Status: agent.DecisionRunning, Criteria: []agent.Criterion{first, second}}
		m.agentLoop.run = run
		m.agentLoop.observations.Put(tools.ToolReadFile, "first.md", 1, strings.Repeat("f", 2040), true)
		m.agentLoop.observations.Put(tools.ToolReadFile, "second.md", 1, strings.Repeat("s", 16), true)
		views := m.criterionEvidenceViews(run, agent.ExecutionResult{ToolCalls: []agent.ToolCallRecord{
			{Name: tools.ToolReadFile, Detail: "first.md", Succeeded: true, Status: agent.ActionExecuted},
			{Name: tools.ToolReadFile, Detail: "second.md", Succeeded: true, Status: agent.ActionExecuted},
		}})
		if len(views) != 1 || views[0].Detail != "second.md" || len(views[0].Excerpt) != 16 {
			t.Fatalf("views = %+v, want only the later complete view", views)
		}
	})

	t.Run("resumed run starts without proof views", func(t *testing.T) {
		m := newTestModel(t)
		m.agentLoop.observations = agent.NewObservationCache()
		criterion := assessmentCriterion(agent.CriterionAssessmentLocalRead, "report.md")
		run := &agent.AgentRun{ID: "resumed-run", Cycle: 2, Status: agent.DecisionRunning, Criteria: []agent.Criterion{criterion}}
		m.agentLoop.run = run
		m.agentLoop.observations.Put(tools.ToolReadFile, "report.md", 1, "old cycle", true)
		views := m.criterionEvidenceViews(run, agent.ExecutionResult{ToolCalls: []agent.ToolCallRecord{{
			Name: tools.ToolReadFile, Detail: "report.md", Succeeded: true, Status: agent.ActionExecuted,
		}}})
		if len(views) != 0 {
			t.Fatalf("views = %+v, want no stale proof after resume", views)
		}
	})
}
