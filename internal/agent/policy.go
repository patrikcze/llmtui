package agent

import (
	"fmt"
	"strings"
	"time"
)

// Decide evaluates hard budgets and the latest deterministic/verification
// evidence. It has no side effects; callers persist and apply its result.
func Decide(run *AgentRun, now time.Time) StopResult {
	if run == nil {
		return StopResult{Decision: DecisionFailed, Reason: "agent state is incomplete"}
	}
	if run.Status == DecisionCancelled {
		return StopResult{Decision: DecisionCancelled, Reason: run.StopReason}
	}
	if len(run.Cycles) == 0 {
		return StopResult{Decision: DecisionFailed, Reason: "agent state is incomplete"}
	}
	cycle := run.LatestCycle()
	if cycle.Execution == nil || cycle.Verification == nil {
		return StopResult{Decision: DecisionFailed, Reason: "cycle is missing execution or verification evidence"}
	}
	exec := cycle.Execution
	verify := cycle.Verification

	if hasErrorKind(exec.Errors, ErrorCancelled) {
		return StopResult{Decision: DecisionCancelled, Reason: "execution was cancelled"}
	}
	if hasErrorKind(exec.Errors, ErrorPermissionDenied) || exec.NeedsUserInput {
		return StopResult{Decision: DecisionNeedsUserInput, Reason: "execution requires explicit user input or permission"}
	}
	if verify.NeedsUserInput {
		return StopResult{Decision: DecisionNeedsUserInput, Reason: nonempty(verify.Summary, "the executor's response requires your input to continue")}
	}
	if hasErrorKind(exec.Errors, ErrorSafety, ErrorInvariant) {
		return StopResult{Decision: DecisionEscalated, Reason: "execution encountered a safety constraint or internal invariant failure"}
	}
	if run.ToolCalls > run.Limits.MaxToolCalls {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum %d tool calls reached", run.Limits.MaxToolCalls)}
	}
	if run.PromptTokens+run.CompletionTokens > run.Limits.MaxTokens {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum %d tokens reached", run.Limits.MaxTokens)}
	}
	if run.Elapsed(now) >= run.Limits.MaxElapsed {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum elapsed time %s reached", run.Limits.MaxElapsed)}
	}
	if run.RepeatedFailures >= run.Limits.MaxRepeatedFailures {
		return StopResult{Decision: DecisionFailed, Reason: fmt.Sprintf("same failure repeated %d times", run.RepeatedFailures)}
	}
	// A verifier may be unable to inspect deliberately redacted raw tool
	// output while still marking every controller-owned criterion satisfied.
	// That is an inconclusive verifier *explanation*, not unfinished work.
	// Retrying here would repeat completed actions or manufacture a question
	// for the user. Explicit input and deterministic failures were handled
	// above and continue to take priority.
	if verify.Verdict == VerificationInconclusive && run.HasCriteria() && len(run.UnresolvedCriteria()) == 0 {
		return StopResult{Decision: DecisionDone, Reason: "all pinned acceptance criteria satisfied"}
	}

	switch verify.Verdict {
	case VerificationPassed:
		// With pinned criteria the controller's own unresolved set is
		// authoritative — the verifier's freeform remaining_criteria list
		// cannot end or extend the run on its own.
		if run.HasCriteria() {
			unresolved := run.UnresolvedCriteria()
			if len(unresolved) == 0 {
				return StopResult{Decision: DecisionDone, Reason: "all pinned acceptance criteria satisfied"}
			}
			return continueResult(run, verify, DecisionContinue, criterionObjective(unresolved[0]))
		}
		if len(verify.RemainingCriteria) == 0 {
			return StopResult{Decision: DecisionDone, Reason: "observable acceptance criteria passed"}
		}
		return continueResult(run, verify, DecisionContinue,
			"Address the next remaining acceptance criterion: "+verify.RemainingCriteria[0])
	case VerificationBlocked:
		if verify.Retryable {
			return retryResult(run, verify)
		}
		return StopResult{Decision: DecisionParked, Reason: nonempty(verify.Summary, "verification is blocked")}
	case VerificationFailed, VerificationInconclusive:
		if !verify.Retryable {
			return StopResult{Decision: DecisionFailed, Reason: nonempty(verify.Summary, "verification did not pass")}
		}
		return retryResult(run, verify)
	default:
		return StopResult{Decision: DecisionFailed, Reason: "verifier returned an unknown verdict"}
	}
}

func retryResult(run *AgentRun, verify *VerificationResult) StopResult {
	if run.Cycle >= run.Limits.MaxCycles {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum %d cycles reached", run.Limits.MaxCycles)}
	}
	next := strings.TrimSpace(verify.RecommendedNext)
	// A rejected answer whose verifier named no next step (empty, or only a
	// restatement of the request — common with small local verifiers) still
	// has corrective work when a pinned criterion is unresolved: that
	// criterion is the objective, exactly as for a passed verdict with
	// unresolved criteria. It is admitted once per run, so the same failure
	// in that cycle still ends the run below.
	if verify.Verdict == VerificationFailed || verify.Verdict == VerificationInconclusive {
		if strings.EqualFold(next, strings.TrimSpace(run.Request)) {
			// Restating the request names no corrective step; counting it
			// as a changed objective would let failures alternate between
			// the request and another objective without limit.
			next = ""
		}
		if next == "" {
			if objective, ok := untriedCriterionObjective(run); ok {
				next = objective
			}
		}
	}
	changed := next != "" && !strings.EqualFold(next, strings.TrimSpace(run.Objective))
	if !changed && !verify.NewEvidence && !verify.StrategyChanged && !verify.TransientFailure {
		// Keep the verifier's own cause: without it the run's only visible
		// stop reason never says what failed (for example a requested file
		// write with no successful write receipt).
		reason := "retry rejected because it has no changed objective, strategy, context, or new evidence"
		if summary := strings.TrimSpace(verify.Summary); summary != "" {
			reason = summary + "; " + reason
		}
		return StopResult{Decision: DecisionFailed, Reason: reason}
	}
	if next == "" {
		next = "Retry the bounded objective using the new evidence or corrected strategy."
	}
	return StopResult{Decision: DecisionRetry, Reason: nonempty(verify.Summary, "verification requested a bounded retry"), NextObjective: next}
}

// criterionObjective is the controller-authored objective for working on
// one unresolved pinned criterion.
func criterionObjective(criterion Criterion) string {
	return "Address the next unresolved acceptance criterion: " + criterion.Text
}

// untriedCriterionObjective returns the objective for the first unresolved
// pinned criterion, unless some cycle of this run already had exactly that
// objective — the bound that keeps a repeated failure from retrying forever.
func untriedCriterionObjective(run *AgentRun) (string, bool) {
	unresolved := run.UnresolvedCriteria()
	if len(unresolved) == 0 {
		return "", false
	}
	objective := criterionObjective(unresolved[0])
	for _, cycle := range run.Cycles {
		if strings.EqualFold(strings.TrimSpace(cycle.Objective), objective) {
			return "", false
		}
	}
	return objective, true
}

func continueResult(run *AgentRun, verify *VerificationResult, decision Decision, fallbackNext string) StopResult {
	if run.Cycle >= run.Limits.MaxCycles {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum %d cycles reached", run.Limits.MaxCycles)}
	}
	next := strings.TrimSpace(verify.RecommendedNext)
	if next == "" {
		next = fallbackNext
	}
	if strings.EqualFold(next, strings.TrimSpace(run.Objective)) && !verify.NewEvidence && !verify.StrategyChanged {
		return StopResult{Decision: DecisionFailed, Reason: "continuation rejected because the next objective did not change"}
	}
	return StopResult{Decision: decision, Reason: "verified progress; acceptance criteria remain", NextObjective: next}
}

func nonempty(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}
