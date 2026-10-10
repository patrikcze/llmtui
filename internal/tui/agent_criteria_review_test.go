package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/agentverify"
)

const reviewContractReply = `{"criteria":["list the files","summarize each file"],"needs_user_input":false,"question":"","user_options":[]}`

// configureCriteriaReviewModel starts a run with agent.review_criteria on and
// drives it to the review pause.
func configureCriteriaReviewModel(t *testing.T, contractReplies []string, steps ...agentScriptStep) (*Model, *scriptedAgentProvider) {
	t.Helper()
	m, prov := configureAgentTestModel(t, steps...)
	m.cfg.Agent.ReviewCriteria = true
	prov.contractReplies = contractReplies
	driveAgentCommands(t, m, m.startVerifiedRun("summarize the files in this folder", nil))
	if !m.agentCriteriaReviewPending() {
		t.Fatalf("review pending = false, run = %+v", m.agentLoop.run)
	}
	return m, prov
}

// submitReview types text into the composer and presses Enter through send,
// the same path a keypress takes.
func submitReview(t *testing.T, m *Model, text string) {
	t.Helper()
	m.input.SetValue(text)
	driveAgentCommands(t, m, m.send())
}

func TestCriteriaReviewPausesBeforeExecutorAndEnterAccepts(t *testing.T) {
	m, prov := configureCriteriaReviewModel(t, []string{reviewContractReply},
		agentScriptStep{text: "Listed and summarized the files."},
		agentScriptStep{text: verifierJSONSatisfying("contract satisfied", "c1", "c2")},
	)
	run := m.agentLoop.run
	if len(prov.requests) != 1 {
		t.Fatalf("requests = %d, want only the contract request before review", len(prov.requests))
	}
	if run.Status != agent.DecisionNeedsUserInput || run.Stage != agent.StageContract || run.HasCriteria() || run.Cycle != 0 {
		t.Fatalf("run = %s/%s criteria=%d cycle=%d, want an unpinned contract pause", run.Status, run.Stage, len(run.Criteria), run.Cycle)
	}
	if run.PausedAt.IsZero() {
		t.Fatal("review pause must not count against max_elapsed")
	}
	view := m.renderLiveTail()
	for _, want := range []string{"review agent criteria", "Goal: summarize the files in this folder", "1. list the files", "2. summarize each file"} {
		if !strings.Contains(view, want) {
			t.Fatalf("review view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, agentCriteriaReviewReason) {
		t.Fatalf("review also rendered as a contract question:\n%s", view)
	}

	submitReview(t, m, "")

	if m.agentCriteriaReviewPending() {
		t.Fatal("review still pending after enter")
	}
	if len(run.Criteria) != 2 || run.Criteria[0].Text != "list the files" || run.Criteria[1].Text != "summarize each file" {
		t.Fatalf("criteria = %+v, want the proposed list pinned", run.Criteria)
	}
	if len(prov.requests) != 3 || !strings.Contains(prov.requests[1].Messages[0].Content, "list the files") {
		t.Fatalf("requests = %d, want executor with the reviewed criteria then verifier", len(prov.requests))
	}
	if run.Status != agent.DecisionDone {
		t.Fatalf("run status = %s, want complete", run.Status)
	}
}

func TestCriteriaReviewEdits(t *testing.T) {
	tests := []struct {
		name    string
		inputs  []string
		want    []string
		wantErr string
	}{
		{name: "add", inputs: []string{"+ cite each file path"}, want: []string{"list the files", "summarize each file", "cite each file path"}},
		{name: "drop", inputs: []string{"-1"}, want: []string{"summarize each file"}},
		{name: "replace", inputs: []string{"2: summarize each file in one line"}, want: []string{"list the files", "summarize each file in one line"}},
		{name: "replace with dot", inputs: []string{"1. list only .go files"}, want: []string{"list only .go files", "summarize each file"}},
		{name: "drop out of range", inputs: []string{"-3"}, want: []string{"list the files", "summarize each file"}, wantErr: "no criterion 3"},
		{name: "cannot drop the last", inputs: []string{"-1", "-1"}, want: []string{"summarize each file"}, wantErr: "at least one criterion"},
		{name: "empty add", inputs: []string{"+"}, want: []string{"list the files", "summarize each file"}, wantErr: "after +"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, prov := configureCriteriaReviewModel(t, []string{reviewContractReply},
				agentScriptStep{text: "Done."},
				agentScriptStep{text: verifierJSON("passed", "contract satisfied", "", false, false)},
			)
			for _, input := range tt.inputs {
				submitReview(t, m, input)
			}
			if !m.agentCriteriaReviewPending() {
				t.Fatal("an edit must keep the review open")
			}
			if len(prov.requests) != 1 {
				t.Fatalf("requests = %d, want no model request for a local edit", len(prov.requests))
			}
			if tt.wantErr != "" && !strings.Contains(m.errText, tt.wantErr) {
				t.Fatalf("errText = %q, want %q", m.errText, tt.wantErr)
			}
			if tt.wantErr == "" && m.errText != "" {
				t.Fatalf("errText = %q, want none", m.errText)
			}

			submitReview(t, m, "ok")

			run := m.agentLoop.run
			if len(run.Criteria) != len(tt.want) {
				t.Fatalf("criteria = %+v, want %q", run.Criteria, tt.want)
			}
			for i, want := range tt.want {
				if run.Criteria[i].Text != want || run.Criteria[i].ID != "c"+string(rune('1'+i)) {
					t.Fatalf("criterion %d = %+v, want %q", i, run.Criteria[i], want)
				}
			}
		})
	}
}

func TestCriteriaReviewAddStopsAtMaxCriteria(t *testing.T) {
	m, _ := configureCriteriaReviewModel(t, []string{reviewContractReply})
	for i := len(m.agentLoop.criteriaReview.criteria); i < agent.MaxCriteria; i++ {
		submitReview(t, m, "+ extra")
	}
	submitReview(t, m, "+ one too many")
	if got := len(m.agentLoop.criteriaReview.criteria); got != agent.MaxCriteria {
		t.Fatalf("criteria = %d, want capped at %d", got, agent.MaxCriteria)
	}
	if !strings.Contains(m.errText, "at most") {
		t.Fatalf("errText = %q, want the cap explained", m.errText)
	}
}

func TestCriteriaReviewFeedbackProposesNewCriteria(t *testing.T) {
	revised := `{"criteria":["list only the Go files","summarize each Go file"],"needs_user_input":false,"question":"","user_options":[]}`
	m, prov := configureCriteriaReviewModel(t, []string{reviewContractReply, revised})

	submitReview(t, m, "only look at Go files")

	if len(prov.requests) != 2 {
		t.Fatalf("requests = %d, want a second contract request", len(prov.requests))
	}
	payload := prov.requests[1].Messages[1].Content
	for _, want := range []string{"only look at Go files", "1. list the files", "2. summarize each file"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("revision request missing %q: %s", want, payload)
		}
	}
	if m.agentLoop.run.Request != "summarize the files in this folder" {
		t.Fatalf("request = %q, want the original request unchanged", m.agentLoop.run.Request)
	}
	if !m.agentCriteriaReviewPending() {
		t.Fatal("revised criteria must be reviewed again")
	}
	if got := m.agentLoop.criteriaReview.criteria; len(got) != 2 || got[0] != "list only the Go files" {
		t.Fatalf("review criteria = %q, want the revised proposal", got)
	}
}

func TestCriteriaReviewEditDropsProposedAssessments(t *testing.T) {
	m, _ := configureAgentTestModel(t)
	m.cfg.Agent.ReviewCriteria = true
	run, err := agent.NewRun("review-assessment", "read report.md", agent.DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginContract(time.Now()); err != nil {
		t.Fatal(err)
	}
	m.agentLoop.run = run
	m.agentLoop.contractGen = 1
	m.resetAgentContext()
	_, _ = m.handleAgentContract(agentContractMsg{
		runID: run.ID, gen: 1,
		out: agentverify.ContractOutput{Contract: agentverify.Contract{
			Criteria: []string{"read report.md"},
			Assessments: map[int]agent.CriterionAssessmentSpec{
				0: {Version: 1, Proposition: "the report read supports the criterion", EvidenceKind: agent.CriterionAssessmentLocalRead, Target: "report.md"},
			},
		}},
	})
	if !m.agentCriteriaReviewPending() || run.HasCriteria() {
		t.Fatalf("review pending = %v criteria = %d, want a pause before pinning", m.agentCriteriaReviewPending(), len(run.Criteria))
	}
	_ = m.handleAgentCriteriaReviewInput("1: read report.md and quote its title", nil)
	if m.agentLoop.criteriaReview.assessments != nil {
		t.Fatal("assessments keyed to the proposed list must be dropped after an edit")
	}
}

func TestCriteriaReviewCancel(t *testing.T) {
	m, prov := configureCriteriaReviewModel(t, []string{reviewContractReply})
	m.input.SetValue("/agent cancel")
	driveAgentCommands(t, m, m.runSlashCommand())
	if m.agentLoop.run.Status != agent.DecisionCancelled {
		t.Fatalf("status = %s, want cancelled", m.agentLoop.run.Status)
	}
	if m.agentCriteriaReviewPending() {
		t.Fatal("a cancelled run must not keep its review")
	}
	if strings.Contains(m.renderLiveTail(), "review agent criteria") {
		t.Fatal("review still rendered after cancel")
	}
	if len(prov.requests) != 1 {
		t.Fatalf("requests = %d, want no executor request", len(prov.requests))
	}
}

func TestCriteriaReviewOffKeepsRunsAutomatic(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Done."},
		agentScriptStep{text: verifierJSONSatisfying("contract satisfied", "c1", "c2")},
	)
	prov.contractReplies = []string{reviewContractReply}
	driveAgentCommands(t, m, m.startVerifiedRun("summarize the files in this folder", nil))
	if m.agentCriteriaReviewPending() || len(prov.requests) != 3 || m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("review=%v requests=%d status=%s, want an uninterrupted run", m.agentCriteriaReviewPending(), len(prov.requests), m.agentLoop.run.Status)
	}
}
