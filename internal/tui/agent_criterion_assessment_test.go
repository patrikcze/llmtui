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
