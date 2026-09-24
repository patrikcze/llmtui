package tui

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/decision"
)

// fakeDecisionEngine implements decision.Engine for tests that need a
// decision.Service without Python or MLX. The existing test doubles in
// internal/decision/*_test.go are unexported and package-scoped, so this is
// a small standalone one for package tui.
type fakeDecisionEngine struct {
	result decision.Result
	// preVerifierResult, when non-nil, is returned for a Predict call whose
	// questions match the pre-verifier set (identified by the presence of
	// "evidence_sufficient", which never appears in the post-cycle
	// question set) instead of result. nil falls back to result — which
	// will fail decision.Service's ValidateResult for the pre-verifier
	// question set (different question count/names), surfacing as a
	// recorded-but-harmless "unavailable" for that call, exactly like a
	// real malformed-answer failure would. Existing tests that don't set
	// this field are unaffected either way.
	preVerifierResult *decision.Result
	err               error
	delay             time.Duration
	predicts          atomic.Int32
	closed            atomic.Bool
}

func (e *fakeDecisionEngine) Name() string { return "fake" }

func (e *fakeDecisionEngine) Predict(ctx context.Context, state any, questions map[string]decision.Question, opts decision.PredictOptions) (decision.Result, error) {
	e.predicts.Add(1)
	if e.delay > 0 {
		select {
		case <-time.After(e.delay):
		case <-ctx.Done():
			return decision.Result{}, ctx.Err()
		}
	}
	if e.err != nil {
		return decision.Result{}, e.err
	}
	if e.preVerifierResult != nil {
		if _, ok := questions["evidence_sufficient"]; ok {
			return *e.preVerifierResult, nil
		}
	}
	return e.result, nil
}

func (e *fakeDecisionEngine) Close() error {
	e.closed.Store(true)
	return nil
}

// fakeCycleActionResult builds a valid Result answering all three shadow
// questions, so decision.Service's ValidateResult (which requires an answer
// for every asked question) accepts it.
func fakeCycleActionResult(choice string) decision.Result {
	return decision.Result{Answers: map[string]decision.Answer{
		"cycle_action":             {Type: decision.QuestionChoice, Choice: choice, Confidence: 0.9},
		"goal_complete":            {Type: decision.QuestionNoul, Probability: 0.5},
		"semantic_verifier_needed": {Type: decision.QuestionNoul, Probability: 0.5},
	}}
}

func wireFakeDecisionShadow(m *Model, engine decision.Engine) *fakeDecisionEngine {
	fake, _ := engine.(*fakeDecisionEngine)
	m.decisionShadow = &decisionShadowService{service: decision.NewService(true, engine), model: "english-mlx"}
	return fake
}

// TestDecisionShadowDisabledAgentUnchanged covers scenario 1 (disabled) and
// 12 (Python/engine never constructed when disabled): decision_engine.enabled
// is false by default in newTestModel's config, so rebuildFromConfig's
// configureDecisionShadow call must leave m.decisionShadow nil, and a normal
// agent run must complete exactly as it would with no decision package
// wiring at all.
func TestDecisionShadowDisabledAgentUnchanged(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	if m.decisionShadow != nil {
		t.Fatalf("decisionShadow = %+v, want nil when decision_engine.enabled is false", m.decisionShadow)
	}
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 1 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if m.lastDebug.DecisionShadowCycleAction != "" || m.lastDebug.DecisionShadowUnavailableReason != "" {
		t.Fatalf("debug shadow fields populated with no engine wired: %+v", m.lastDebug)
	}
}

// TestDecisionShadowUnavailableAgentUnchanged covers scenarios 2 and 8: an
// engine error (unavailable, crashed, malformed — all surface as a Predict
// error) must have zero effect on the agent run, and gets recorded as an
// unavailable reason rather than a stale-looking success.
func TestDecisionShadowUnavailableAgentUnchanged(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	fake := wireFakeDecisionShadow(m, &fakeDecisionEngine{err: errors.New("worker crashed")})
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 1 {
		t.Fatalf("run = %+v, want unaffected by decision engine error", m.agentLoop.run)
	}
	// One cycle now dispatches two shadow calls sharing this engine: the
	// pre-verifier counterfactual shadow and the post-cycle shadow.
	if fake.predicts.Load() != 2 {
		t.Fatalf("predicts = %d, want exactly 2 shadow calls (pre-verifier + post-cycle)", fake.predicts.Load())
	}
	if m.lastDebug.DecisionShadowUnavailableReason == "" {
		t.Fatal("want DecisionShadowUnavailableReason set for a worker error")
	}
	if m.lastDebug.DecisionShadowCycleAction != "" {
		t.Fatalf("DecisionShadowCycleAction = %q, want empty on error", m.lastDebug.DecisionShadowCycleAction)
	}
}

// TestDecisionShadowFinishDoesNotOverrideFailedVerifier covers scenario 3:
// Laya recommending "finish" must not affect a run the real verifier fails
// twice in a row to DecisionFailed.
func TestDecisionShadowFinishDoesNotOverrideFailedVerifier(t *testing.T) {
	next := "inspect a different deterministic edge case"
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Attempt one."},
		agentScriptStep{text: verifierJSON("failed", "same failure", next, true, true)},
		agentScriptStep{text: "Attempt two."},
		agentScriptStep{text: verifierJSON("failed", "same failure", next, true, true)},
	)
	m.cfg.Agent.MaxRepeatedFailures = 2
	wireFakeDecisionShadow(m, &fakeDecisionEngine{result: fakeCycleActionResult("finish")})
	driveAgentCommands(t, m, m.startVerifiedRun("fix repeated failure", nil))

	if m.agentLoop.run.Status != agent.DecisionFailed {
		t.Fatalf("status = %s, want %s (verifier must win over a shadow 'finish')", m.agentLoop.run.Status, agent.DecisionFailed)
	}
	if m.lastDebug.DecisionShadowCycleAction != "finish" {
		t.Fatalf("DecisionShadowCycleAction = %q, want the fake's recorded finish recommendation", m.lastDebug.DecisionShadowCycleAction)
	}
}

// TestDecisionShadowContinueDoesNotOverrideDone covers scenario 4: Laya
// recommending "continue" must not affect a run that legitimately finishes
// in one cycle.
func TestDecisionShadowContinueDoesNotOverrideDone(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	wireFakeDecisionShadow(m, &fakeDecisionEngine{result: fakeCycleActionResult("continue")})
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 1 {
		t.Fatalf("run = %+v, want done in one cycle regardless of shadow 'continue'", m.agentLoop.run)
	}
}

// TestDecisionShadowAskUserCreatesNoState covers scenario 5: Laya
// recommending "ask_user" must never itself open the question picker or set
// errText — only the authoritative DecisionNeedsUserInput path may do that.
func TestDecisionShadowAskUserCreatesNoState(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	wireFakeDecisionShadow(m, &fakeDecisionEngine{result: fakeCycleActionResult("ask_user")})
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("status = %s, want done: a shadow 'ask_user' must not change the outcome", m.agentLoop.run.Status)
	}
	if m.picker.pickerKind != pickerNone {
		t.Fatalf("picker = %v, want none: shadow 'ask_user' must not open a picker", m.picker.pickerKind)
	}
	if m.errText != "" {
		t.Fatalf("errText = %q, want empty: shadow 'ask_user' must not set an input prompt", m.errText)
	}
}

// TestDecisionShadowSemanticVerifyDoesNotForceVerification covers scenario 6:
// with deterministic-only verification configured, a cycle resolves via
// startAgentVerification's synthetic shortcut (no real model verifier
// request at all). A shadow recommendation of "semantic_verify" must not
// cause a real verifier call to happen — the script offers no second
// provider reply, so driveAgentCommands would fail with "script exhausted"
// if one were incorrectly triggered.
func TestDecisionShadowSemanticVerifyDoesNotForceVerification(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
	)
	m.cfg.Agent.Verifier.Mode = config.VerifierModeDeterministic
	wireFakeDecisionShadow(m, &fakeDecisionEngine{result: fakeCycleActionResult("semantic_verify")})
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("status = %s, want done via the deterministic shortcut", m.agentLoop.run.Status)
	}
	// contract + executor only — no verifier request was ever dispatched.
	if len(prov.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (contract + executor, no verifier call)", len(prov.requests))
	}
	if m.lastDebug.DecisionShadowActualVerifierPath != "deterministic" {
		t.Fatalf("DecisionShadowActualVerifierPath = %q, want deterministic", m.lastDebug.DecisionShadowActualVerifierPath)
	}
}

// TestDecisionShadowStaleResultIgnored covers scenario 7, mirroring
// TestStaleVerifierResultDoesNotClearCurrentActivity's direct-handler
// pattern: a decisionShadowMsg carrying an older generation than the run's
// current one must be discarded without touching any debug/metrics state.
func TestDecisionShadowStaleResultIgnored(t *testing.T) {
	m := newTestModel(t)
	m.agentLoop.run = &agent.AgentRun{
		ID: "run-stale-shadow", Cycle: 2, Stage: agent.StageStopCheck, Status: agent.DecisionRunning,
		Limits: agent.Limits{MaxCycles: 8},
	}
	m.agentLoop.decisionShadowGen = 2
	before := m.decisionShadowMetrics

	_, cmd := m.handleAgentDecisionShadow(agentDecisionShadowMsg{
		runID: "run-stale-shadow", cycle: 2, gen: 1, // stale: current gen is 2
		result: fakeCycleActionResult("finish"),
	})
	if cmd != nil {
		t.Fatal("stale shadow result scheduled work")
	}
	if m.decisionShadowMetrics != before {
		t.Fatalf("stale shadow result changed metrics: got %+v, want unchanged %+v", m.decisionShadowMetrics, before)
	}
	if m.lastDebug.DecisionShadowCycleAction != "" {
		t.Fatalf("stale shadow result populated debugInfo: %q", m.lastDebug.DecisionShadowCycleAction)
	}
}

// TestDecisionShadowContextTimeoutDoesNotBlock covers scenario 9: a shadow
// call whose engine never returns must still resolve via its own bounded
// context timeout rather than hanging, and must carry that as an error, not
// a leaked goroutine or a stuck run.
func TestDecisionShadowContextTimeoutDoesNotBlock(t *testing.T) {
	old := decisionShadowTimeout
	decisionShadowTimeout = 20 * time.Millisecond
	t.Cleanup(func() { decisionShadowTimeout = old })

	m, _ := configureAgentTestModel(t,
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
		t.Fatal("agent run did not complete promptly; shadow call appears to have blocked it")
	}
	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("status = %s, want done despite a hung shadow engine", m.agentLoop.run.Status)
	}
	if m.lastDebug.DecisionShadowUnavailableReason == "" {
		t.Fatal("want a timeout recorded as an unavailable reason")
	}
}

// TestDecisionShadowClosesOnShutdown covers scenario 10: quit()'s returned
// tea.Cmd must close the decision engine's worker alongside mcpRegistry.
func TestDecisionShadowClosesOnShutdown(t *testing.T) {
	m := newTestModel(t)
	fake := wireFakeDecisionShadow(m, &fakeDecisionEngine{})
	cmd := m.quit()
	if cmd == nil {
		t.Fatal("quit() returned a nil command")
	}
	cmd()
	if !fake.closed.Load() {
		t.Fatal("decision engine was not closed on shutdown")
	}
}

// TestDecisionShadowSameServiceAcrossCycles covers scenario 11: the wired
// decisionShadowService (and therefore the Router/worker it owns) must be
// reused across multiple cycles of the same run, never reconstructed
// per-prediction.
func TestDecisionShadowSameServiceAcrossCycles(t *testing.T) {
	next := "continue with the next bounded step"
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "First attempt completed."},
		agentScriptStep{text: verifierJSON("failed", "still incomplete", next, true, true)},
		agentScriptStep{text: "Second attempt completed."},
		agentScriptStep{text: verifierJSON("passed", "now complete", "", false, false)},
	)
	fake := wireFakeDecisionShadow(m, &fakeDecisionEngine{result: fakeCycleActionResult("continue")})
	svc := m.decisionShadow
	driveAgentCommands(t, m, m.startVerifiedRun("multi-cycle task", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 2 {
		t.Fatalf("run = %+v, want a 2-cycle run", m.agentLoop.run)
	}
	if m.decisionShadow != svc {
		t.Fatal("decisionShadow was reconstructed mid-run")
	}
	// 2 cycles x 2 shadow calls each (pre-verifier + post-cycle), all
	// sharing this one engine instance.
	if fake.predicts.Load() != 4 {
		t.Fatalf("predicts = %d, want exactly 4 shadow calls (2 cycles x pre-verifier+post-cycle)", fake.predicts.Load())
	}
}

// TestDecisionShadowOrdinaryChatUnaffected covers scenario 13: a decision
// engine wired and enabled must have no effect whatsoever on ordinary
// (non-agent) chat — dispatchAgentDecisionShadow is only ever reachable from
// inside handleAgentVerification.
func TestDecisionShadowOrdinaryChatUnaffected(t *testing.T) {
	m := newTestModel(t)
	fake := wireFakeDecisionShadow(m, &fakeDecisionEngine{result: fakeCycleActionResult("finish")})
	m.agentOn = false

	_, cmd := m.Update(streamEventMsg{ok: false})
	_ = cmd
	if fake.predicts.Load() != 0 {
		t.Fatalf("predicts = %d, want 0: ordinary chat must never consult the decision engine", fake.predicts.Load())
	}
}

// TestBuildAgentDecisionShadowStateCapsAndTruncates is a focused unit test
// for the compact-state builder's explicit size caps, independent of the
// Bubble Tea plumbing.
func TestBuildAgentDecisionShadowStateCapsAndTruncates(t *testing.T) {
	m := newTestModel(t)
	longTask := ""
	for i := 0; i < 200; i++ {
		longTask += "0123456789"
	}
	run := &agent.AgentRun{
		Request: longTask, Objective: longTask,
		Limits: agent.Limits{MaxCycles: 8},
	}
	for i := 0; i < 30; i++ {
		run.Criteria = append(run.Criteria, agent.Criterion{
			ID: "c", Kind: agent.CriterionSemantic, Status: agent.CriterionPending,
		})
	}
	execution := agent.ExecutionResult{}
	for i := 0; i < 30; i++ {
		execution.ChangedFiles = append(execution.ChangedFiles, "file.txt")
		execution.Errors = append(execution.Errors, agent.RunError{Kind: agent.ErrorTimeout})
	}

	state := m.buildAgentDecisionShadowState(run, execution)

	if len(state.Task) > decisionShadowMaxTaskBytes+len("…") {
		t.Fatalf("Task length = %d bytes, want <= %d", len(state.Task), decisionShadowMaxTaskBytes)
	}
	if len(state.Objective) > decisionShadowMaxObjectiveBytes+len("…") {
		t.Fatalf("Objective length = %d bytes, want <= %d", len(state.Objective), decisionShadowMaxObjectiveBytes)
	}
	if len(state.Criteria) != decisionShadowMaxCriteria {
		t.Fatalf("Criteria = %d, want capped at %d", len(state.Criteria), decisionShadowMaxCriteria)
	}
	if len(state.ChangedFiles) != decisionShadowMaxChangedFiles {
		t.Fatalf("ChangedFiles = %d, want capped at %d", len(state.ChangedFiles), decisionShadowMaxChangedFiles)
	}
	if len(state.DeterministicErrors) != decisionShadowMaxErrorKinds {
		t.Fatalf("DeterministicErrors = %d, want capped at %d", len(state.DeterministicErrors), decisionShadowMaxErrorKinds)
	}
}

// TestNormalizeAuthoritativeDecision covers every agent.Decision value so
// the calibration mapping table can never silently miss a new one.
func TestNormalizeAuthoritativeDecision(t *testing.T) {
	cases := []struct {
		decision agent.Decision
		want     string
	}{
		{agent.DecisionDone, "finish"},
		{agent.DecisionContinue, "continue"},
		{agent.DecisionRetry, "continue"},
		{agent.DecisionNeedsUserInput, "ask_user"},
		{agent.DecisionParked, "blocked"},
		{agent.DecisionEscalated, "blocked"},
		{agent.DecisionRunning, "other"},
		{agent.DecisionCancelled, "other"},
		{agent.DecisionFailed, "other"},
		{agent.DecisionBudgetExhausted, "other"},
		{agent.DecisionNoProgress, "other"},
		{agent.DecisionVerificationUnavailable, "other"},
	}
	for _, c := range cases {
		if got := normalizeAuthoritativeDecision(c.decision); got != c.want {
			t.Errorf("normalizeAuthoritativeDecision(%s) = %q, want %q", c.decision, got, c.want)
		}
	}
}
