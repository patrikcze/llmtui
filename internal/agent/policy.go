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
	if run.ToolCalls > run.Limits.MaxToolCalls {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum %d tool calls reached", run.Limits.MaxToolCalls)}
	}
	if run.PromptTokens+run.CompletionTokens > run.Limits.MaxTokens {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum %d tokens reached", run.Limits.MaxTokens)}
	}
	if now.Sub(run.CreatedAt) >= run.Limits.MaxElapsed {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum elapsed time %s reached", run.Limits.MaxElapsed)}
	}
	if run.RepeatedFailures >= run.Limits.MaxRepeatedFailures {
		return StopResult{Decision: DecisionFailed, Reason: fmt.Sprintf("same failure repeated %d times", run.RepeatedFailures)}
	}

	switch verify.Verdict {
	case VerificationPassed:
		if len(verify.RemainingCriteria) == 0 {
			return StopResult{Decision: DecisionDone, Reason: "observable acceptance criteria passed"}
		}
		return continueResult(run, verify, DecisionContinue)
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
	changed := next != "" && !strings.EqualFold(next, strings.TrimSpace(run.Objective))
	if !changed && !verify.NewEvidence && !verify.StrategyChanged && !verify.TransientFailure {
		return StopResult{Decision: DecisionFailed, Reason: "retry rejected because it has no changed objective, strategy, context, or new evidence"}
	}
	if next == "" {
		next = "Retry the bounded objective using the new evidence or corrected strategy."
	}
	return StopResult{Decision: DecisionRetry, Reason: nonempty(verify.Summary, "verification requested a bounded retry"), NextObjective: next}
}

func continueResult(run *AgentRun, verify *VerificationResult, decision Decision) StopResult {
	if run.Cycle >= run.Limits.MaxCycles {
		return StopResult{Decision: DecisionBudgetExhausted, Reason: fmt.Sprintf("maximum %d cycles reached", run.Limits.MaxCycles)}
	}
	next := strings.TrimSpace(verify.RecommendedNext)
	if next == "" {
		next = "Address the next remaining acceptance criterion: " + verify.RemainingCriteria[0]
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
