package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/decision"
)

// This file wires the optional Laya decision engine (internal/decision) into
// the bounded agent loop as a SHADOW-ONLY observer: after agent.Decide()
// resolves each cycle, a compact bounded snapshot is sent to Laya and its
// prediction is recorded for /debug last and calibration counters. Nothing
// here may influence run.Status, stop.Decision, the verifier, tool approval,
// or acceptance criteria — see docs/decision-engine.md's "Shadow advisor"
// section and docs/architecture/decisions/0010-laya-shadow-advisor.md.

// decisionShadowTimeout bounds the independent context used for every shadow
// Predict call. Deliberately not derived from m.agentContext(): endAgentRun()
// (skills.go) cancels the run's context synchronously inside
// handleAgentVerification, before any tea.Cmd batched in that same Update
// call ever runs, so a terminal-decision cycle's shadow call would start
// with an already-cancelling context if it reused that context. A var, not a
// const, solely so tests can shrink it instead of sleeping multiple seconds.
var decisionShadowTimeout = 5 * time.Second

// decisionShadowService bundles a constructed decision.Service with the
// Router it owns so Close() can be called once, from config reload or
// shutdown. A nil *decisionShadowService (or nil m.decisionShadow) is the
// disabled/unavailable state; every exported method here is nil-receiver
// safe, mirroring decision.Service.Close's own nil-safety.
type decisionShadowService struct {
	service *decision.Service
	// model is decision_engine.laya.default_model at construction time, kept
	// for shadow-result diagnostics without re-reading config on every call.
	model string
}

// newDecisionShadowService builds the long-lived Service/Router/loader chain,
// mirroring internal/cli/decision.go's newDecisionManager and
// internal/cli/decision_runtime.go's construction exactly. It never launches
// Python or loads a model itself — Router only spawns a worker lazily on the
// first Predict for a given model alias (see decision.Router.acquire).
func newDecisionShadowService(cfg *config.Config) (*decisionShadowService, error) {
	laya := cfg.DecisionEngine.Laya
	manager, err := decision.NewModelManager(decision.ModelManagerOptions{RootDir: laya.ModelDir})
	if err != nil {
		return nil, fmt.Errorf("decision engine: %w", err)
	}
	router, err := decision.NewRouter(decision.RouterOptions{
		Store:        manager,
		Loader:       decision.MLXRuntimeLoader{Python: laya.MLXPython},
		DefaultModel: laya.DefaultModel,
		MaxLoaded:    laya.MaxLoaded,
	})
	if err != nil {
		return nil, fmt.Errorf("decision engine: %w", err)
	}
	return &decisionShadowService{
		service: decision.NewService(true, router),
		model:   laya.DefaultModel,
	}, nil
}

// Close is nil-receiver safe and idempotent (decision.Service.Close and
// decision.Router.Close both are). Router.Close never blocks on an in-flight
// prediction — a busy worker is marked for lazy close by the releasing
// Predict call instead — so this is safe to call synchronously from the
// Update() goroutine during /config reload or shutdown.
func (d *decisionShadowService) Close() error {
	if d == nil || d.service == nil {
		return nil
	}
	return d.service.Close()
}

// configureDecisionShadow rebuilds the Laya shadow wiring from config,
// called from rebuildFromConfig right after configureAgentLoop. Unlike
// mcpRegistry/personalApps (which are silently dropped and rebuilt on
// reload, an accepted tradeoff documented at their call sites), the old
// service here is explicitly closed before being replaced: an un-Closed
// Router leaks its MLX subprocess indefinitely, and Router.Close is verified
// non-blocking on any in-flight prediction (see Close's doc comment above),
// so there is no Update()-blocking risk in doing this synchronously.
func (m *Model) configureDecisionShadow() {
	old := m.decisionShadow
	m.decisionShadow = nil
	if !m.cfg.DecisionEngine.Enabled {
		if old != nil {
			_ = old.Close()
		}
		return
	}
	svc, err := newDecisionShadowService(m.cfg)
	if err != nil {
		m.errText = err.Error()
		if old != nil {
			_ = old.Close()
		}
		return
	}
	m.decisionShadow = svc
	if old != nil {
		_ = old.Close()
	}
}

// Explicit size caps for agentDecisionShadowState — never send the full
// conversation, raw reasoning, full tool output, or full entity bodies.
// Task/Objective caps are bytes (truncateAgentText truncates by byte length,
// same convention as maxAgentDirectiveBytes above it in agent_loop.go).
const (
	decisionShadowMaxTaskBytes      = 800
	decisionShadowMaxObjectiveBytes = 400
	decisionShadowMaxCriteria       = 20
	decisionShadowMaxToolNames      = 20
	decisionShadowMaxChangedFiles   = 20
	decisionShadowMaxErrorKinds     = 10
)

// agentDecisionShadowCriterion is the bounded per-criterion projection sent
// to Laya: identity and status only, never the criterion's free-text body.
type agentDecisionShadowCriterion struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// agentDecisionShadowState is the compact, bounded snapshot handed to Laya.
// Every field is derived from controller-owned data already computed by the
// time handleAgentVerification reaches its hook point — nothing here is
// re-derived from the conversation, raw reasoning, or tool output bodies.
type agentDecisionShadowState struct {
	Task                       string                         `json:"task"`
	Objective                  string                         `json:"objective"`
	Cycle                      int                            `json:"cycle"`
	MaxCycles                  int                            `json:"max_cycles"`
	Criteria                   []agentDecisionShadowCriterion `json:"criteria,omitempty"`
	SucceededTools             []string                       `json:"succeeded_tools,omitempty"`
	FailedTools                []string                       `json:"failed_tools,omitempty"`
	PermissionDenied           bool                           `json:"permission_denied"`
	NeedsUserInput             bool                           `json:"needs_user_input"`
	ChangedFiles               []string                       `json:"changed_files,omitempty"`
	TestsPassed                int                            `json:"tests_passed"`
	TestsFailed                int                            `json:"tests_failed"`
	DeterministicErrors        []string                       `json:"deterministic_errors,omitempty"`
	NewEvidence                bool                           `json:"new_evidence"`
	UnresolvedCriteria         int                            `json:"unresolved_criteria"`
	UnresolvedSemanticCriteria int                            `json:"unresolved_semantic_criteria"`
	ContractCoverageJustified  bool                           `json:"contract_coverage_justified"`
	Capabilities               []string                       `json:"capabilities,omitempty"`
}

// buildAgentDecisionShadowState derives the bounded snapshot from run and
// the cycle's ExecutionResult, both already fully populated by the time
// handleAgentVerification reaches its hook (after CompleteExecution,
// ApplyDeterministicCriteria, CompleteVerification, and Decide have all run).
func (m *Model) buildAgentDecisionShadowState(run *agent.AgentRun, execution agent.ExecutionResult) agentDecisionShadowState {
	state := agentDecisionShadowState{
		Task:                       truncateAgentText(run.Request, decisionShadowMaxTaskBytes),
		Objective:                  truncateAgentText(run.Objective, decisionShadowMaxObjectiveBytes),
		Cycle:                      run.Cycle,
		MaxCycles:                  run.Limits.MaxCycles,
		NeedsUserInput:             execution.NeedsUserInput,
		TestsPassed:                0,
		TestsFailed:                0,
		NewEvidence:                execution.NewEvidence,
		UnresolvedCriteria:         len(run.UnresolvedCriteria()),
		UnresolvedSemanticCriteria: len(run.UnresolvedSemanticCriteria()),
		ContractCoverageJustified:  run.ContractCoverageJustified(),
		Capabilities:               m.agentContextSources(),
	}

	for _, criterion := range run.Criteria {
		if len(state.Criteria) >= decisionShadowMaxCriteria {
			break
		}
		state.Criteria = append(state.Criteria, agentDecisionShadowCriterion{
			ID: criterion.ID, Kind: string(criterion.Kind), Status: string(criterion.Status),
		})
	}

	seenSucceeded := make(map[string]bool)
	seenFailed := make(map[string]bool)
	for _, call := range execution.ToolCalls {
		if call.Status == agent.ActionDenied {
			state.PermissionDenied = true
		}
		if call.Succeeded {
			if !seenSucceeded[call.Name] && len(state.SucceededTools) < decisionShadowMaxToolNames {
				seenSucceeded[call.Name] = true
				state.SucceededTools = append(state.SucceededTools, call.Name)
			}
			continue
		}
		if !seenFailed[call.Name] && len(state.FailedTools) < decisionShadowMaxToolNames {
			seenFailed[call.Name] = true
			state.FailedTools = append(state.FailedTools, call.Name)
		}
	}

	for i, file := range execution.ChangedFiles {
		if i >= decisionShadowMaxChangedFiles {
			break
		}
		state.ChangedFiles = append(state.ChangedFiles, file)
	}

	for _, test := range execution.TestsRun {
		if test.Passed {
			state.TestsPassed++
		} else {
			state.TestsFailed++
		}
	}

	for i, runErr := range execution.Errors {
		if i >= decisionShadowMaxErrorKinds {
			break
		}
		state.DeterministicErrors = append(state.DeterministicErrors, string(runErr.Kind))
		if runErr.Kind == agent.ErrorPermissionDenied {
			state.PermissionDenied = true
		}
	}

	return state
}

// decisionShadowQuestions is the fixed, small first question set: one choice
// question for the controller's next action, and two boolean (noul)
// questions calibrating goal completion and verifier necessity. See
// docs/decision-engine.md for the noul/choice answer contract.
var decisionShadowQuestions = map[string]decision.Question{
	"cycle_action": {
		Type:         decision.QuestionChoice,
		Instructions: "What should the controller do next based only on the supplied observable state?",
		Criteria: map[string]string{
			"finish":          "All pinned acceptance criteria appear satisfied by observed evidence.",
			"semantic_verify": "Evidence is incomplete or ambiguous and a semantic verifier should inspect the cycle.",
			"continue":        "More bounded executor work is required and can continue without additional user input.",
			"ask_user":        "Essential information or explicit user permission is required.",
			"blocked":         "Progress cannot currently continue using the available evidence or capabilities.",
		},
	},
	"goal_complete": {
		Type:         decision.QuestionNoul,
		Instructions: "Are all pinned acceptance criteria satisfied by the supplied observable evidence?",
	},
	"semantic_verifier_needed": {
		Type:         decision.QuestionNoul,
		Instructions: "Is semantic verification needed because deterministic evidence is insufficient to establish completion or failure?",
	},
}

// agentDecisionShadowMsg carries a shadow Predict result back into Update().
// It copies agentVerificationMsg's stale-message shape exactly (agent_loop.go
// agentVerificationMsg), with its own dedicated generation counter so a
// stale shadow response can never be confused with verifier/contract
// staleness or vice versa.
type agentDecisionShadowMsg struct {
	runID          string
	cycle          int
	gen            int
	model          string
	verifierPath   string // "deterministic" | "semantic", captured by the caller before dispatch
	actualDecision agent.Decision
	actualVerdict  agent.VerificationVerdict
	result         decision.Result
	err            error
	elapsed        time.Duration
}

// dispatchAgentDecisionShadow returns a tea.Cmd that makes one Predict call
// for the current cycle and never blocks Update(). It returns nil (a safe
// no-op tea.Cmd) whenever the engine isn't wired — decision_engine.enabled
// false means m.decisionShadow is always nil (see configureDecisionShadow),
// so this path never launches Python or loads a model.
func (m *Model) dispatchAgentDecisionShadow(run *agent.AgentRun, execution agent.ExecutionResult, verifierPath string, actualDecision agent.Decision, actualVerdict agent.VerificationVerdict) tea.Cmd {
	if m.decisionShadow == nil || m.decisionShadow.service == nil || m.agentLoop == nil {
		return nil
	}
	m.agentLoop.decisionShadowGen++
	gen := m.agentLoop.decisionShadowGen
	runID, cycle := run.ID, run.Cycle
	state := m.buildAgentDecisionShadowState(run, execution)
	svc := m.decisionShadow.service
	model := m.decisionShadow.model
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), decisionShadowTimeout)
		defer cancel()
		start := time.Now()
		result, err := svc.Predict(ctx, state, decisionShadowQuestions, decision.PredictOptions{Model: model})
		return agentDecisionShadowMsg{
			runID: runID, cycle: cycle, gen: gen, model: model,
			verifierPath: verifierPath, actualDecision: actualDecision, actualVerdict: actualVerdict,
			result: result, err: err, elapsed: time.Since(start),
		}
	}
}

// handleAgentDecisionShadow applies the stale-message guard, then only ever
// writes to m.lastDebug.DecisionShadow* and m.decisionShadowMetrics — it
// must never touch run, execution, stop, or any authoritative field. A
// decision-runtime error (unavailable, malformed, timed out, worker crash)
// simply records an unavailable reason and returns; it has zero effect on
// the agent run either way, satisfying the shadow-only contract by
// construction (this handler never calls anything but recording helpers).
func (m *Model) handleAgentDecisionShadow(msg agentDecisionShadowMsg) (tea.Model, tea.Cmd) {
	if m.agentLoop == nil || m.agentLoop.run == nil ||
		msg.runID != m.agentLoop.run.ID || msg.cycle != m.agentLoop.run.Cycle ||
		msg.gen != m.agentLoop.decisionShadowGen {
		return m, nil
	}
	m.recordAgentDecisionShadowResult(msg)
	return m, nil
}

// normalizeAuthoritativeDecision maps agent.Decision onto the same coarse
// vocabulary as cycle_action, for calibration comparison only. This mapping
// is documented, not exact: DecisionRetry and DecisionContinue both mean
// "the controller is not finished," which cycle_action does not distinguish,
// and DecisionParked/DecisionEscalated both become "blocked".
func normalizeAuthoritativeDecision(d agent.Decision) string {
	switch d {
	case agent.DecisionDone:
		return "finish"
	case agent.DecisionContinue, agent.DecisionRetry:
		return "continue"
	case agent.DecisionNeedsUserInput:
		return "ask_user"
	case agent.DecisionParked, agent.DecisionEscalated:
		return "blocked"
	default:
		return "other"
	}
}

// agentDecisionShadowMetrics is a small, session-scoped, in-memory rolling
// counter set — not persisted, not reset by /config reload. It complements
// the per-cycle debugInfo snapshot for a first-glance calibration read; the
// authoritative comparison is expected to happen in tests/evaluation
// tooling (see docs/agent-evaluation.md), not here. FalseFinishCount is the
// spec's priority metric: Laya recommending "finish" when the authoritative
// controller did not finish is worse than an ordinary disagreement.
type agentDecisionShadowMetrics struct {
	Total                   int
	Available               int
	CycleActionAgreement    int
	FalseFinishCount        int
	AskUserAgreement        int
	VerifierNeededAgreement int
	VerifierNeededFalseNeg  int
	latencySum              time.Duration
	latencyCount            int
}

// recordAgentDecisionShadowResult updates debugInfo and the rolling metrics
// from one shadow Predict outcome. It is the only place shadow results are
// written, and it never reads or writes anything on Model besides
// lastDebug/decisionShadowMetrics.
func (m *Model) recordAgentDecisionShadowResult(msg agentDecisionShadowMsg) {
	m.decisionShadowMetrics.Total++
	m.lastDebug.DecisionShadowModel = msg.model
	m.lastDebug.DecisionShadowActualVerifierPath = msg.verifierPath
	authoritative := normalizeAuthoritativeDecision(msg.actualDecision)
	m.lastDebug.DecisionShadowActualDecision = authoritative

	if msg.err != nil {
		m.lastDebug.DecisionShadowUnavailableReason = msg.err.Error()
		m.lastDebug.DecisionShadowCycleAction = ""
		m.lastDebug.DecisionShadowCycleActionConfidence = 0
		m.lastDebug.DecisionShadowCycleActionProbability = 0
		m.lastDebug.DecisionShadowCycleActionProbabilities = nil
		m.lastDebug.DecisionShadowGoalCompleteProbability = 0
		m.lastDebug.DecisionShadowVerifierNeededProbability = 0
		m.lastDebug.DecisionShadowLatency = msg.elapsed
		return
	}

	m.decisionShadowMetrics.Available++
	m.decisionShadowMetrics.latencySum += msg.elapsed
	m.decisionShadowMetrics.latencyCount++

	m.lastDebug.DecisionShadowUnavailableReason = ""
	m.lastDebug.DecisionShadowLatency = msg.elapsed

	action := msg.result.Answers["cycle_action"]
	m.lastDebug.DecisionShadowCycleAction = action.Choice
	m.lastDebug.DecisionShadowCycleActionConfidence = action.Confidence
	m.lastDebug.DecisionShadowCycleActionProbability = action.Probabilities[action.Choice]
	// The full distribution, not just the winning choice's own probability —
	// Confidence and the single winning probability were both shown to be
	// close to uninterpretable in isolation during manual calibration
	// (e.g. a clearly-correct choice can carry Confidence well under 0.5).
	// Seeing every option's probability is what actually answers "was
	// ask_user ever seriously considered, or is it structurally near-zero."
	if len(action.Probabilities) > 0 {
		m.lastDebug.DecisionShadowCycleActionProbabilities = make(map[string]float64, len(action.Probabilities))
		for choice, p := range action.Probabilities {
			m.lastDebug.DecisionShadowCycleActionProbabilities[choice] = p
		}
	}

	if goalComplete, ok := msg.result.Answers["goal_complete"]; ok {
		m.lastDebug.DecisionShadowGoalCompleteProbability = goalComplete.Probability
	}
	verifierNeeded, hasVerifierNeeded := msg.result.Answers["semantic_verifier_needed"]
	if hasVerifierNeeded {
		m.lastDebug.DecisionShadowVerifierNeededProbability = verifierNeeded.Probability
	}

	if action.Choice == authoritative {
		m.decisionShadowMetrics.CycleActionAgreement++
	}
	if action.Choice == "finish" && authoritative != "finish" {
		m.decisionShadowMetrics.FalseFinishCount++
	}
	if action.Choice == "ask_user" && authoritative == "ask_user" {
		m.decisionShadowMetrics.AskUserAgreement++
	}

	semanticRan := msg.verifierPath == "semantic"
	if hasVerifierNeeded {
		predictedNeeded := verifierNeeded.Probability >= 0.5
		if predictedNeeded == semanticRan {
			m.decisionShadowMetrics.VerifierNeededAgreement++
		}
		if !predictedNeeded && semanticRan {
			m.decisionShadowMetrics.VerifierNeededFalseNeg++
		}
	}
}

// ============================================================================
// Pre-verifier counterfactual shadow (Phase 2)
//
// The shadow above always fires after agent.Decide() — it measures "did Laya
// predict the cycle's final outcome," but by the time it's asked, a semantic
// verifier (if one ran) has already happened, so it can never cleanly
// isolate "is Laya good at judging whether semantic verification was
// needed" from "is Laya good at predicting the final action." A future
// guarded_assist phase (docs/decision-engine.md) would need exactly that
// narrower judgment BEFORE the verifier runs, to decide whether to force one
// adaptive mode would otherwise skip. This second, earlier observation
// exists purely to gather that calibration evidence now, still 100%
// non-authoritative — see docs/architecture/decisions/0011-pre-verifier-laya-shadow-observation.md.
// ============================================================================

// preVerifierQuestions is deliberately NOT the 5-way cycle_action set: at
// this point in the cycle there is no "final action" to predict yet, only
// whether the deterministic evidence gathered so far looks sufficient.
var preVerifierQuestions = map[string]decision.Question{
	"semantic_verifier_needed": {
		Type:         decision.QuestionNoul,
		Instructions: "Based only on the supplied observable execution evidence, is semantic verification needed before this cycle can safely be considered complete?",
	},
	"evidence_sufficient": {
		Type:         decision.QuestionNoul,
		Instructions: "Is the supplied deterministic evidence sufficient to determine whether the current objective and acceptance criteria are complete?",
	},
}

// agentDecisionPreVerifierShadowMsg mirrors agentDecisionShadowMsg's shape
// exactly, with its own dedicated generation field (agentLoopState's
// preVerifierShadowGen, never decisionShadowGen) — the two shadows have
// different question sets and different arrival timing, so their staleness
// checks must never be able to cross-contaminate each other.
type agentDecisionPreVerifierShadowMsg struct {
	runID   string
	cycle   int
	gen     int
	result  decision.Result
	err     error
	elapsed time.Duration
}

// dispatchAgentDecisionPreVerifierShadow returns a tea.Cmd for one Predict
// call built from the state as it exists immediately after
// ApplyDeterministicCriteria and before any verifier has run — see the call
// site in startAgentVerification for why that ordering is safe. Reuses
// buildAgentDecisionShadowState unmodified: that function only ever reads
// run/execution fields, and run.Criteria at this point in the real flow has
// not yet received any verifier CriteriaUpdates (those land only inside
// AgentRun.CompleteVerification, which has not been called yet), so the
// snapshot is provably pre-verifier without needing a second builder.
func (m *Model) dispatchAgentDecisionPreVerifierShadow(run *agent.AgentRun, execution agent.ExecutionResult) tea.Cmd {
	if m.decisionShadow == nil || m.decisionShadow.service == nil || m.agentLoop == nil {
		return nil
	}
	m.agentLoop.preVerifierShadowGen++
	gen := m.agentLoop.preVerifierShadowGen
	runID, cycle := run.ID, run.Cycle
	state := m.buildAgentDecisionShadowState(run, execution)
	svc := m.decisionShadow.service
	model := m.decisionShadow.model
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), decisionShadowTimeout)
		defer cancel()
		start := time.Now()
		result, err := svc.Predict(ctx, state, preVerifierQuestions, decision.PredictOptions{Model: model})
		return agentDecisionPreVerifierShadowMsg{runID: runID, cycle: cycle, gen: gen, result: result, err: err, elapsed: time.Since(start)}
	}
}

// handleAgentDecisionPreVerifierShadow applies the stale-message guard, then
// only ever calls recordPreVerifierPrediction — never touches run,
// execution, stop, or any authoritative field.
func (m *Model) handleAgentDecisionPreVerifierShadow(msg agentDecisionPreVerifierShadowMsg) (tea.Model, tea.Cmd) {
	if m.agentLoop == nil || m.agentLoop.run == nil ||
		msg.runID != m.agentLoop.run.ID || msg.cycle != m.agentLoop.run.Cycle ||
		msg.gen != m.agentLoop.preVerifierShadowGen {
		return m, nil
	}
	m.recordPreVerifierPrediction(msg)
	return m, nil
}

// maxPreVerifierCorrelations bounds Model.preVerifierCorrelations so a run
// that never reaches handleAgentVerification (cancelled, crashed) cannot
// leak an entry forever — mirrors evictedResourceKeys' bounded-eviction
// pattern in agent_loop.go.
const maxPreVerifierCorrelations = 16

// maxPreVerifierShadowSamples bounds Model.preVerifierShadowSamples, the raw
// probability/outcome pairs a future threshold-sweep report reads. Oldest
// evicted first; this is observability data, not a decision input.
const maxPreVerifierShadowSamples = 256

// preVerifierCorrelation reconciles one cycle's async Laya prediction with
// its later-arriving authoritative outcome. Either half may arrive first —
// see recordPreVerifierPrediction/recordPreVerifierActual — and the record
// is only folded into metrics and deleted once both halves are present.
type preVerifierCorrelation struct {
	runID string
	cycle int

	predictionArrived             bool
	verifierNeededProbability     float64
	evidenceSufficientProbability float64
	predictionErr                 string
	predictionLatency             time.Duration

	actualArrived             bool
	actualSemanticVerifierRan bool
	actualVerifierVerdict     agent.VerificationVerdict
	actualDecision            agent.Decision
}

func preVerifierCorrelationKey(runID string, cycle int) string {
	return runID + ":" + strconv.Itoa(cycle)
}

// preVerifierSample is one finalized (prediction, actual) pair retained for
// a later threshold sweep. Bounded and content-free: a probability and a
// boolean, nothing else.
type preVerifierSample struct {
	Probability float64
	ActualRan   bool
}

// preVerifierShadowMetrics is the pre-verifier counterpart to
// agentDecisionShadowMetrics: session-scoped, in-memory, not persisted. The
// four confusion-matrix counters are diagnostic only, computed at a fixed
// 0.5 probability threshold purely for a first-glance /debug read — no
// production behavior ever reads or acts on them. FalseNegative (Laya said
// verification was NOT needed, but the authoritative pipeline ran one
// anyway) is the priority safety metric for any future guarded_assist gate:
// a false negative here is exactly the case where a future active gate
// would have wrongly skipped a verification that was actually required.
type preVerifierShadowMetrics struct {
	Total              int
	Available          int
	ActualVerifierRuns int
	// Diagnostic-only 0.5-threshold confusion matrix. Never used to gate
	// anything; see the type doc comment.
	TruePositive  int
	FalsePositive int
	TrueNegative  int
	FalseNegative int
}

// recordPreVerifierPrediction stores the prediction half of a correlation
// record, creating it if the actual half hasn't arrived yet, and finalizes
// immediately if the actual half already has (verifier-first ordering).
func (m *Model) recordPreVerifierPrediction(msg agentDecisionPreVerifierShadowMsg) {
	m.preVerifierShadowMetrics.Total++
	entry := m.preVerifierCorrelationEntry(msg.runID, msg.cycle)
	entry.predictionArrived = true
	entry.predictionLatency = msg.elapsed
	if msg.err != nil {
		entry.predictionErr = msg.err.Error()
	} else {
		m.preVerifierShadowMetrics.Available++
		if needed, ok := msg.result.Answers["semantic_verifier_needed"]; ok {
			entry.verifierNeededProbability = needed.Probability
		}
		if sufficient, ok := msg.result.Answers["evidence_sufficient"]; ok {
			entry.evidenceSufficientProbability = sufficient.Probability
		}
	}
	m.lastDebug.DecisionShadowPreVerifierUnavailableReason = entry.predictionErr
	m.lastDebug.DecisionShadowPreVerifierNeededProbability = entry.verifierNeededProbability
	m.lastDebug.DecisionShadowPreVerifierEvidenceSufficientProbability = entry.evidenceSufficientProbability
	m.finalizePreVerifierCorrelationIfReady(entry)
}

// recordPreVerifierActual stores the authoritative-outcome half, called from
// handleAgentVerification once agent.Decide() has resolved — see that call
// site for why decisionShadowVerifierPath's value is exactly
// "did a semantic verifier run this cycle."
func (m *Model) recordPreVerifierActual(runID string, cycle int, semanticVerifierRan bool, verdict agent.VerificationVerdict, decision agent.Decision) {
	entry := m.preVerifierCorrelationEntry(runID, cycle)
	entry.actualArrived = true
	entry.actualSemanticVerifierRan = semanticVerifierRan
	entry.actualVerifierVerdict = verdict
	entry.actualDecision = decision
	m.finalizePreVerifierCorrelationIfReady(entry)
}

// preVerifierCorrelationEntry returns the existing entry for (runID, cycle)
// or creates one, evicting the oldest unfinalized entry first if the bounded
// map is already full.
func (m *Model) preVerifierCorrelationEntry(runID string, cycle int) *preVerifierCorrelation {
	if m.preVerifierCorrelations == nil {
		m.preVerifierCorrelations = make(map[string]*preVerifierCorrelation)
	}
	key := preVerifierCorrelationKey(runID, cycle)
	if entry, ok := m.preVerifierCorrelations[key]; ok {
		return entry
	}
	if len(m.preVerifierCorrelations) >= maxPreVerifierCorrelations {
		for oldestKey := range m.preVerifierCorrelations {
			delete(m.preVerifierCorrelations, oldestKey)
			break
		}
	}
	entry := &preVerifierCorrelation{runID: runID, cycle: cycle}
	m.preVerifierCorrelations[key] = entry
	return entry
}

// finalizePreVerifierCorrelationIfReady folds a correlation record into the
// rolling metrics and a bounded raw sample once both the prediction and the
// actual-outcome halves have arrived, then deletes it — the map only ever
// holds in-flight cycles, never a growing history.
func (m *Model) finalizePreVerifierCorrelationIfReady(entry *preVerifierCorrelation) {
	if !entry.predictionArrived || !entry.actualArrived {
		return
	}
	defer delete(m.preVerifierCorrelations, preVerifierCorrelationKey(entry.runID, entry.cycle))

	m.lastDebug.DecisionShadowActualSemanticVerifierRan = entry.actualSemanticVerifierRan

	if entry.predictionErr != "" {
		return
	}
	m.preVerifierShadowMetrics.ActualVerifierRuns += boolToInt(entry.actualSemanticVerifierRan)

	predictedNeeded := entry.verifierNeededProbability >= 0.5
	switch {
	case predictedNeeded && entry.actualSemanticVerifierRan:
		m.preVerifierShadowMetrics.TruePositive++
	case predictedNeeded && !entry.actualSemanticVerifierRan:
		m.preVerifierShadowMetrics.FalsePositive++
	case !predictedNeeded && !entry.actualSemanticVerifierRan:
		m.preVerifierShadowMetrics.TrueNegative++
	case !predictedNeeded && entry.actualSemanticVerifierRan:
		m.preVerifierShadowMetrics.FalseNegative++
	}

	m.preVerifierShadowSamples = append(m.preVerifierShadowSamples, preVerifierSample{
		Probability: entry.verifierNeededProbability, ActualRan: entry.actualSemanticVerifierRan,
	})
	if len(m.preVerifierShadowSamples) > maxPreVerifierShadowSamples {
		m.preVerifierShadowSamples = m.preVerifierShadowSamples[len(m.preVerifierShadowSamples)-maxPreVerifierShadowSamples:]
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// formatProbabilityDistribution renders a cycle_action probability map as a
// sorted, compact "choice=p.pp" list for /debug last — sorted by choice name
// so the line is stable across cycles/renders rather than reflecting Go's
// randomized map iteration order.
func formatProbabilityDistribution(probabilities map[string]float64) string {
	if len(probabilities) == 0 {
		return ""
	}
	choices := make([]string, 0, len(probabilities))
	for choice := range probabilities {
		choices = append(choices, choice)
	}
	sort.Strings(choices)
	parts := make([]string, 0, len(choices))
	for _, choice := range choices {
		parts = append(parts, fmt.Sprintf("%s=%.2f", choice, probabilities[choice]))
	}
	return strings.Join(parts, " ")
}

// thresholdRow is one row of a threshold-sweep confusion matrix over
// preVerifierSample data. No threshold here is ever selected as a default
// or acted on — this is purely a reporting function for a human (or a
// future PR) to read.
type thresholdRow struct {
	Threshold                   float64
	TruePositive, FalsePositive int
	TrueNegative, FalseNegative int
}

// computeThresholdSweep evaluates samples against each threshold in
// thresholds, treating Probability >= threshold as "predicted needed". Pure
// and side-effect free, reusable by both a future /debug view and the
// opt-in calibration harness (internal/tui/agent_decision_calibration_test.go).
func computeThresholdSweep(samples []preVerifierSample, thresholds []float64) []thresholdRow {
	rows := make([]thresholdRow, 0, len(thresholds))
	for _, threshold := range thresholds {
		row := thresholdRow{Threshold: threshold}
		for _, sample := range samples {
			predictedNeeded := sample.Probability >= threshold
			switch {
			case predictedNeeded && sample.ActualRan:
				row.TruePositive++
			case predictedNeeded && !sample.ActualRan:
				row.FalsePositive++
			case !predictedNeeded && !sample.ActualRan:
				row.TrueNegative++
			case !predictedNeeded && sample.ActualRan:
				row.FalseNegative++
			}
		}
		rows = append(rows, row)
	}
	return rows
}
