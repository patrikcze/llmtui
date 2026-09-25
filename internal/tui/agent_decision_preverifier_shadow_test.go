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

// TestPreVerifierStaleResultIgnored covers spec item 6: a result whose own
// dispatch was never registered (no matching correlation entry exists at
// all — here because the test never calls dispatchAgentDecisionPreVerifierShadow)
// must never be treated as a legitimate observation. Phase 0a changed the
// guard from "does this match the model's live cycle/gen" to "does this
// match a correlation entry actually registered at dispatch time," so this
// now correctly increments the Dropped accounting counter instead of
// silently leaving every counter at zero — see handleAgentDecisionPreVerifierShadow's
// doc comment and preVerifierShadowMetrics.Dropped. It must still never be
// mistaken for a real observation: no correlation entry, no confusion-matrix
// counter, and no debug field may change.
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
		runID: "run-stale-preverifier", cycle: 2, gen: 1, // never dispatched: no correlation entry was ever registered for this (runID, cycle)
		result: fakePreVerifierResult(0.9, 0.9),
	})
	if cmd != nil {
		t.Fatal("stale pre-verifier result scheduled work")
	}
	wantMetrics := beforeMetrics
	wantMetrics.Dropped++
	if m.preVerifierShadowMetrics != wantMetrics {
		t.Fatalf("unmatched pre-verifier result metrics = %+v, want only Dropped incremented from %+v", m.preVerifierShadowMetrics, beforeMetrics)
	}
	if len(m.preVerifierCorrelations) != beforeCorrelations {
		t.Fatal("unmatched pre-verifier result created a correlation entry")
	}
	if m.lastDebug.DecisionShadowPreVerifierNeededProbability != 0 || m.lastDebug.DecisionShadowPreVerifierAvailable {
		t.Fatalf("unmatched pre-verifier result wrote debug fields: %+v", m.lastDebug)
	}
}

// TestPreVerifierStaleGenerationOnSameCycleIsDropped covers the "stale
// generation" scenario distinctly from TestPreVerifierStaleResultIgnored's
// never-dispatched case: a dispatch WAS registered for this exact (runID,
// cycle), but a message carrying a different generation than the one that
// dispatch recorded must still be rejected as unmatchable — e.g. a stray
// result from some earlier, superseded dispatch for the same cycle.
func TestPreVerifierStaleGenerationOnSameCycleIsDropped(t *testing.T) {
	m := newTestModel(t)
	m.agentLoop.run = &agent.AgentRun{
		ID: "run-stale-gen", Cycle: 1, Stage: agent.StageVerifier, Status: agent.DecisionRunning,
		Limits: agent.Limits{MaxCycles: 8},
	}
	entry := m.preVerifierCorrelationEntry("run-stale-gen", 1)
	entry.dispatched = true
	entry.dispatchGen = 5
	before := m.preVerifierShadowMetrics

	_, cmd := m.handleAgentDecisionPreVerifierShadow(agentDecisionPreVerifierShadowMsg{
		runID: "run-stale-gen", cycle: 1, gen: 4, // registered dispatch used gen 5, not 4
		result: fakePreVerifierResult(0.9, 0.9),
	})
	if cmd != nil {
		t.Fatal("stale-generation result scheduled work")
	}
	want := before
	want.Dropped++
	if m.preVerifierShadowMetrics != want {
		t.Fatalf("stale-generation metrics = %+v, want only Dropped incremented from %+v", m.preVerifierShadowMetrics, before)
	}
	if entry.predictionArrived {
		t.Fatal("stale-generation result was recorded onto the registered entry")
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

	m.recordPreVerifierActual("run-pred-first", 1, true, agent.VerificationPassed, agent.DecisionDone, "semantic")

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
	m.recordPreVerifierActual("run-verifier-first", 1, false, agent.VerificationPassed, agent.DecisionDone, "deterministic")
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
	m.recordPreVerifierActual("run-false-neg", 1, true, agent.VerificationFailed, agent.DecisionRetry, "semantic") // but it DID run

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

// ============================================================================
// Phase 0a: measurement-integrity tests — see .claude/tasks/plans/laya-decision-architecture.md
// §11.0a. These prove the dispatch-time correlation registration, the
// live/late distinction, cancellation/reload censoring, duplicate-arrival
// idempotency, and deterministic bounded eviction that the spec calls for.
// ============================================================================

// dispatchedPreVerifierGen dispatches a pre-verifier shadow for run/execution
// and returns the generation the dispatch registered on the correlation
// entry for (run.ID, run.Cycle), without ever invoking the returned tea.Cmd —
// tests that need to simulate a real dispatch's registration side effect,
// then deliver a synthetic result for it later, use this instead of racing
// dispatchAgentDecisionPreVerifierShadow's async goroutine.
func dispatchedPreVerifierGen(t *testing.T, m *Model, run *agent.AgentRun, execution agent.ExecutionResult) int {
	t.Helper()
	if cmd := m.dispatchAgentDecisionPreVerifierShadow(run, execution); cmd == nil {
		t.Fatal("dispatchAgentDecisionPreVerifierShadow returned nil — decisionShadow must be wired first")
	}
	entry, ok := m.preVerifierCorrelations[preVerifierCorrelationKey(run.ID, run.Cycle)]
	if !ok || !entry.dispatched {
		t.Fatalf("dispatch did not register a correlation entry for (%s, %d)", run.ID, run.Cycle)
	}
	return entry.dispatchGen
}

// TestPreVerifierNextCycleBeforePredictionStillCorrelatesWithoutOverwritingDebug
// covers the "next-cycle-before-prediction" and "debug non-overwrite"
// scenarios together: cycle 1's prediction arrives only after cycle 2 has
// already become the model's live cycle. It must still finalize into the
// confusion matrix and samples (Late, not Dropped) but must never clobber
// the debug fields a since-more-current observation already populated.
func TestPreVerifierNextCycleBeforePredictionStillCorrelatesWithoutOverwritingDebug(t *testing.T) {
	m := newTestModel(t)
	wireFakeDecisionShadow(m, &fakeDecisionEngine{})
	run := &agent.AgentRun{ID: "run-next-cycle", Cycle: 1, Status: agent.DecisionRunning, Limits: agent.Limits{MaxCycles: 8}}
	m.agentLoop.run = run
	gen := dispatchedPreVerifierGen(t, m, run, agent.ExecutionResult{})

	// Cycle 1's actual outcome resolves while still live: a real semantic
	// verifier ran this cycle.
	m.recordPreVerifierActual(run.ID, 1, true, agent.VerificationPassed, agent.DecisionDone, "semantic")
	if len(m.preVerifierShadowSamples) != 0 {
		t.Fatal("finalized before the prediction arrived")
	}

	// The run advances to cycle 2, and cycle 2's own pre-verifier debug
	// state is recorded — this must survive cycle 1's later prediction.
	run.Cycle = 2
	m.lastDebug.DecisionShadowPreVerifierNeededProbability = 0.42
	m.lastDebug.DecisionShadowActualSemanticVerifierRan = false

	_, cmd := m.handleAgentDecisionPreVerifierShadow(agentDecisionPreVerifierShadowMsg{
		runID: run.ID, cycle: 1, gen: gen, result: fakePreVerifierResult(0.77, 0.3),
	})
	if cmd != nil {
		t.Fatal("handler scheduled work")
	}

	if len(m.preVerifierShadowSamples) != 1 {
		t.Fatalf("samples = %d, want 1 (a late-but-real observation must still finalize)", len(m.preVerifierShadowSamples))
	}
	sample := m.preVerifierShadowSamples[0]
	if sample.Cycle != 1 || sample.Availability != "late" || sample.BaselineRoute != "semantic" {
		t.Fatalf("sample = %+v, want cycle 1, availability late, route semantic", sample)
	}
	if m.preVerifierShadowMetrics.Late != 1 {
		t.Fatalf("Late = %d, want 1", m.preVerifierShadowMetrics.Late)
	}
	if m.preVerifierShadowMetrics.TruePositive != 1 {
		t.Fatalf("want a true positive (predicted needed >=0.5, verifier actually ran): metrics = %+v", m.preVerifierShadowMetrics)
	}
	if m.lastDebug.DecisionShadowPreVerifierNeededProbability != 0.42 {
		t.Fatalf("DecisionShadowPreVerifierNeededProbability = %v, want cycle 2's own 0.42 preserved, not cycle 1's 0.77",
			m.lastDebug.DecisionShadowPreVerifierNeededProbability)
	}
	if m.lastDebug.DecisionShadowActualSemanticVerifierRan {
		t.Fatal("DecisionShadowActualSemanticVerifierRan was overwritten by a stale cycle's late result")
	}
}

// TestPreVerifierTerminalArrivalAfterRunEnds covers a prediction arriving
// after the run it belongs to has ended entirely (m.agentLoop.run is nil) —
// the most extreme case of "not live." It must still finalize safely
// (no nil-pointer access) without touching debug fields.
func TestPreVerifierTerminalArrivalAfterRunEnds(t *testing.T) {
	m := newTestModel(t)
	wireFakeDecisionShadow(m, &fakeDecisionEngine{})
	run := &agent.AgentRun{ID: "run-terminal", Cycle: 1, Status: agent.DecisionRunning, Limits: agent.Limits{MaxCycles: 8}}
	m.agentLoop.run = run
	gen := dispatchedPreVerifierGen(t, m, run, agent.ExecutionResult{})
	m.recordPreVerifierActual(run.ID, 1, false, agent.VerificationPassed, agent.DecisionDone, "deterministic")

	// The run ends entirely before the slow prediction arrives.
	m.agentLoop.run = nil

	_, cmd := m.handleAgentDecisionPreVerifierShadow(agentDecisionPreVerifierShadowMsg{
		runID: run.ID, cycle: 1, gen: gen, result: fakePreVerifierResult(0.1, 0.9),
	})
	if cmd != nil {
		t.Fatal("handler scheduled work")
	}
	if len(m.preVerifierShadowSamples) != 1 {
		t.Fatalf("samples = %d, want 1", len(m.preVerifierShadowSamples))
	}
	if m.preVerifierShadowMetrics.TrueNegative != 1 {
		t.Fatalf("want a true negative: metrics = %+v", m.preVerifierShadowMetrics)
	}
	if m.preVerifierShadowMetrics.Late != 1 {
		t.Fatalf("Late = %d, want 1", m.preVerifierShadowMetrics.Late)
	}
}

// TestPreVerifierCensorPendingCorrelations covers the cancellation half of
// the "cancellation/reload" scenario at the unit level: a pending (dispatched,
// not yet both-arrived) entry is removed and counted Cancelled, and a
// subsequently arriving orphaned prediction for it is then Dropped (not
// double-counted as a second Cancelled).
func TestPreVerifierCensorPendingCorrelations(t *testing.T) {
	m := newTestModel(t)
	wireFakeDecisionShadow(m, &fakeDecisionEngine{})
	run := &agent.AgentRun{ID: "run-censor", Cycle: 1, Status: agent.DecisionRunning, Limits: agent.Limits{MaxCycles: 8}}
	m.agentLoop.run = run
	gen := dispatchedPreVerifierGen(t, m, run, agent.ExecutionResult{})

	m.censorPendingPreVerifierCorrelations(run.ID)

	if m.preVerifierShadowMetrics.Cancelled != 1 {
		t.Fatalf("Cancelled = %d, want 1", m.preVerifierShadowMetrics.Cancelled)
	}
	if len(m.preVerifierCorrelations) != 0 {
		t.Fatal("censored entry was not removed")
	}

	// The orphaned prediction shows up anyway (the goroutine wasn't killed,
	// only the correlation bookkeeping was) — it cannot be matched to
	// anything now, so it must be Dropped, not treated as a second cancel.
	_, cmd := m.handleAgentDecisionPreVerifierShadow(agentDecisionPreVerifierShadowMsg{
		runID: run.ID, cycle: 1, gen: gen, result: fakePreVerifierResult(0.5, 0.5),
	})
	if cmd != nil {
		t.Fatal("handler scheduled work")
	}
	if m.preVerifierShadowMetrics.Cancelled != 1 {
		t.Fatalf("Cancelled = %d, want still 1 (unchanged)", m.preVerifierShadowMetrics.Cancelled)
	}
	if m.preVerifierShadowMetrics.Dropped != 1 {
		t.Fatalf("Dropped = %d, want 1", m.preVerifierShadowMetrics.Dropped)
	}
}

// TestPreVerifierCancelVerifiedRunCensorsPendingCorrelation is the
// integration-level companion: cancelVerifiedRun (the real ESC/`/agent
// cancel`/shutdown path) must itself invoke the censor, since a cancelled
// cycle's handleAgentVerification resolution never runs and so
// recordPreVerifierActual will never supply the missing half.
func TestPreVerifierCancelVerifiedRunCensorsPendingCorrelation(t *testing.T) {
	m := newTestModel(t)
	wireFakeDecisionShadow(m, &fakeDecisionEngine{})
	run := &agent.AgentRun{ID: "run-cancel-integration", Cycle: 1, Status: agent.DecisionRunning, Limits: agent.Limits{MaxCycles: 8}}
	m.agentLoop.run = run
	dispatchedPreVerifierGen(t, m, run, agent.ExecutionResult{})

	m.cancelVerifiedRun("verification cancelled by the user")

	if m.preVerifierShadowMetrics.Cancelled != 1 {
		t.Fatalf("Cancelled = %d, want 1 after cancelVerifiedRun", m.preVerifierShadowMetrics.Cancelled)
	}
	if len(m.preVerifierCorrelations) != 0 {
		t.Fatal("cancelVerifiedRun left a pending correlation entry behind")
	}
}

// TestPreVerifierConfigureDecisionShadowReloadCensorsPendingCorrelations
// covers the reload half of "cancellation/reload": replacing (or disabling)
// an already-wired decision shadow service must censor every pending entry
// regardless of which run it belongs to, since a prediction still in flight
// against the just-closed service can no longer be trusted to correlate
// cleanly with whatever wiring comes next.
func TestPreVerifierConfigureDecisionShadowReloadCensorsPendingCorrelations(t *testing.T) {
	m := newTestModel(t)
	m.cfg.DecisionEngine.Enabled = true
	wireFakeDecisionShadow(m, &fakeDecisionEngine{})
	runA := &agent.AgentRun{ID: "run-reload-a", Cycle: 1, Status: agent.DecisionRunning, Limits: agent.Limits{MaxCycles: 8}}
	m.agentLoop.run = runA
	dispatchedPreVerifierGen(t, m, runA, agent.ExecutionResult{})
	runB := &agent.AgentRun{ID: "run-reload-b", Cycle: 1, Status: agent.DecisionRunning, Limits: agent.Limits{MaxCycles: 8}}
	m.agentLoop.run = runB
	dispatchedPreVerifierGen(t, m, runB, agent.ExecutionResult{})
	if len(m.preVerifierCorrelations) != 2 {
		t.Fatalf("preVerifierCorrelations = %d, want 2 pending entries before reload", len(m.preVerifierCorrelations))
	}

	m.cfg.DecisionEngine.Enabled = false
	m.configureDecisionShadow()

	if m.preVerifierShadowMetrics.Cancelled != 2 {
		t.Fatalf("Cancelled = %d, want 2 (both runs' pending entries)", m.preVerifierShadowMetrics.Cancelled)
	}
	if len(m.preVerifierCorrelations) != 0 {
		t.Fatal("reload left pending correlation entries behind")
	}
}

// TestPreVerifierDuplicatePredictionArrivalNotDoubleCounted covers the
// "duplicate" scenario: the same prediction result arriving twice (e.g. a
// defensive redundant delivery) before the actual half ever arrives must not
// double-count Total/Available, and must still finalize exactly once.
func TestPreVerifierDuplicatePredictionArrivalNotDoubleCounted(t *testing.T) {
	m := newTestModel(t)
	msg := agentDecisionPreVerifierShadowMsg{runID: "run-dup-pred", cycle: 1, result: fakePreVerifierResult(0.6, 0.4)}

	m.recordPreVerifierPrediction(msg)
	m.recordPreVerifierPrediction(msg)

	if m.preVerifierShadowMetrics.Total != 1 || m.preVerifierShadowMetrics.Available != 1 {
		t.Fatalf("metrics = %+v, want Total=1 Available=1 after a duplicate prediction arrival", m.preVerifierShadowMetrics)
	}
	if m.preVerifierShadowMetrics.Duplicate != 1 {
		t.Fatalf("Duplicate = %d, want 1", m.preVerifierShadowMetrics.Duplicate)
	}

	m.recordPreVerifierActual("run-dup-pred", 1, true, agent.VerificationPassed, agent.DecisionDone, "semantic")
	if len(m.preVerifierShadowSamples) != 1 {
		t.Fatalf("samples = %d, want exactly 1 (finalized once despite the duplicate)", len(m.preVerifierShadowSamples))
	}
}

// TestPreVerifierDuplicateActualArrivalNotDoubleCounted mirrors the
// prediction-side duplicate test for the actual-outcome half.
func TestPreVerifierDuplicateActualArrivalNotDoubleCounted(t *testing.T) {
	m := newTestModel(t)
	m.recordPreVerifierActual("run-dup-actual", 1, true, agent.VerificationPassed, agent.DecisionDone, "semantic")
	m.recordPreVerifierActual("run-dup-actual", 1, true, agent.VerificationPassed, agent.DecisionDone, "semantic")

	if m.preVerifierShadowMetrics.Duplicate != 1 {
		t.Fatalf("Duplicate = %d, want 1", m.preVerifierShadowMetrics.Duplicate)
	}

	m.recordPreVerifierPrediction(agentDecisionPreVerifierShadowMsg{
		runID: "run-dup-actual", cycle: 1, result: fakePreVerifierResult(0.6, 0.4),
	})
	if m.preVerifierShadowMetrics.ActualVerifierRuns != 1 {
		t.Fatalf("ActualVerifierRuns = %d, want 1 (counted once despite the duplicate actual)", m.preVerifierShadowMetrics.ActualVerifierRuns)
	}
	if len(m.preVerifierShadowSamples) != 1 {
		t.Fatalf("samples = %d, want exactly 1", len(m.preVerifierShadowSamples))
	}
}

// TestPreVerifierDeterministicEviction covers the "deterministic eviction"
// scenario: filling the bounded correlation map past maxPreVerifierCorrelations
// must always evict the entry with the smallest insertion sequence — the
// true oldest — never an arbitrary one picked by Go's randomized map
// iteration order. Repeated across several rounds so a single lucky pass
// through map order can't hide a regression.
func TestPreVerifierDeterministicEviction(t *testing.T) {
	m := newTestModel(t)
	var keys []string
	for i := 0; i < maxPreVerifierCorrelations; i++ {
		runID := "run-evict"
		cycle := i + 1
		m.preVerifierCorrelationEntry(runID, cycle)
		keys = append(keys, preVerifierCorrelationKey(runID, cycle))
	}
	if len(m.preVerifierCorrelations) != maxPreVerifierCorrelations {
		t.Fatalf("correlations = %d, want %d before eviction", len(m.preVerifierCorrelations), maxPreVerifierCorrelations)
	}

	for round := 0; round < 3; round++ {
		oldestKey := keys[round]
		nextRunID := "run-evict-new"
		nextCycle := 1000 + round
		m.preVerifierCorrelationEntry(nextRunID, nextCycle)

		if _, exists := m.preVerifierCorrelations[oldestKey]; exists {
			t.Fatalf("round %d: true-oldest entry %q was not evicted", round, oldestKey)
		}
		if len(m.preVerifierCorrelations) != maxPreVerifierCorrelations {
			t.Fatalf("round %d: correlations = %d, want bounded at %d", round, len(m.preVerifierCorrelations), maxPreVerifierCorrelations)
		}
		if m.preVerifierShadowMetrics.Dropped != round+1 {
			t.Fatalf("round %d: Dropped = %d, want %d", round, m.preVerifierShadowMetrics.Dropped, round+1)
		}
	}
}
