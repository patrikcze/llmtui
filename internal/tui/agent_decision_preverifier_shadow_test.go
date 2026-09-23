package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/decision"
)

// fakePreVerifierResult builds a valid Result for preVerifierQuestions (not
// the post-cycle cycle_action set) — semantic_verifier_needed and
// evidence_sufficient, and nothing else, so decision.Service's
// ValidateResult accepts it for that specific question map.
func fakePreVerifierResult(verifierNeeded, evidenceSufficient float64) decision.Result {
	return decision.Result{Answers: map[string]decision.Answer{
		"semantic_verifier_needed": {Type: decision.QuestionNoul, Probability: verifierNeeded},
		"evidence_sufficient":      {Type: decision.QuestionNoul, Probability: evidenceSufficient},
	}}
}

// TestPreVerifierSnapshotExcludesSemanticOutput covers spec items 1 and 2:
// a snapshot built before a verifier's CriteriaUpdates land must not (and,
// being a plain value copy, structurally cannot) reflect them, proving the
// pre-verifier dispatch — which builds its state synchronously before any
// verifier call exists — captures deterministic-only evidence.
func TestPreVerifierSnapshotExcludesSemanticOutput(t *testing.T) {
	m := newTestModel(t)
	run := &agent.AgentRun{
		ID: "run-preverifier", Cycle: 1, Status: agent.DecisionRunning,
		Request: "task", Objective: "objective", Limits: agent.Limits{MaxCycles: 8},
		Criteria: []agent.Criterion{{ID: "c1", Kind: agent.CriterionSemantic, Status: agent.CriterionPending}},
	}
	execution := agent.ExecutionResult{}

	preState := m.buildAgentDecisionShadowState(run, execution)
	if len(preState.Criteria) != 1 || preState.Criteria[0].Status != string(agent.CriterionPending) {
		t.Fatalf("pre-verifier snapshot criteria = %+v, want pending", preState.Criteria)
	}

	// Simulate exactly what AgentRun.CompleteVerification's
	// ApplyCriteriaUpdates does when a semantic verifier resolves a
	// criterion — this must never be visible in a snapshot taken earlier.
	run.ApplyCriteriaUpdates([]agent.CriterionUpdate{{ID: "c1", Status: agent.CriterionSatisfied, Note: "semantic verifier passed it"}}, 1)

	postState := m.buildAgentDecisionShadowState(run, execution)
	if postState.Criteria[0].Status != string(agent.CriterionSatisfied) {
		t.Fatalf("post-verifier snapshot criteria = %+v, want satisfied", postState.Criteria)
	}
	if preState.Criteria[0].Status == postState.Criteria[0].Status {
		t.Fatal("pre-verifier snapshot was not independent of the later verifier update — it leaked in")
	}
}

// TestPreVerifierDoesNotBlockRealVerifierDispatch covers spec items 3 and 4:
// a slow/timing-out pre-verifier Laya call must never delay or gate the
// real semantic verifier request, which shares the same tea.Batch dispatch
// site.
func TestPreVerifierDoesNotBlockRealVerifierDispatch(t *testing.T) {
	old := decisionShadowTimeout
	decisionShadowTimeout = 20 * time.Millisecond
	t.Cleanup(func() { decisionShadowTimeout = old })

	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	wireFakeDecisionShadow(m, &fakeDecisionEngine{delay: time.Second})

	done := make(chan struct{})
	go func() {
		driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent run did not complete promptly; pre-verifier shadow appears to have blocked the real verifier dispatch")
	}
	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("status = %s, want done — the real verifier request must have completed independently of the hung Laya engine", m.agentLoop.run.Status)
	}
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want contract + executor + real verifier request", len(prov.requests))
	}
}

// TestPreVerifierCrashDoesNotAffectAgent covers spec item 5.
func TestPreVerifierCrashDoesNotAffectAgent(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	wireFakeDecisionShadow(m, &fakeDecisionEngine{err: errors.New("worker crashed")})
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v, want done despite a pre-verifier engine crash", m.agentLoop.run)
	}
	if m.lastDebug.DecisionShadowPreVerifierUnavailableReason == "" {
		t.Fatal("want DecisionShadowPreVerifierUnavailableReason set for a worker error")
	}
}

// TestPreVerifierStaleResultIgnored covers spec item 6, mirroring
// TestDecisionShadowStaleResultIgnored but for the pre-verifier's own,
// independent generation counter.
func TestPreVerifierStaleResultIgnored(t *testing.T) {
	m := newTestModel(t)
	m.agentLoop.run = &agent.AgentRun{
		ID: "run-stale-preverifier", Cycle: 2, Stage: agent.StageVerifier, Status: agent.DecisionRunning,
		Limits: agent.Limits{MaxCycles: 8},
	}
	m.agentLoop.preVerifierShadowGen = 2
	beforeMetrics := m.preVerifierShadowMetrics
	beforeCorrelations := len(m.preVerifierCorrelations)

	_, cmd := m.handleAgentDecisionPreVerifierShadow(agentDecisionPreVerifierShadowMsg{
		runID: "run-stale-preverifier", cycle: 2, gen: 1, // stale: current gen is 2
		result: fakePreVerifierResult(0.9, 0.9),
	})
	if cmd != nil {
		t.Fatal("stale pre-verifier result scheduled work")
	}
	if m.preVerifierShadowMetrics != beforeMetrics {
		t.Fatalf("stale pre-verifier result changed metrics: got %+v, want unchanged %+v", m.preVerifierShadowMetrics, beforeMetrics)
	}
	if len(m.preVerifierCorrelations) != beforeCorrelations {
		t.Fatal("stale pre-verifier result created a correlation entry")
	}
}

// TestPreVerifierCorrelationPredictionFirst covers spec items 7 and 10: the
// prediction half arrives before the actual-outcome half, and the finalized
// record correctly reflects a real semantic-verifier cycle.
func TestPreVerifierCorrelationPredictionFirst(t *testing.T) {
	m := newTestModel(t)
	m.recordPreVerifierPrediction(agentDecisionPreVerifierShadowMsg{
		runID: "run-pred-first", cycle: 1, result: fakePreVerifierResult(0.8, 0.3),
	})
	if m.preVerifierShadowMetrics.Available != 1 {
		t.Fatalf("Available = %d, want 1", m.preVerifierShadowMetrics.Available)
	}
	if len(m.preVerifierShadowSamples) != 0 {
		t.Fatal("finalized before the actual outcome arrived")
	}

	m.recordPreVerifierActual("run-pred-first", 1, true, agent.VerificationPassed, agent.DecisionDone)

	if len(m.preVerifierShadowSamples) != 1 {
		t.Fatalf("samples = %d, want 1 (finalized once both halves arrived)", len(m.preVerifierShadowSamples))
	}
	if m.preVerifierShadowMetrics.TruePositive != 1 {
		t.Fatalf("want a true positive (predicted needed >=0.5, verifier actually ran): metrics = %+v", m.preVerifierShadowMetrics)
	}
	if m.preVerifierShadowMetrics.ActualVerifierRuns != 1 {
		t.Fatalf("ActualVerifierRuns = %d, want 1", m.preVerifierShadowMetrics.ActualVerifierRuns)
	}
	if _, exists := m.preVerifierCorrelations[preVerifierCorrelationKey("run-pred-first", 1)]; exists {
		t.Fatal("correlation entry was not deleted after finalizing")
	}
}

// TestPreVerifierCorrelationVerifierFirst covers spec items 8 and 9: the
// actual-outcome half arrives before the prediction, and the finalized
// record correctly reflects a deterministic-only cycle (no semantic
// verifier ran).
func TestPreVerifierCorrelationVerifierFirst(t *testing.T) {
	m := newTestModel(t)
	m.recordPreVerifierActual("run-verifier-first", 1, false, agent.VerificationPassed, agent.DecisionDone)
	if len(m.preVerifierShadowSamples) != 0 {
		t.Fatal("finalized before the prediction arrived")
	}

	m.recordPreVerifierPrediction(agentDecisionPreVerifierShadowMsg{
		runID: "run-verifier-first", cycle: 1, result: fakePreVerifierResult(0.2, 0.9),
	})

	if len(m.preVerifierShadowSamples) != 1 {
		t.Fatalf("samples = %d, want 1", len(m.preVerifierShadowSamples))
	}
	if m.preVerifierShadowMetrics.TrueNegative != 1 {
		t.Fatalf("want a true negative (predicted not needed, deterministic-only cycle): metrics = %+v", m.preVerifierShadowMetrics)
	}
	if m.preVerifierShadowMetrics.ActualVerifierRuns != 0 {
		t.Fatalf("ActualVerifierRuns = %d, want 0 for a deterministic-only cycle", m.preVerifierShadowMetrics.ActualVerifierRuns)
	}
}

// TestPreVerifierFalseNegativeIsCountedSeparately proves the spec's stated
// priority safety metric — Laya says verification is unnecessary but the
// authoritative pipeline ran one anyway — is tracked distinctly from a
// correct negative.
func TestPreVerifierFalseNegativeIsCountedSeparately(t *testing.T) {
	m := newTestModel(t)
	m.recordPreVerifierPrediction(agentDecisionPreVerifierShadowMsg{
		runID: "run-false-neg", cycle: 1, result: fakePreVerifierResult(0.1, 0.9), // predicted NOT needed
	})
	m.recordPreVerifierActual("run-false-neg", 1, true, agent.VerificationFailed, agent.DecisionRetry) // but it DID run

	if m.preVerifierShadowMetrics.FalseNegative != 1 {
		t.Fatalf("want a false negative recorded: metrics = %+v", m.preVerifierShadowMetrics)
	}
	if m.preVerifierShadowMetrics.TrueNegative != 0 || m.preVerifierShadowMetrics.TruePositive != 0 || m.preVerifierShadowMetrics.FalsePositive != 0 {
		t.Fatalf("false negative miscounted into another bucket: metrics = %+v", m.preVerifierShadowMetrics)
	}
}

// TestDecisionShadowFullProbabilityMapRendersCorrectly covers spec item 12.
func TestDecisionShadowFullProbabilityMapRendersCorrectly(t *testing.T) {
	m := newTestModel(t)
	m.lastDebug.When = time.Now()
	m.lastDebug.DecisionShadowCycleAction = "finish"
	m.lastDebug.DecisionShadowCycleActionProbability = 0.34
	m.lastDebug.DecisionShadowCycleActionProbabilities = map[string]float64{
		"finish": 0.34, "semantic_verify": 0.28, "continue": 0.18, "ask_user": 0.07, "blocked": 0.13,
	}
	view := m.debugOverlay()
	for _, want := range []string{"finish=0.34", "semantic_verify=0.28", "continue=0.18", "ask_user=0.07", "blocked=0.13"} {
		if !strings.Contains(view, want) {
			t.Fatalf("debug overlay missing %q:\n%s", want, view)
		}
	}
}

// TestPreVerifierDisabledStartsNoWorker covers spec item 13: disabled means
// no dispatch at all, for the pre-verifier path exactly like the post-cycle
// one (TestDecisionShadowDisabledAgentUnchanged).
func TestPreVerifierDisabledStartsNoWorker(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	if m.decisionShadow != nil {
		t.Fatalf("decisionShadow = %+v, want nil when decision_engine.enabled is false", m.decisionShadow)
	}
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if m.lastDebug.DecisionShadowPreVerifierUnavailableReason != "" || m.lastDebug.DecisionShadowPreVerifierNeededProbability != 0 {
		t.Fatalf("pre-verifier debug fields populated with no engine wired: %+v", m.lastDebug)
	}
	if len(m.preVerifierCorrelations) != 0 || len(m.preVerifierShadowSamples) != 0 {
		t.Fatal("pre-verifier state populated with no engine wired")
	}
}

// TestPreVerifierOrdinaryChatUnaffected covers spec item 14: the pre-verifier
// dispatch is only ever reachable from inside startAgentVerification, never
// from the ordinary chat path.
func TestPreVerifierOrdinaryChatUnaffected(t *testing.T) {
	m := newTestModel(t)
	fake := wireFakeDecisionShadow(m, &fakeDecisionEngine{result: fakePreVerifierResult(0.9, 0.9)})
	m.agentOn = false

	_, cmd := m.Update(streamEventMsg{ok: false})
	_ = cmd
	if fake.predicts.Load() != 0 {
		t.Fatalf("predicts = %d, want 0: ordinary chat must never consult the pre-verifier shadow", fake.predicts.Load())
	}
}
