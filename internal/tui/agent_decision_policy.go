package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/agentverify"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/decision"
)

// This file is Phase 1 of the Laya decision-architecture plan: the ONE
// active capability Laya is ever permitted — a one-way guarded escalation
// to the existing semantic verifier on an adaptive-mode cycle that would
// otherwise have taken a synthetic (no-verifier) completion shortcut.
// Laya can only ADD a semantic verification call; it can never skip,
// delay, or replace one, satisfy a criterion, or otherwise touch run
// status. See docs/decision-engine.md's "Guarded verifier escalation"
// section and docs/architecture/decisions/0012-laya-guarded-verifier-escalation.md.
//
// SHIPPED-INERT BY DESIGN: decisionCalibrationProfiles is empty in this
// codebase. guarded_assist mode therefore has zero behavioral effect
// regardless of config, because dispatchGuardedAssist always finds no
// resolved profile and defers to the exact same synthetic/semantic route
// startAgentVerification has always taken — see
// TestGuardedAssistWithNoProfileMatchesBaselineForEveryPlannedBranch.

// agentVerificationPlanRoute is the resolved route for one cycle's
// verification.
type agentVerificationPlanRoute int

const (
	agentVerificationPlanSynthetic agentVerificationPlanRoute = iota
	agentVerificationPlanSemantic
)

// agentVerificationPlan is a transient, non-persisted control decision —
// never a store, never serialized, never read back after the cycle it was
// computed for. Exactly one of Result (Synthetic route) or a semantic
// dispatch (Semantic route) is ever acted on by the caller.
type agentVerificationPlan struct {
	Route agentVerificationPlanRoute
	// Result is meaningful only when Route == agentVerificationPlanSynthetic.
	Result agent.VerificationResult
	// GuardEligible is true only for the two adaptive-mode synthetic-PASS
	// branches the plan's §7 permits guarded_assist to ever consider
	// escalating: the early all-resolved-criteria shortcut (only when the
	// resolved verifier mode is itself adaptive) and the mechanically-
	// complete-cycle shortcut. A Semantic route, an off/deterministic/
	// always route, and every synthetic FAILURE/needs-input/blocked
	// outcome are never eligible — see planAgentVerification.
	GuardEligible bool
}

// planAgentVerification computes the exact same verification route
// startAgentVerification has always computed, extracted into a pure
// function so it can be unit-tested directly and so Phase 1's guard check
// can inspect a synthetic route before it is dispatched — without
// duplicating any branch logic. No I/O; no field on any *Model is read or
// written; run/execution are read-only here (both must already be fully
// populated by the caller, exactly as they were before this extraction).
func planAgentVerification(run *agent.AgentRun, execution agent.ExecutionResult, mode string) agentVerificationPlan {
	if run.HasCriteria() && len(run.UnresolvedCriteria()) == 0 && run.ContractCoverageJustified() {
		return agentVerificationPlan{
			Route: agentVerificationPlanSynthetic,
			Result: agent.VerificationResult{
				Verdict: agent.VerificationPassed, Summary: "all pinned acceptance criteria are satisfied",
				Evidence: []string{"criteria ledger resolved"}, Confidence: 1,
			},
			// The shortcut fires regardless of mode (matches existing
			// behavior — it runs before the mode switch below), but
			// guarded-assist eligibility is deliberately narrower: only
			// when the resolved mode is itself adaptive.
			GuardEligible: mode == config.VerifierModeAdaptive,
		}
	}
	switch mode {
	case config.VerifierModeOff:
		return agentVerificationPlan{Route: agentVerificationPlanSynthetic, Result: agent.VerificationResult{
			Verdict: agent.VerificationPassed, Summary: "verification disabled; executor output was not verified",
			Evidence: []string{"verification mode off"}, Confidence: 0,
		}}
	case config.VerifierModeDeterministic:
		return agentVerificationPlan{Route: agentVerificationPlanSynthetic, Result: agentverify.ApplyDeterministicEvidence(agent.VerificationResult{
			Verdict: agent.VerificationPassed, Summary: "no deterministic failure was observed",
			Evidence: []string{"deterministic-only verification configured"}, Confidence: 0.5,
		}, execution)}
	case config.VerifierModeAdaptive:
		if deterministic, conclusive := agent.EvaluateDeterministic(execution); conclusive {
			return agentVerificationPlan{Route: agentVerificationPlanSynthetic, Result: deterministic}
		}
		if unresolved := run.UnresolvedCriteria(); run.HasCriteria() && len(run.UnresolvedSemanticCriteria()) == 0 && len(unresolved) > 0 {
			for _, criterion := range unresolved {
				if criterion.Kind == agent.CriterionUserInput {
					return agentVerificationPlan{Route: agentVerificationPlanSynthetic, Result: agent.VerificationResult{
						Verdict: agent.VerificationInconclusive, Summary: criterion.Text,
						NeedsUserInput: true, Retryable: false, Confidence: 1,
					}}
				}
			}
			return agentVerificationPlan{Route: agentVerificationPlanSynthetic, Result: agent.VerificationResult{
				Verdict: agent.VerificationFailed, Summary: "deterministic acceptance criterion remains unresolved",
				Retryable: true, NewEvidence: execution.NewEvidence, Confidence: 1,
			}}
		}
		if !run.HasCriteria() && run.Cycle != 1 && agent.MechanicallyComplete(execution) {
			return agentVerificationPlan{
				Route: agentVerificationPlanSynthetic,
				Result: agent.VerificationResult{
					Verdict: agent.VerificationPassed, Summary: "deterministic evidence is sufficient: all tool calls and tests succeeded",
					Evidence: []string{"mechanically complete cycle"}, Confidence: 0.7,
				},
				GuardEligible: true,
			}
		}
	case config.VerifierModeAlways:
		// Falls through to the semantic route below — always mode never
		// takes a synthetic shortcut of its own (only the all-resolved
		// shortcut above, which is mode-independent by construction).
	}
	return agentVerificationPlan{Route: agentVerificationPlanSemantic}
}

// decisionCalibrationProfile binds every fact a guarded-assist decision
// depends on to the exact evidence it was calibrated against. A profile is
// versioned CODE DATA (a Go value in decisionCalibrationProfiles below,
// never user-editable config, never a database row, never a public
// threshold knob) with an explicit evidence-report citation — see ADR
// 0012. No profile is ever synthesized or defaulted at runtime.
type decisionCalibrationProfile struct {
	// ModelAlias/ModelRevision bind the exact loaded checkpoint this
	// profile was calibrated against (decision.Result.Routing.Model/Revision).
	// resolveDecisionCalibrationProfile looks profiles up by ModelAlias;
	// ModelRevision exists for the profile's own provenance record and
	// future revision-mismatch checks, not as a lookup key.
	ModelAlias    string
	ModelRevision string
	// Threshold is the frozen P(semantic_verifier_needed) cutoff a G1
	// report justified — never selected online, never defaulted to an
	// arbitrary value such as 0.5.
	Threshold float64
	// MaxWait bounds how long a guarded decision may wait for its strict
	// Predict call before falling back to the cycle's original synthetic
	// result. Also clamped at use to the run's own remaining elapsed
	// budget — see dispatchGuardedAssist — so a profile cannot itself
	// grant more time than the run has left.
	MaxWait time.Duration
	// EvidenceReport names the report this profile's threshold/scope was
	// justified by — a human-readable citation, not machine-checked.
	EvidenceReport string
}

// decisionCalibrationProfiles holds every approved profile, keyed by the
// exact model alias (decision_engine.laya.default_model) it applies to.
//
// EMPTY IN THIS CODEBASE: Phase 1 ships the guarded_assist mechanism with
// no calibration evidence behind it — see this file's package doc comment
// and ADR 0012. decision_engine.mode: guarded_assist therefore currently
// behaves identically to shadow in every real deployment, until a future,
// separately reviewed change adds an entry here backed by an approved G1
// report. A var (not a const map, which Go cannot express anyway) solely
// so tests can inject a profile to exercise guarded_assist's control flow
// without waiting for real calibration evidence — always restored via
// t.Cleanup, mirroring decisionShadowTimeout's own test-override
// convention in agent_decision_shadow.go.
var decisionCalibrationProfiles = map[string]decisionCalibrationProfile{}

// resolveDecisionCalibrationProfile returns the approved profile for alias,
// if any. No fuzzy matching, no fallback profile — an alias with no exact
// entry has no profile.
func resolveDecisionCalibrationProfile(alias string) (decisionCalibrationProfile, bool) {
	profile, ok := decisionCalibrationProfiles[alias]
	return profile, ok
}

// agentDecisionGuardedAssistMsg carries one guarded-assist strict Predict
// result back into Update(). verifyGen guards against the cycle's
// verification having already resolved another way (e.g. cancellation);
// guardGen is this dispatch's own counter, separate from verifyGen, so a
// guarded dispatch that gets superseded (which cannot currently happen
// more than once per cycle, but is guarded defensively the same way every
// other async result in this package is) can never resolve a later one.
// preVerifierGen matches this same Predict call into the ordinary
// pre-verifier correlation bookkeeping it also feeds — see
// dispatchGuardedAssist's doc comment for why this reuses that bookkeeping
// instead of dispatching the shadow a second time.
type agentDecisionGuardedAssistMsg struct {
	runID          string
	cycle          int
	verifyGen      int
	guardGen       int
	preVerifierGen int
	profile        decisionCalibrationProfile
	fallback       agent.VerificationResult
	result         decision.Result
	err            error
	elapsed        time.Duration
}

// dispatchGuardedAssist attempts to start the guarded-assist strict
// prediction for one eligible adaptive-mode synthetic-completion cycle. It
// returns nil — dispatching nothing at all, including the ordinary shadow
// observation — whenever guarded_assist is not actually active/available
// for this cycle: wrong mode, no wired decision engine, no resolved
// calibration profile, or no run-elapsed budget left. The caller is
// responsible for dispatching the ordinary pre-verifier shadow instead
// whenever this returns nil, so every cycle gets exactly one Laya call
// (shadow-only or guarded, never both, never neither) — see
// startAgentVerification.
//
// This is the ONLY function in this package allowed to make a
// verification decision wait on a Laya prediction. Every other shadow
// dispatch remains exactly as before: fire-and-forget, never gating
// anything.
func (m *Model) dispatchGuardedAssist(run *agent.AgentRun, execution agent.ExecutionResult, fallback agent.VerificationResult, runID string, cycle, verifyGen int) tea.Cmd {
	if m.cfg.DecisionEngine.ResolvedMode() != config.DecisionEngineModeGuardedAssist {
		return nil
	}
	if m.decisionShadow == nil || m.decisionShadow.service == nil || m.agentLoop == nil {
		return nil
	}
	profile, ok := resolveDecisionCalibrationProfile(m.decisionShadow.model)
	if !ok {
		return nil
	}
	// The active deadline can never exceed the run's own remaining elapsed
	// budget: a guarded wait must not itself become the reason a run
	// exceeds agent.Limits.MaxElapsed, and a run with no budget left has
	// nothing for Laya to usefully wait for — the existing budget stop
	// rules own that case, not this one.
	remaining := time.Until(run.CreatedAt.Add(run.Limits.MaxElapsed))
	deadline := profile.MaxWait
	if remaining < deadline {
		deadline = remaining
	}
	if deadline <= 0 {
		return nil
	}

	// Register the SAME correlation entry the ordinary pre-verifier shadow
	// dispatch would have (dispatchAgentDecisionPreVerifierShadow in
	// agent_decision_shadow.go) — reusing preVerifierShadowGen/
	// preVerifierCorrelationEntry directly rather than calling that
	// function, since this dispatch's Predict call itself supplies the
	// prediction half once it resolves (see handleAgentDecisionGuardedAssist).
	// This is the "reuse the one pre-verifier prediction for its
	// diagnostic correlation; do not double-call shadow plus assist for
	// the same stage" requirement: a guarded-eligible cycle gets exactly
	// one Laya Predict call, and it counts toward the exact same
	// calibration accounting a shadow-only cycle's call would have.
	m.agentLoop.preVerifierShadowGen++
	preVerifierGen := m.agentLoop.preVerifierShadowGen
	entry := m.preVerifierCorrelationEntry(runID, cycle)
	entry.dispatched = true
	entry.dispatchGen = preVerifierGen

	m.agentLoop.guardedAssistGen++
	guardGen := m.agentLoop.guardedAssistGen
	m.agentLoop.pendingVerificationPlan = &fallback

	state := m.buildAgentDecisionShadowState(run, execution)
	svc := m.decisionShadow.service
	model := m.decisionShadow.model
	// Deliberately derived from m.agentContext(), unlike the shadow's own
	// context.Background()-derived timeout: this call is on the
	// authoritative decision path, so cancelling the run must also cancel
	// it, rather than letting it survive the run the way a purely
	// diagnostic shadow call intentionally does.
	ctx, cancel := context.WithTimeout(m.agentContext(), deadline)
	m.agentLoop.guardedAssistCancel = cancel
	return func() tea.Msg {
		start := time.Now()
		result, err := svc.Predict(ctx, state, preVerifierQuestions, decision.PredictOptions{Model: model, RequireCompleteInput: true})
		return agentDecisionGuardedAssistMsg{
			runID: runID, cycle: cycle, verifyGen: verifyGen, guardGen: guardGen, preVerifierGen: preVerifierGen,
			profile: profile, fallback: fallback, result: result, err: err, elapsed: time.Since(start),
		}
	}
}

// guardedAssistReason is a bounded, closed-vocabulary diagnostic code for
// /debug last and eval.AgentTrial — never raw error text, never a
// probability rendered as if it were a claim of correctness.
type guardedAssistReason string

const (
	guardedAssistReasonEscalated       guardedAssistReason = "escalated"
	guardedAssistReasonBelowThreshold  guardedAssistReason = "below_threshold"
	guardedAssistReasonUnavailable     guardedAssistReason = "unavailable"
	guardedAssistReasonTimeout         guardedAssistReason = "timeout"
	guardedAssistReasonCancelled       guardedAssistReason = "cancelled"
	guardedAssistReasonMalformedResult guardedAssistReason = "malformed_result"
)

// handleAgentDecisionGuardedAssist resolves one guarded-assist Predict
// result. It always feeds the pre-verifier correlation bookkeeping exactly
// once for this dispatch — even when the verification itself has gone
// stale (the run moved on, or was cancelled, before this arrived) — since
// a late-but-real observation still finalizes into shadow calibration
// metrics (Phase 0a's Late accounting) even though it can no longer settle
// an authoritative decision that already resolved another way. Only a
// live (non-stale) result may actually escalate or resolve the pending
// synthetic completion.
func (m *Model) handleAgentDecisionGuardedAssist(msg agentDecisionGuardedAssistMsg) (tea.Model, tea.Cmd) {
	m.recordPreVerifierPrediction(agentDecisionPreVerifierShadowMsg{
		runID: msg.runID, cycle: msg.cycle, gen: msg.preVerifierGen, result: msg.result, err: msg.err, elapsed: msg.elapsed,
	})

	if m.agentLoop == nil || m.agentLoop.run == nil ||
		msg.runID != m.agentLoop.run.ID || msg.cycle != m.agentLoop.run.Cycle ||
		msg.verifyGen != m.agentLoop.verifyGen || msg.guardGen != m.agentLoop.guardedAssistGen {
		return m, nil
	}
	m.agentLoop.pendingVerificationPlan = nil
	if m.agentLoop.guardedAssistCancel != nil {
		m.agentLoop.guardedAssistCancel()
		m.agentLoop.guardedAssistCancel = nil
	}

	run := m.agentLoop.run
	runID, cycle, verifyGen := run.ID, run.Cycle, m.agentLoop.verifyGen

	escalate := false
	var reason guardedAssistReason
	probability := 0.0
	switch {
	case msg.err != nil && errors.Is(msg.err, context.DeadlineExceeded):
		reason = guardedAssistReasonTimeout
	case msg.err != nil && errors.Is(msg.err, context.Canceled):
		reason = guardedAssistReasonCancelled
	case msg.err != nil:
		reason = guardedAssistReasonUnavailable
	default:
		// A strict (RequireCompleteInput) call already guarantees, at the
		// worker boundary, that a successful result was never computed
		// from truncated input — no separate InputUsage check is needed
		// here; an incomplete input surfaces as msg.err instead (mapped
		// from the worker's capacity rejection), landing in the
		// "unavailable" branch above like any other Predict failure.
		if needed, ok := msg.result.Answers["semantic_verifier_needed"]; ok {
			probability = needed.Probability
			escalate = probability >= msg.profile.Threshold
			reason = guardedAssistReasonBelowThreshold
			if escalate {
				reason = guardedAssistReasonEscalated
			}
		} else {
			reason = guardedAssistReasonMalformedResult
		}
	}

	m.lastDebug.DecisionGuardedAssistEligible = true
	m.lastDebug.DecisionGuardedAssistProfile = msg.profile.ModelAlias
	m.lastDebug.DecisionGuardedAssistThreshold = msg.profile.Threshold
	m.lastDebug.DecisionGuardedAssistProbability = probability
	m.lastDebug.DecisionGuardedAssistEscalated = escalate
	m.lastDebug.DecisionGuardedAssistReason = string(reason)

	if !escalate {
		return m, func() tea.Msg {
			return agentVerificationMsg{runID: runID, cycle: cycle, gen: verifyGen, out: agentverify.Output{Result: msg.fallback}}
		}
	}
	ctx, cancel := context.WithCancel(m.agentContext())
	m.agentLoop.verifyCancel = cancel
	return m, m.dispatchVerifierAttempt(run, m.agentLoop.execution, ctx, verifyGen)
}
