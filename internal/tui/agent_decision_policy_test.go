package tui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/decision"
)

// mechanicallyCompleteExecution builds an ExecutionResult that satisfies
// agent.MechanicallyComplete: at least one succeeded tool call, no errors,
// no failed tests, a non-empty summary, no pending user input.
func mechanicallyCompleteExecution() agent.ExecutionResult {
	return agent.ExecutionResult{
		Summary:   "wrote the file",
		ToolCalls: []agent.ToolCallRecord{{Name: "write_file", Succeeded: true}},
	}
}

// twoCriteriaResolvedRun builds a run whose ContractCoverageJustified is
// unconditionally true (genuine decomposition — more than one pinned
// criterion) with every criterion already resolved, satisfying the early
// all-resolved shortcut in planAgentVerification.
func twoCriteriaResolvedRun(cycle int) *agent.AgentRun {
	return &agent.AgentRun{
		Cycle:   cycle,
		Request: "do two things",
		Criteria: []agent.Criterion{
			{ID: "c1", Kind: agent.CriterionSemantic, Status: agent.CriterionSatisfied},
			{ID: "c2", Kind: agent.CriterionSemantic, Status: agent.CriterionSatisfied},
		},
	}
}

// TestPlanAgentVerificationBranches covers every route planAgentVerification
// can produce, and specifically GuardEligible's narrower rule (§7 of the
// plan): only an adaptive-mode synthetic-PASS completion candidate is ever
// eligible, never off/deterministic/always, never a semantic route, and
// never a synthetic failure/needs-input outcome.
func TestPlanAgentVerificationBranches(t *testing.T) {
	cases := []struct {
		name         string
		run          *agent.AgentRun
		execution    agent.ExecutionResult
		mode         string
		wantRoute    agentVerificationPlanRoute
		wantEligible bool
		wantVerdict  agent.VerificationVerdict // checked only when wantRoute is Synthetic
	}{
		{
			name: "off mode synthetic pass, never eligible",
			run:  &agent.AgentRun{Cycle: 2}, mode: config.VerifierModeOff,
			wantRoute: agentVerificationPlanSynthetic, wantEligible: false, wantVerdict: agent.VerificationPassed,
		},
		{
			name: "deterministic mode synthetic, never eligible",
			run:  &agent.AgentRun{Cycle: 2}, mode: config.VerifierModeDeterministic,
			wantRoute: agentVerificationPlanSynthetic, wantEligible: false, wantVerdict: agent.VerificationPassed,
		},
		{
			name: "adaptive, no criteria, empty execution, cycle!=1: falls to semantic",
			run:  &agent.AgentRun{Cycle: 2}, execution: agent.ExecutionResult{}, mode: config.VerifierModeAdaptive,
			wantRoute: agentVerificationPlanSemantic, wantEligible: false,
		},
		{
			name: "adaptive, no criteria, cycle==1, mechanically complete: still falls to semantic",
			run:  &agent.AgentRun{Cycle: 1}, execution: mechanicallyCompleteExecution(), mode: config.VerifierModeAdaptive,
			wantRoute: agentVerificationPlanSemantic, wantEligible: false,
		},
		{
			name: "adaptive, no criteria, cycle!=1, mechanically complete: synthetic pass, eligible",
			run:  &agent.AgentRun{Cycle: 2}, execution: mechanicallyCompleteExecution(), mode: config.VerifierModeAdaptive,
			wantRoute: agentVerificationPlanSynthetic, wantEligible: true, wantVerdict: agent.VerificationPassed,
		},
		{
			name: "adaptive, conclusive deterministic failure: synthetic, never eligible",
			run:  &agent.AgentRun{Cycle: 2},
			execution: agent.ExecutionResult{ToolCalls: []agent.ToolCallRecord{
				{Name: "write_file", Succeeded: false, ErrorKind: agent.ErrorToolExecution},
			}},
			mode: config.VerifierModeAdaptive, wantRoute: agentVerificationPlanSynthetic, wantEligible: false, wantVerdict: agent.VerificationFailed,
		},
		{
			name: "adaptive, unresolved user-input criterion: synthetic inconclusive, never eligible",
			run: &agent.AgentRun{Cycle: 2, Criteria: []agent.Criterion{
				{ID: "c1", Kind: agent.CriterionUserInput, Status: agent.CriterionPending, Text: "need a path"},
			}},
			mode: config.VerifierModeAdaptive, wantRoute: agentVerificationPlanSynthetic, wantEligible: false, wantVerdict: agent.VerificationInconclusive,
		},
		{
			name: "adaptive, unresolved deterministic criterion: synthetic failed, never eligible",
			run: &agent.AgentRun{Cycle: 2, Criteria: []agent.Criterion{
				{ID: "c1", Kind: agent.CriterionFileState, Status: agent.CriterionPending},
			}},
			mode: config.VerifierModeAdaptive, wantRoute: agentVerificationPlanSynthetic, wantEligible: false, wantVerdict: agent.VerificationFailed,
		},
		{
			name: "always mode: falls to semantic (no synthetic shortcut of its own)",
			run:  &agent.AgentRun{Cycle: 2}, execution: mechanicallyCompleteExecution(), mode: config.VerifierModeAlways,
			wantRoute: agentVerificationPlanSemantic, wantEligible: false,
		},
		{
			name: "early all-resolved shortcut under adaptive: eligible",
			run:  twoCriteriaResolvedRun(2), mode: config.VerifierModeAdaptive,
			wantRoute: agentVerificationPlanSynthetic, wantEligible: true, wantVerdict: agent.VerificationPassed,
		},
		{
			name: "early all-resolved shortcut under always: fires, but NOT eligible",
			run:  twoCriteriaResolvedRun(2), mode: config.VerifierModeAlways,
			wantRoute: agentVerificationPlanSynthetic, wantEligible: false, wantVerdict: agent.VerificationPassed,
		},
		{
			name: "early all-resolved shortcut under off: fires, but NOT eligible",
			run:  twoCriteriaResolvedRun(2), mode: config.VerifierModeOff,
			wantRoute: agentVerificationPlanSynthetic, wantEligible: false, wantVerdict: agent.VerificationPassed,
		},
		{
			name: "early all-resolved shortcut under deterministic: fires, but NOT eligible",
			run:  twoCriteriaResolvedRun(2), mode: config.VerifierModeDeterministic,
			wantRoute: agentVerificationPlanSynthetic, wantEligible: false, wantVerdict: agent.VerificationPassed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := planAgentVerification(tc.run, tc.execution, tc.mode)
			if plan.Route != tc.wantRoute {
				t.Fatalf("Route = %v, want %v", plan.Route, tc.wantRoute)
			}
			if plan.GuardEligible != tc.wantEligible {
				t.Fatalf("GuardEligible = %v, want %v (plan=%+v)", plan.GuardEligible, tc.wantEligible, plan)
			}
			if tc.wantRoute == agentVerificationPlanSynthetic && plan.Result.Verdict != tc.wantVerdict {
				t.Fatalf("Result.Verdict = %v, want %v", plan.Result.Verdict, tc.wantVerdict)
			}
		})
	}
}

// TestResolveDecisionCalibrationProfile covers the profile lookup itself:
// exact-alias match only, no fuzzy fallback, and empty-map (the shipped
// state) means every alias is unresolved.
func TestResolveDecisionCalibrationProfile(t *testing.T) {
	if _, ok := resolveDecisionCalibrationProfile("english-mlx"); ok {
		t.Fatal("resolved a profile from the shipped-empty decisionCalibrationProfiles map")
	}

	old := decisionCalibrationProfiles
	decisionCalibrationProfiles = map[string]decisionCalibrationProfile{
		"english-mlx": {ModelAlias: "english-mlx", Threshold: 0.7},
	}
	t.Cleanup(func() { decisionCalibrationProfiles = old })

	if _, ok := resolveDecisionCalibrationProfile("multilingual-mlx"); ok {
		t.Fatal("resolved a profile for an alias with no exact entry")
	}
	profile, ok := resolveDecisionCalibrationProfile("english-mlx")
	if !ok || profile.Threshold != 0.7 {
		t.Fatalf("profile = %+v, ok = %v, want the injected profile", profile, ok)
	}
}

// withTestCalibrationProfile injects profile for alias for the duration of
// the test, restoring the (production-empty) map on cleanup — the only
// sanctioned way to make guarded_assist active in a test, per
// decisionCalibrationProfiles' own doc comment.
func withTestCalibrationProfile(t *testing.T, alias string, profile decisionCalibrationProfile) {
	t.Helper()
	old := decisionCalibrationProfiles
	profile.ModelAlias = alias
	decisionCalibrationProfiles = map[string]decisionCalibrationProfile{alias: profile}
	t.Cleanup(func() { decisionCalibrationProfiles = old })
}

// newGuardedAssistReadyRun builds a real *agent.AgentRun in exactly the
// state startAgentVerification hands to planAgentVerification/
// dispatchGuardedAssist: CompleteExecution and ApplyDeterministicCriteria
// already applied, no criteria pinned (so downstream Decide/ApplyStop see a
// plain mechanically-complete cycle), ready for handleAgentVerification's
// later CompleteVerification/WriteMemory/Decide/ApplyStop pipeline to
// resolve normally however the guarded decision comes out.
func newGuardedAssistReadyRun(t *testing.T, execution agent.ExecutionResult) *agent.AgentRun {
	t.Helper()
	run, err := agent.NewRun("run-guarded", "do a bounded thing", agent.Limits{
		MaxCycles: 4, MaxToolCalls: 8, MaxTokens: 4096, MaxElapsed: time.Hour, MaxRepeatedFailures: 3,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("do a bounded thing", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(execution, time.Now()); err != nil {
		t.Fatal(err)
	}
	run.ApplyDeterministicCriteria(execution, run.Cycle)
	return run
}

// configureGuardedAssistTestModel wires an adaptive-mode agent test model
// with decision_engine.mode: guarded_assist and the given fake engine —
// callers still need withTestCalibrationProfile for guarded_assist to
// actually be eligible.
func configureGuardedAssistTestModel(t *testing.T, engine decision.Engine, steps ...agentScriptStep) (*Model, *scriptedAgentProvider) {
	t.Helper()
	m, prov := configureAgentTestModel(t, steps...)
	m.cfg.Agent.Verifier.Mode = config.VerifierModeAdaptive
	m.cfg.DecisionEngine.Enabled = true
	m.cfg.DecisionEngine.Mode = config.DecisionEngineModeGuardedAssist
	wireFakeDecisionShadow(m, engine)
	return m, prov
}

// dispatchGuardedAssistForTest wires run onto m.agentLoop and calls
// dispatchGuardedAssist with a fresh verify generation, mirroring exactly
// what startAgentVerification does immediately before calling it.
func dispatchGuardedAssistForTest(t *testing.T, m *Model, run *agent.AgentRun, execution agent.ExecutionResult, fallback agent.VerificationResult) tea.Cmd {
	t.Helper()
	m.agentLoop.run = run
	m.agentLoop.execution = execution
	m.agentLoop.verifying = true
	m.agentLoop.verifyGen++
	gen := m.agentLoop.verifyGen
	cmd := m.dispatchGuardedAssist(run, execution, fallback, run.ID, run.Cycle, gen)
	if cmd == nil {
		t.Fatal("dispatchGuardedAssist returned nil")
	}
	return cmd
}

// TestGuardedAssistWithNoProfileNeverDispatches is the central Definition-
// of-Done proof for Phase 1: with decision_engine.mode set to
// guarded_assist but NO calibration profile resolved — the shipped state,
// decisionCalibrationProfiles is empty in this codebase — dispatchGuardedAssist
// must return nil for an otherwise fully eligible cycle, deferring entirely
// to the caller's ordinary synthetic-result fallback.
func TestGuardedAssistWithNoProfileNeverDispatches(t *testing.T) {
	// Deliberately NOT calling withTestCalibrationProfile: decisionCalibrationProfiles
	// stays at its real, shipped, empty value for this test.
	m, _ := configureGuardedAssistTestModel(t, &fakeDecisionEngine{})
	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	run.Cycle = 2 // MechanicallyComplete eligibility excludes cycle 1.
	m.agentLoop.run = run

	cmd := m.dispatchGuardedAssist(run, mechanicallyCompleteExecution(), agent.VerificationResult{Verdict: agent.VerificationPassed}, run.ID, run.Cycle, 1)
	if cmd != nil {
		t.Fatal("dispatchGuardedAssist returned a command with no calibration profile resolved")
	}
}

// TestGuardedAssistDisabledEngineNeverDispatches covers the Definition-of-
// Done invariant directly: decision_engine.enabled=false must mean
// dispatchGuardedAssist never even reaches the profile lookup, regardless
// of Mode or an injected profile.
func TestGuardedAssistDisabledEngineNeverDispatches(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.1, MaxWait: time.Second})
	m, _ := configureAgentTestModel(t)
	m.cfg.Agent.Verifier.Mode = config.VerifierModeAdaptive
	m.cfg.DecisionEngine.Enabled = false
	m.cfg.DecisionEngine.Mode = config.DecisionEngineModeGuardedAssist
	if m.decisionShadow != nil {
		t.Fatalf("decisionShadow = %+v, want nil when decision_engine.enabled is false", m.decisionShadow)
	}

	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	m.agentLoop.run = run
	cmd := m.dispatchGuardedAssist(run, mechanicallyCompleteExecution(), agent.VerificationResult{Verdict: agent.VerificationPassed}, run.ID, run.Cycle, 1)
	if cmd != nil {
		t.Fatal("dispatchGuardedAssist returned a command with the decision engine disabled")
	}
}

// TestGuardedAssistShadowModeNeverDispatches covers mode=shadow (the
// default) with a valid profile and a wired engine: dispatchGuardedAssist
// must still return nil, since guarded_assist requires the mode itself to
// be selected, not just Enabled+profile.
func TestGuardedAssistShadowModeNeverDispatches(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.1, MaxWait: time.Second})
	m := newTestModel(t)
	m.cfg.DecisionEngine.Enabled = true
	m.cfg.DecisionEngine.Mode = config.DecisionEngineModeShadow
	fake := wireFakeDecisionShadow(m, &fakeDecisionEngine{})
	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	m.agentLoop.run = run
	cmd := m.dispatchGuardedAssist(run, mechanicallyCompleteExecution(), agent.VerificationResult{Verdict: agent.VerificationPassed}, run.ID, run.Cycle, 1)
	if cmd != nil {
		t.Fatal("dispatchGuardedAssist returned a command while mode=shadow")
	}
	if fake.predicts.Load() != 0 {
		t.Fatalf("predicts = %d, want 0", fake.predicts.Load())
	}
}

// TestGuardedAssistNoRemainingBudgetNeverDispatches covers the "active
// deadline cannot exceed remaining run elapsed budget" invariant at its own
// boundary: a run whose elapsed budget is already exhausted must never get
// a guarded wait — the existing budget stop rules own that case.
func TestGuardedAssistNoRemainingBudgetNeverDispatches(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.1, MaxWait: time.Minute})
	m := newTestModel(t)
	m.cfg.DecisionEngine.Enabled = true
	m.cfg.DecisionEngine.Mode = config.DecisionEngineModeGuardedAssist
	wireFakeDecisionShadow(m, &fakeDecisionEngine{})
	run := &agent.AgentRun{
		ID: "run-no-budget", Cycle: 2, Status: agent.DecisionRunning,
		Limits: agent.Limits{MaxCycles: 8, MaxElapsed: time.Millisecond},
		// CreatedAt far enough in the past that MaxElapsed has already
		// passed — remaining budget is negative.
		CreatedAt: time.Now().Add(-time.Hour),
	}
	m.agentLoop.run = run
	cmd := m.dispatchGuardedAssist(run, mechanicallyCompleteExecution(), agent.VerificationResult{Verdict: agent.VerificationPassed}, run.ID, run.Cycle, 1)
	if cmd != nil {
		t.Fatal("dispatchGuardedAssist returned a command with no remaining run budget")
	}
}

// TestGuardedAssistEscalatesAboveThreshold covers the core positive path:
// a probability at or above the frozen threshold forces the existing
// semantic verifier, which is what actually determines the cycle's
// outcome — not Laya's own answer.
func TestGuardedAssistEscalatesAboveThreshold(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.5, MaxWait: 2 * time.Second})
	preVerifier := fakePreVerifierResult(0.9, 0.9)
	m, prov := configureGuardedAssistTestModel(t,
		&fakeDecisionEngine{preVerifierResult: &preVerifier},
		agentScriptStep{text: verifierJSON("passed", "escalated verifier passed it", "", false, false)},
	)
	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	cmd := dispatchGuardedAssistForTest(t, m, run, mechanicallyCompleteExecution(), agent.VerificationResult{Verdict: agent.VerificationPassed, Summary: "fallback, should not be used"})
	driveAgentCommands(t, m, cmd)

	if run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v, want done via the escalated verifier", run)
	}
	if !m.lastDebug.DecisionGuardedAssistEligible || !m.lastDebug.DecisionGuardedAssistEscalated {
		t.Fatalf("lastDebug = %+v, want eligible+escalated", m.lastDebug)
	}
	if m.lastDebug.DecisionGuardedAssistReason != string(guardedAssistReasonEscalated) {
		t.Fatalf("reason = %q, want %q", m.lastDebug.DecisionGuardedAssistReason, guardedAssistReasonEscalated)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("provider requests = %d, want exactly 1 (the escalated verifier call)", len(prov.requests))
	}
}

// TestGuardedAssistDoesNotEscalateBelowThreshold covers the negative path:
// a probability below threshold must fall back to the exact original
// synthetic result — no verifier request at all.
func TestGuardedAssistDoesNotEscalateBelowThreshold(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.9, MaxWait: 2 * time.Second})
	preVerifier := fakePreVerifierResult(0.1, 0.9)
	m, prov := configureGuardedAssistTestModel(t, &fakeDecisionEngine{preVerifierResult: &preVerifier})
	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	fallback := agent.VerificationResult{Verdict: agent.VerificationPassed, Summary: "original synthetic result"}
	cmd := dispatchGuardedAssistForTest(t, m, run, mechanicallyCompleteExecution(), fallback)
	driveAgentCommands(t, m, cmd)

	if run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v, want done via the original synthetic result", run)
	}
	if !m.lastDebug.DecisionGuardedAssistEligible || m.lastDebug.DecisionGuardedAssistEscalated {
		t.Fatalf("lastDebug = %+v, want eligible, not escalated", m.lastDebug)
	}
	if len(prov.requests) != 0 {
		t.Fatalf("provider requests = %d, want 0 (no verifier call)", len(prov.requests))
	}
	if run.Cycles[0].Verification == nil || run.Cycles[0].Verification.Summary != "original synthetic result" {
		t.Fatalf("cycle verification = %+v, want the original fallback summary preserved", run.Cycles[0].Verification)
	}
}

// TestGuardedAssistUnavailableFallsBackToSynthetic covers a Predict error
// (worker crash) during the guarded wait: the cycle must still complete via
// its original synthetic result, exactly like a shadow-only failure would
// leave the run unaffected.
func TestGuardedAssistUnavailableFallsBackToSynthetic(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.1, MaxWait: 2 * time.Second})
	m, prov := configureGuardedAssistTestModel(t, &fakeDecisionEngine{err: errors.New("worker crashed")})
	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	fallback := agent.VerificationResult{Verdict: agent.VerificationPassed}
	cmd := dispatchGuardedAssistForTest(t, m, run, mechanicallyCompleteExecution(), fallback)
	driveAgentCommands(t, m, cmd)

	if run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v, want done despite the guarded predict failing", run)
	}
	if m.lastDebug.DecisionGuardedAssistEscalated {
		t.Fatal("an unavailable guarded predict must never escalate")
	}
	if m.lastDebug.DecisionGuardedAssistReason != string(guardedAssistReasonUnavailable) {
		t.Fatalf("reason = %q, want %q", m.lastDebug.DecisionGuardedAssistReason, guardedAssistReasonUnavailable)
	}
	if len(prov.requests) != 0 {
		t.Fatalf("provider requests = %d, want 0", len(prov.requests))
	}
}

// TestGuardedAssistTimeoutFallsBackToSynthetic covers a guarded predict
// that never returns within its bounded deadline: the run must proceed via
// the synthetic result instead of hanging.
func TestGuardedAssistTimeoutFallsBackToSynthetic(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.1, MaxWait: 20 * time.Millisecond})
	m, prov := configureGuardedAssistTestModel(t, &fakeDecisionEngine{delay: time.Second})
	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	fallback := agent.VerificationResult{Verdict: agent.VerificationPassed}
	cmd := dispatchGuardedAssistForTest(t, m, run, mechanicallyCompleteExecution(), fallback)

	done := make(chan struct{})
	go func() {
		driveAgentCommands(t, m, cmd)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent run did not complete promptly; guarded assist appears to have blocked on the hung engine")
	}
	if run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v, want done despite the guarded predict timing out", run)
	}
	if m.lastDebug.DecisionGuardedAssistEscalated {
		t.Fatal("a timed-out guarded predict must never escalate")
	}
	if len(prov.requests) != 0 {
		t.Fatalf("provider requests = %d, want 0", len(prov.requests))
	}
}

// TestGuardedAssistFeedsPreVerifierCorrelationExactlyOnce covers "reuse the
// one pre-verifier prediction for its diagnostic correlation; do not
// double-call shadow plus assist for the same stage": a guarded dispatch
// must make exactly one Laya Predict call for the pre-verifier question
// set (never a second, separately-dispatched pre-verifier shadow call for
// the same cycle), and that one call's result must still finalize into the
// ordinary pre-verifier shadow calibration accounting. The ordinary
// post-cycle shadow (a distinct, pre-existing mechanism from ADR 0010,
// asking a different cycle_action question set at the end of
// handleAgentVerification regardless of how the cycle resolved) still
// fires as it always has — that is not the double-dispatch this test
// guards against, so predicts.Load() is 2 (guarded pre-verifier +
// post-cycle shadow), not 1.
func TestGuardedAssistFeedsPreVerifierCorrelationExactlyOnce(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.9, MaxWait: 2 * time.Second})
	preVerifier := fakePreVerifierResult(0.2, 0.9)
	fake := &fakeDecisionEngine{preVerifierResult: &preVerifier}
	m, _ := configureGuardedAssistTestModel(t, fake)
	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	fallback := agent.VerificationResult{Verdict: agent.VerificationPassed}
	cmd := dispatchGuardedAssistForTest(t, m, run, mechanicallyCompleteExecution(), fallback)
	driveAgentCommands(t, m, cmd)

	if fake.predicts.Load() != 2 {
		t.Fatalf("predicts = %d, want exactly 2 (guarded pre-verifier call + the unrelated post-cycle shadow)", fake.predicts.Load())
	}
	if m.preVerifierShadowMetrics.Total != 1 || m.preVerifierShadowMetrics.Available != 1 {
		t.Fatalf("preVerifierShadowMetrics = %+v, want the guarded predict counted once", m.preVerifierShadowMetrics)
	}
}

// TestGuardedAssistCancellationNeverResolvesStaleCycle covers cancellation
// mid-wait: cancelVerifiedRun must invalidate the pending guarded decision
// so a result that arrives afterward can never resolve (escalate or
// complete) a cycle the cancellation already settled.
func TestGuardedAssistCancellationNeverResolvesStaleCycle(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.1, MaxWait: 5 * time.Second})
	m := newTestModel(t)
	m.cfg.Agent.Verifier.Mode = config.VerifierModeAdaptive
	m.cfg.DecisionEngine.Enabled = true
	m.cfg.DecisionEngine.Mode = config.DecisionEngineModeGuardedAssist
	wireFakeDecisionShadow(m, &fakeDecisionEngine{})

	run := newGuardedAssistReadyRun(t, mechanicallyCompleteExecution())
	m.agentLoop.run = run
	cmd := m.dispatchGuardedAssist(run, mechanicallyCompleteExecution(), agent.VerificationResult{Verdict: agent.VerificationPassed}, run.ID, run.Cycle, 1)
	if cmd == nil {
		t.Fatal("dispatchGuardedAssist returned nil with a valid profile/engine/budget")
	}
	if m.agentLoop.pendingVerificationPlan == nil {
		t.Fatal("pendingVerificationPlan not set after dispatch")
	}
	guardGenAtDispatch := m.agentLoop.guardedAssistGen

	m.cancelVerifiedRun("test cancellation")
	if m.agentLoop.pendingVerificationPlan != nil {
		t.Fatal("pendingVerificationPlan survived cancelVerifiedRun")
	}
	if m.agentLoop.guardedAssistGen == guardGenAtDispatch {
		t.Fatal("guardedAssistGen was not bumped by cancelVerifiedRun")
	}

	// The in-flight predict "arrives late" after cancellation.
	_, resultCmd := m.handleAgentDecisionGuardedAssist(agentDecisionGuardedAssistMsg{
		runID: run.ID, cycle: run.Cycle, verifyGen: m.agentLoop.verifyGen, guardGen: guardGenAtDispatch, preVerifierGen: 1,
		profile: decisionCalibrationProfile{Threshold: 0.1}, fallback: agent.VerificationResult{Verdict: agent.VerificationPassed},
		result: fakePreVerifierResult(0.99, 0.99),
	})
	if resultCmd != nil {
		t.Fatal("a stale guarded-assist result scheduled work after cancellation")
	}
	if m.lastDebug.DecisionGuardedAssistEligible {
		t.Fatal("a stale guarded-assist result was still treated as authoritative")
	}
}

// TestGuardedAssistNeverDispatchedForSemanticRoute is a structural proof of
// "no would-run -> skip transition": with a valid profile and an engine
// that would eagerly escalate, a cycle whose planAgentVerification route is
// Semantic (here: a run's very first cycle, which always dispatches
// semantically in adaptive mode regardless of mechanical completeness)
// must never touch guarded assist at all — startAgentVerification only
// calls dispatchGuardedAssist when plan.GuardEligible is true, which
// planAgentVerification never sets for a Semantic route.
func TestGuardedAssistNeverDispatchedForSemanticRoute(t *testing.T) {
	withTestCalibrationProfile(t, "english-mlx", decisionCalibrationProfile{Threshold: 0.0, MaxWait: 5 * time.Second})
	preVerifier := fakePreVerifierResult(0.99, 0.99)
	m, prov := configureGuardedAssistTestModel(t,
		&fakeDecisionEngine{preVerifierResult: &preVerifier},
		agentScriptStep{text: "First attempt completed."},
		agentScriptStep{text: verifierJSON("passed", "verified normally", "", false, false)},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("do a bounded thing", nil))

	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if m.lastDebug.DecisionGuardedAssistEligible {
		t.Fatal("a semantic-route cycle (first cycle, adaptive) was incorrectly treated as guard-eligible")
	}
	// contract + executor + verifier — no guarded predict changed this.
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(prov.requests))
	}
}
