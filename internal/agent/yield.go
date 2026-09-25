package agent

import "fmt"

// This file implements the pure yield policy from
// docs/architecture/decisions/0013-agent-execution-yield-policy.md (Phase 1
// of the agent-execution-harness plan). A "yield" is a clean, normalized
// assistant completion without executable calls, after protocol handling —
// it is not task completion. EvaluateYield decides what happens at that
// boundary using only the bounded facts in YieldInput: it holds no Model,
// provider client, tool runner, or mutable entity registry, makes no network
// or inference call, and mutates nothing. Wiring this into the TUI's actual
// stream-completion handler is Phase 2 work; this file has no such caller
// yet.

// YieldAction is EvaluateYield's decision at one clean, no-tool assistant
// completion boundary. It never carries a permission grant or a raw
// executable command — only a typed action, its reason (YieldReason), and
// bounded criterion identity (YieldDecision).
type YieldAction string

const (
	// YieldUnknown is the zero value. EvaluateYield never returns it for any
	// input this file defines a rule for; it exists so a caller that hasn't
	// evaluated anything yet has a safe default that can never be mistaken
	// for permission to continue or for completion.
	YieldUnknown YieldAction = ""
	// YieldContinue admits one more bounded model request in the same
	// episode toward an actionable mechanical obligation. It never fires
	// merely because a semantic criterion is pending.
	YieldContinue YieldAction = "continue"
	// YieldVerify reports quiescence — no actionable executable work
	// remains — and hands off to the existing verification policy. It is a
	// bounded evaluation of completion, not a success verdict; unresolved
	// semantic criteria still route here, not to YieldContinue.
	YieldVerify YieldAction = "verify"
	// YieldNeedsUser reports a pending human barrier. The existing approval
	// UI owns approval and the existing ask flow owns input; this action
	// never infers consent from prose.
	YieldNeedsUser YieldAction = "needs_user"
	// YieldWait reports relevant, already-owned asynchronous work in flight
	// (a tool batch or a vision capture). It schedules no new model request
	// and waits for that already-owned event.
	YieldWait YieldAction = "wait"
	// YieldBlocked is a terminal stop — safety/invariant concerns, an
	// exhausted no-progress nudge budget, or infeasible context — that must
	// never be replayed by starting a fresh episode to reset its cap.
	YieldBlocked YieldAction = "blocked"
	// YieldFailed is a terminal stop for an unrecoverable protocol failure
	// (a truncated or malformed completion bounded recovery cannot retry).
	YieldFailed YieldAction = "failed"
	// YieldCancelled is a terminal stop because the run's own context was
	// cancelled. It is distinct from a stale/superseded stream event, which
	// the adapter must already have dropped before ever calling EvaluateYield.
	YieldCancelled YieldAction = "cancelled"
	// YieldBudgetExhausted is a terminal stop for an exhausted hard budget
	// (tokens, tool calls, elapsed, executor requests) or expired run
	// deadline. It takes precedence over waiting or renewing any allowance.
	YieldBudgetExhausted YieldAction = "budget_exhausted"
)

// YieldReason discriminates why EvaluateYield chose an action, for
// diagnostics, checkpoint metadata, and terminal-decision mapping. It is
// never, by itself, authority for a later decision.
type YieldReason string

const (
	YieldReasonUnknown YieldReason = ""
	// ReasonCancelled: the run's context was cancelled.
	ReasonCancelled YieldReason = "cancelled"
	// ReasonSafetyBlocked: an existing safety/invariant/unknown-side-effect
	// outcome was already detected upstream; it is reported, never replayed.
	ReasonSafetyBlocked YieldReason = "safety_blocked"
	// ReasonBudgetExhausted: a hard run budget or deadline was exceeded.
	ReasonBudgetExhausted YieldReason = "budget_exhausted"
	// ReasonPendingApproval: a tool call is waiting on the approval UI.
	ReasonPendingApproval YieldReason = "pending_approval"
	// ReasonPendingAsk: the run is waiting on an existing ask_user answer.
	ReasonPendingAsk YieldReason = "pending_ask"
	// ReasonProtocolRecoverable: an incomplete/truncated completion has a
	// bounded recovery attempt still available.
	ReasonProtocolRecoverable YieldReason = "protocol_recoverable"
	// ReasonProtocolFailure: an incomplete/truncated completion exhausted
	// its bounded recovery attempt.
	ReasonProtocolFailure YieldReason = "protocol_failure"
	// ReasonPendingToolBatch: an already-owned tool batch is still running.
	ReasonPendingToolBatch YieldReason = "pending_tool_batch"
	// ReasonPendingVisionCapture: an already-owned vision capture is still
	// running.
	ReasonPendingVisionCapture YieldReason = "pending_vision_capture"
	// ReasonNoProgressStalled: the configured no-progress nudge budget for
	// this episode is exhausted.
	ReasonNoProgressStalled YieldReason = "no_progress_stalled"
	// ReasonMechanicalObligation: at least one actionable, criterion-linked
	// mechanical obligation remains and context preparation is feasible.
	ReasonMechanicalObligation YieldReason = "mechanical_obligation"
	// ReasonContextInfeasible: mechanical obligations remain, but context
	// preparation for the admitted continuation is not currently feasible.
	ReasonContextInfeasible YieldReason = "context_infeasible"
	// ReasonQuiescent: no unresolved executable obligation or relevant
	// pending asynchronous operation remains. Unresolved semantic criteria
	// do not prevent this reason; they are the verifier's concern.
	ReasonQuiescent YieldReason = "quiescent"
)

// validYieldReason reports whether s is the zero value or one of the closed
// YieldReason vocabulary above. Used only to validate a persisted
// checkpoint's LastYieldReason on load (see validateEpisodeCheckpoint) — a
// live decision always gets its reason from EvaluateYield itself, which can
// only ever produce one of these.
func validYieldReason(s YieldReason) bool {
	switch s {
	case YieldReasonUnknown, ReasonCancelled, ReasonSafetyBlocked, ReasonBudgetExhausted,
		ReasonPendingApproval, ReasonPendingAsk, ReasonProtocolRecoverable, ReasonProtocolFailure,
		ReasonPendingToolBatch, ReasonPendingVisionCapture, ReasonNoProgressStalled,
		ReasonMechanicalObligation, ReasonContextInfeasible, ReasonQuiescent:
		return true
	}
	return false
}

// maxCheckpointPolicyVersion is the newest EpisodeCheckpoint.PolicyVersion
// this build knows how to interpret. A checkpoint from a newer build must
// never be silently accepted as if it had this version's shape.
const maxCheckpointPolicyVersion = 1

// validateEpisodeCheckpoint rejects a persisted EpisodeCheckpoint whose
// counters, policy version, or reason could not have been produced by this
// package's own writer (internal/tui/agent_yield.go's handleAgentYield is
// the only production writer). nil is always valid — most cycles have no
// checkpoint at all, and older persisted records predate the field entirely.
// See harness plan §16: "add explicit versioned checkpoint validation in
// decodeRun; reject malformed counters ... and unsupported checkpoint
// policy versions."
func validateEpisodeCheckpoint(ep *EpisodeCheckpoint) error {
	if ep == nil {
		return nil
	}
	if ep.PolicyVersion < 0 || ep.PolicyVersion > maxCheckpointPolicyVersion {
		return fmt.Errorf("%w: unsupported checkpoint policy version %d", ErrCorruptRun, ep.PolicyVersion)
	}
	if ep.ExecutorRequests < 0 || ep.NoProgressNudges < 0 || ep.Revision < 0 {
		return fmt.Errorf("%w: checkpoint counters must not be negative", ErrCorruptRun)
	}
	if len(ep.UnresolvedCriterionIDs) > MaxCriteria {
		return fmt.Errorf("%w: checkpoint unresolved criterion IDs exceed maximum %d", ErrCorruptRun, MaxCriteria)
	}
	if !validYieldReason(ep.LastYieldReason) {
		return fmt.Errorf("%w: unknown checkpoint yield reason %q", ErrCorruptRun, ep.LastYieldReason)
	}
	return nil
}

// MechanicalObligation is one bounded, controller-validated pending
// execution requirement tied to an immutable pinned criterion — never a raw
// model claim (see §7 of the harness plan). A criterion being Pending is
// insufficient by itself: an obligation must have a known observation
// predicate and an admissible way to gather the missing evidence.
type MechanicalObligation struct {
	// CriterionID is the pinned criterion this obligation is linked to.
	CriterionID string
	// Actionable reports whether an admissible way to gather the missing
	// evidence currently exists: the required capability is available,
	// permission was not denied, no needed snapshot was lost, and the
	// obligation is not an unexecutable wildcard check. An obligation with
	// Actionable=false can never justify YieldContinue; the caller must
	// still route it to YieldVerify like any other unresolved criterion.
	Actionable bool
}

// YieldInput is an immutable, adapter-supplied projection of run status for
// one clean, no-tool assistant completion boundary. EvaluateYield decides
// from these bounded facts alone; assembling them (Phase 2) is the TUI
// adapter's job, not this package's.
//
// Fields are intentionally independent yes/no facts rather than raw run
// state: a stale or superseded stream event must already have been dropped
// by the adapter before this input is even built, exactly like OMP's
// onBeforeYield boundary — EvaluateYield has no generation counter to check
// that with.
type YieldInput struct {
	// Cancelled reports the run's own context was cancelled before this
	// boundary was reached.
	Cancelled bool

	// SafetyBlocked reports an existing safety/invariant/unknown-side-effect
	// outcome the caller already detected upstream.
	SafetyBlocked bool
	// SafetyDetail is a short, bounded diagnostic for SafetyBlocked.
	SafetyDetail string

	// BudgetExhausted mirrors the run's own hard budget checks (tokens, tool
	// calls, elapsed, executor requests) and expired run deadlines.
	BudgetExhausted bool
	// BudgetDetail is a short, bounded diagnostic for BudgetExhausted.
	BudgetDetail string

	// PendingApproval reports a tool call is waiting on the approval UI.
	PendingApproval bool
	// PendingAsk reports the run is waiting on an existing ask_user answer.
	PendingAsk bool

	// IncompleteProtocol reports a truncated, error, or otherwise
	// non-runnable assistant completion.
	IncompleteProtocol bool
	// ProtocolRecoveryEligible reports whether existing bounded recovery
	// still permits one more attempt for that incomplete completion. It is
	// meaningless when IncompleteProtocol is false.
	ProtocolRecoveryEligible bool

	// PendingToolBatch reports an already-owned tool batch is still running.
	PendingToolBatch bool
	// PendingVisionCapture reports an already-owned vision capture is still
	// running.
	PendingVisionCapture bool

	// NoProgressNudges is the number of controller nudges already admitted
	// this episode since the last observed relevant progress.
	NoProgressNudges int
	// NudgeLimit is the configured bound on NoProgressNudges. A value <= 0
	// means no cap is configured (never stalls on this rule alone).
	NudgeLimit int

	// MechanicalObligations are bounded, controller-validated pending
	// execution requirements this yield could still resolve. Semantic
	// criteria are deliberately absent from this input: they never justify
	// YieldContinue, so they have no effect on this decision and are the
	// verifier's concern alone.
	MechanicalObligations []MechanicalObligation

	// ContextFeasible reports whether context preparation for an admitted
	// continuation is currently possible. It is only consulted when at
	// least one actionable mechanical obligation remains.
	ContextFeasible bool
}

// YieldDecision is EvaluateYield's pure output. It never contains a
// permission grant or a raw executable command — only a typed action, its
// reason, and bounded criterion identity. Typed recovery references (e.g.
// next-offset hints) are Phase 4 work and deliberately absent here.
type YieldDecision struct {
	Action YieldAction `json:"action"`
	Reason YieldReason `json:"reason"`
	// CriterionIDs are the pinned criterion IDs this decision concerns. For
	// YieldContinue they are the actionable obligations still outstanding;
	// bounded by the pinned criteria list (MaxCriteria).
	CriterionIDs []string `json:"criterion_ids,omitempty"`
	// Note is a short, bounded human-readable diagnostic. It must never be
	// built from raw model or tool output — only from the caller's own
	// bounded detail strings.
	Note string `json:"note,omitempty"`
}

// Quiescent reports whether this decision implies quiescence. YieldVerify is
// the only action that does; there is deliberately no second, independently
// mutable "quiescent" status to keep in sync with it.
func (d YieldDecision) Quiescent() bool { return d.Action == YieldVerify }

// Terminal reports whether this decision ends the episode outright, as
// opposed to continuing, waiting, or hand-off to verification.
func (d YieldDecision) Terminal() bool {
	switch d.Action {
	case YieldNeedsUser, YieldBlocked, YieldFailed, YieldCancelled, YieldBudgetExhausted:
		return true
	default:
		return false
	}
}

// TerminalDecision maps a terminal YieldAction to the existing persisted
// Decision it corresponds to, so a caller ending a run from a yield reuses
// existing terminal vocabulary instead of inventing a new one (the harness
// plan is explicit that new terminal strings must not be added to persisted
// run status unnecessarily). ok is false for a non-terminal action
// (YieldContinue, YieldVerify, YieldWait) — those never end a run by
// themselves.
func (d YieldDecision) TerminalDecision() (Decision, bool) {
	switch d.Action {
	case YieldNeedsUser:
		return DecisionNeedsUserInput, true
	case YieldCancelled:
		return DecisionCancelled, true
	case YieldBudgetExhausted:
		return DecisionBudgetExhausted, true
	case YieldFailed:
		return DecisionFailed, true
	case YieldBlocked:
		if d.Reason == ReasonNoProgressStalled {
			// Matches existing DecisionNoProgress semantics: a stalled
			// no-progress budget, not a verifier rejection or safety issue.
			return DecisionNoProgress, true
		}
		// Safety/invariant and context-infeasible blocks both reuse the
		// existing escalation outcome; internal/tui/agent_loop.go already
		// distinguishes the reason for the user-facing message from its own
		// StopReason text, not from a second Decision value.
		return DecisionEscalated, true
	default:
		return "", false
	}
}

// actionableObligations returns the criterion IDs of every actionable
// mechanical obligation, in the order given. A non-actionable obligation
// (denied permission, unavailable capability, lost snapshot, unexecutable
// wildcard) never appears here: it cannot justify YieldContinue and is left
// for the verifier like any other unresolved criterion.
func actionableObligations(obligations []MechanicalObligation) []string {
	var ids []string
	for _, ob := range obligations {
		if !ob.Actionable {
			continue
		}
		ids = append(ids, ob.CriterionID)
		if len(ids) >= MaxCriteria {
			break
		}
	}
	return ids
}

// EvaluateYield is the pure ordered yield policy (harness plan §7). Rules
// are checked in strict precedence order and the first match decides; later
// rules are never consulted once an earlier one fires. It never blocks,
// allocates unboundedly, or depends on wall-clock time — the same input
// always produces the same output.
func EvaluateYield(in YieldInput) YieldDecision {
	// 1. Cancellation and existing safety/invariant/unknown-side-effect
	// outcomes take precedence over everything else and are never replayed.
	// (A stale/superseded stream event is the adapter's concern before this
	// function is ever called — there is no generation counter here.)
	if in.Cancelled {
		return YieldDecision{Action: YieldCancelled, Reason: ReasonCancelled}
	}
	if in.SafetyBlocked {
		return YieldDecision{Action: YieldBlocked, Reason: ReasonSafetyBlocked, Note: truncate(in.SafetyDetail, 256)}
	}

	// 2. Exhausted hard budgets or an expired run deadline outrank waiting
	// or renewing any tool-round allowance.
	if in.BudgetExhausted {
		return YieldDecision{Action: YieldBudgetExhausted, Reason: ReasonBudgetExhausted, Note: truncate(in.BudgetDetail, 256)}
	}

	// 3. A pending human barrier: the existing approval UI owns approval,
	// the existing ask flow owns input. No consent is ever inferred here.
	if in.PendingApproval {
		return YieldDecision{Action: YieldNeedsUser, Reason: ReasonPendingApproval}
	}
	if in.PendingAsk {
		return YieldDecision{Action: YieldNeedsUser, Reason: ReasonPendingAsk}
	}

	// 4. An incomplete/truncated/malformed completion is not an ordinary
	// yield. A typed recoverable reason is only honored while existing
	// bounded recovery still allows it; otherwise it is a failure.
	if in.IncompleteProtocol {
		if in.ProtocolRecoveryEligible {
			return YieldDecision{Action: YieldContinue, Reason: ReasonProtocolRecoverable}
		}
		return YieldDecision{Action: YieldFailed, Reason: ReasonProtocolFailure}
	}

	// 5. Relevant, already-owned asynchronous work in flight: wait for it
	// rather than starting a new model request.
	if in.PendingToolBatch {
		return YieldDecision{Action: YieldWait, Reason: ReasonPendingToolBatch}
	}
	if in.PendingVisionCapture {
		return YieldDecision{Action: YieldWait, Reason: ReasonPendingVisionCapture}
	}

	// 6. An exhausted no-progress nudge budget stops the episode outright.
	// This is a terminal stop, never routed through a fresh episode to
	// reset the cap.
	if in.NudgeLimit > 0 && in.NoProgressNudges >= in.NudgeLimit {
		return YieldDecision{Action: YieldBlocked, Reason: ReasonNoProgressStalled}
	}

	// 7-8. Eligible mandatory mechanical execution remains: continue,
	// provided context preparation for that continuation is feasible.
	if ids := actionableObligations(in.MechanicalObligations); len(ids) > 0 {
		if !in.ContextFeasible {
			return YieldDecision{Action: YieldBlocked, Reason: ReasonContextInfeasible, CriterionIDs: ids}
		}
		return YieldDecision{Action: YieldContinue, Reason: ReasonMechanicalObligation, CriterionIDs: ids}
	}

	// 9. Otherwise: quiescent. This includes any unresolved semantic
	// criteria — they are a bounded evaluation for the verifier, never
	// grounds for YieldContinue by themselves.
	return YieldDecision{Action: YieldVerify, Reason: ReasonQuiescent}
}
