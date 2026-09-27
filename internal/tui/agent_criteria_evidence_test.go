package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
)

// TestVerifiedAgentBareSemanticPassDoesNotSatisfyEveryCriterion is the TUI
// regression for audit P2-6: a semantic verifier's bare "passed" over two
// criteria used to mark both satisfied and end the run. After a repair that
// reports only c1 satisfied, the run must continue toward c2.
func TestVerifiedAgentBareSemanticPassDoesNotSatisfyEveryCriterion(t *testing.T) {
	repaired := strings.Replace(verifierJSON("passed", "first part done", "", false, false),
		`"criteria":[]`, `"criteria":[{"id":"c1","status":"satisfied"},{"id":"c2","status":"pending"}]`, 1)
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Did the first part."},
		agentScriptStep{text: verifierJSON("passed", "looks done", "", false, false)},
		agentScriptStep{text: repaired},
		agentScriptStep{text: "must stop here"},
	)
	prov.contractReplies = []string{`{"criteria":["do the first part","do the second part"],"needs_user_input":false,"question":"","user_options":[]}`}
	driveAgentCommands(t, m, m.startVerifiedRun("do two parts", nil))

	run := m.agentLoop.run
	if run.Criteria[0].Status != agent.CriterionSatisfied || run.Criteria[1].Status != agent.CriterionPending {
		t.Fatalf("criteria = %+v, want c1 satisfied and c2 still pending", run.Criteria)
	}
	if run.Cycle != 2 || !strings.Contains(run.Cycles[1].Objective, "do the second part") {
		t.Fatalf("cycle=%d objective=%q, want a second cycle for c2", run.Cycle, run.Cycles[len(run.Cycles)-1].Objective)
	}
}

// TestAgentDirectiveShowsCriterionReadFacts checks the executor sees what
// the runtime already observed toward an exact-read criterion before
// verification — as a fact under the criterion, never as a status.
func TestAgentDirectiveShowsCriterionReadFacts(t *testing.T) {
	m, _ := configureAgentTestModel(t)
	run, err := agent.NewRun("facts", "read the report", agent.DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run.PinCriteria([]string{"Read the file report.md"})
	if err := run.BeginCycle("read the report", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	total := int64(300)
	m.agentLoop.run = run
	m.agentLoop.execution = agent.ExecutionResult{ReadObservations: []agent.ReadObservation{
		{Target: "report.md", StartLine: 1, EndLine: 120, TotalLines: &total},
	}}
	directive := m.agentDirective()
	if !strings.Contains(directive, `observed so far: lines 1-120 of 300 of "report.md" delivered contiguously`) {
		t.Fatalf("directive lacks the criterion fact:\n%s", directive)
	}
	if run.Criteria[0].Status != agent.CriterionPending {
		t.Fatal("building the directive changed a criterion status")
	}
}
