package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
)

// This file is the single seam between the authoritative agent
// orchestration (agent_loop.go) and Laya's advisory decision support
// (agent_decision_shadow.go, agent_yield_shadow.go,
// agent_criterion_assessment.go, agent_decision_policy.go).
//
// Two kinds of Laya involvement exist, and the orchestrator reaches each only
// through the helpers below:
//
//   - Shadow observations (agentShadowState) are fire-and-forget. They record
//     diagnostics next to decisions the controller has already made or is
//     about to make, and nothing they return can reach run state.
//   - Assists (agentAssistState) are the only Laya paths the controller waits
//     on, and each can only ADD the existing semantic verifier to a cycle
//     that would otherwise complete synthetically — never skip, replace, or
//     decide one. Both ship inert: their calibration profile maps are empty.
//
// Keeping these here means a change to cycle, verification, or stop logic
// does not have to reason about each advisory hook separately, and a new
// hook cannot quietly acquire authority by being wired into the middle of
// the authoritative flow (audit P2-10).

// agentShadowState holds the staleness generations of the shadow-only
// observations. Each has its own counter so a late result from one shadow
// can never satisfy another's staleness check.
type agentShadowState struct {
	// decisionGen guards the post-cycle decision shadow's async Predict
	// result the same way verifyGen/contractGen guard the verifier/contract
	// results (see agentDecisionShadowMsg) — bumped once per dispatched
	// shadow call, so a stale response from a superseded or cancelled cycle
	// is discarded. No cancel or in-flight fields are needed: the call is
	// fire-and-forget with its own bounded context.
	decisionGen int
	// yieldGen identifies one optional Phase 7 yield observation per cycle.
	yieldGen int
	// criterionAssessmentGen guards the Phase 3 criterion-assessment batch,
	// which has its own question/state contract.
	criterionAssessmentGen int
	// preVerifierGen guards the pre-verifier counterfactual shadow, which
	// has different questions and arrival timing from the decision shadow.
	preVerifierGen int
}

// agentAssistState backs the two add-only assists, the only Laya paths the
// controller can wait on and therefore must be able to cancel.
type agentAssistState struct {
	// criterionGen/criterionCancel/criterionPending guard the Phase 4b
	// criterion-assist wait. A live assist may only add the existing
	// semantic verifier; cancellation or a later cycle invalidates it.
	criterionGen     int
	criterionCancel  context.CancelFunc
	criterionPending bool
	// guardedGen/guardedCancel/pendingVerificationPlan back Phase 1's
	// guarded-assist decision wait (agent_decision_policy.go).
	// dispatchGuardedAssist derives its context from the run's own, and
	// cancelling mid-wait must both stop waiting and never let a stale
	// result resolve a cycle a cancellation already settled another way.
	// pendingVerificationPlan holds the cycle's fallback synthetic result
	// while the Predict call that may supersede it is in flight; nil
	// whenever no guarded decision is pending.
	guardedGen              int
	guardedCancel           context.CancelFunc
	pendingVerificationPlan *agent.VerificationResult
}

// agentVerificationShadows bundles the shadow observations dispatched for
// one cycle's verification. The criterion-assessment and yield shadows are
// captured once, before any verifier result can arrive; the pre-verifier
// shadow is dispatched lazily at the moment the orchestrator commits to a
// route, exactly once per cycle.
type agentVerificationShadows struct {
	criterion   tea.Cmd
	yield       tea.Cmd
	preVerifier func() tea.Cmd
}

// observeAgentVerification dispatches the shadow observations for one
// cycle's verification. It must be called after the cycle's execution is
// committed and deterministic criteria are applied, and before any verifier
// dispatch, so every shadow sees the same immutable cycle state.
func (m *Model) observeAgentVerification(run *agent.AgentRun, execution agent.ExecutionResult, plan agentVerificationPlan) agentVerificationShadows {
	criterion := m.dispatchCriterionAssessment(run, execution)
	yield := m.dispatchAgentYieldShadow(run, execution, plan)
	return agentVerificationShadows{
		criterion: criterion,
		yield:     yield,
		preVerifier: func() tea.Cmd {
			return m.dispatchAgentDecisionPreVerifierShadow(run, execution)
		},
	}
}

// observeAgentStopDecision records the authoritative outcome of a completed
// verification for every shadow that correlates against it. stop is
// already applied; nothing here may change it. path is "semantic" when a
// real verifier request ran, otherwise "deterministic".
func (m *Model) observeAgentStopDecision(run *agent.AgentRun, execution agent.ExecutionResult, path string, stop agent.StopResult, verdict agent.VerificationVerdict) tea.Cmd {
	cmd := m.dispatchAgentDecisionShadow(run, execution, path, stop.Decision, verdict)
	// Only when the decision shadow is wired was a pre-verifier prediction
	// dispatched for this cycle; recording an actual half otherwise would
	// create a correlation entry that can never be finalized.
	if m.decisionShadow != nil {
		m.recordPreVerifierActual(run.ID, run.Cycle, path == "semantic", verdict, stop.Decision, path)
	}
	m.recordAgentYieldShadowActual(run.ID, run.Cycle, normalizeAuthoritativeDecision(stop.Decision))
	return cmd
}

// observeAgentVerificationAbandoned censors shadow observations waiting for
// an actual outcome that a verification-stage termination will never
// provide.
func (m *Model) observeAgentVerificationAbandoned() {
	m.censorPendingAgentYieldShadows()
}

// cancelAgentAdvisory stops every Laya wait and pending observation for a
// cancelled run: the add-only assists are cancelled and invalidated, and
// shadow correlations still waiting for this run's outcome are censored.
func (m *Model) cancelAgentAdvisory() {
	if m.agentLoop.assist.guardedCancel != nil {
		m.agentLoop.assist.guardedCancel()
		m.agentLoop.assist.guardedCancel = nil
	}
	m.censorPendingAgentYieldShadows()
	m.agentLoop.assist.guardedGen++
	if m.agentLoop.assist.criterionCancel != nil {
		m.agentLoop.assist.criterionCancel()
		m.agentLoop.assist.criterionCancel = nil
	}
	m.agentLoop.assist.criterionGen++
	m.agentLoop.assist.criterionPending = false
	m.agentLoop.assist.pendingVerificationPlan = nil
	if m.agentRunActive() {
		// A cancelled cycle's verification never resolves, so its
		// pre-verifier correlation can never receive its actual half.
		m.censorPendingPreVerifierCorrelations(m.agentLoop.run.ID)
	}
}
