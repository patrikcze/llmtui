package agent

import (
	"strings"
	"testing"
	"time"
)

// semanticFailureRun drives one contract-pinned cycle that ended with a plain
// answer (no tool calls, no new evidence) and the given verifier verdict
// through the real run transitions and stop policy.
func semanticFailureRun(t *testing.T, run *AgentRun, verdict VerificationResult, now time.Time) StopResult {
	t.Helper()
	objective := run.Request
	if run.Cycle > 0 {
		objective = run.Objective
	}
	if err := run.BeginCycle(objective, nil, now); err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "parseConfig is only called from main.go. Done."}, now); err != nil {
		t.Fatalf("CompleteExecution: %v", err)
	}
	if err := run.CompleteVerification(verdict, now); err != nil {
		t.Fatalf("CompleteVerification: %v", err)
	}
	if err := run.WriteMemory(now); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	stop := Decide(run, now)
	if err := run.ApplyStop(stop, now); err != nil {
		t.Fatalf("ApplyStop: %v", err)
	}
	return stop
}

func newCallersRun(t *testing.T, now time.Time) *AgentRun {
	t.Helper()
	run, err := NewRun("callers", "Find every caller of parseConfig in this project.", DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginContract(now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteContract([]string{"every caller of parseConfig in the workspace is listed"}, now); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestFailedVerdictWithoutNextStepRetriesUnresolvedCriterionOnce is the
// regression for a run that ended "failed" after its first cycle although
// the executor had not tried anything yet: the verifier rejected an
// unsupported answer ("no search was performed") but, like small local
// verifiers often do, left recommended_next empty or restated the request.
// The retry gate saw no changed objective and stopped. A pinned criterion
// that is still unresolved is itself the corrective objective — the same
// one a passed verdict with unresolved criteria already gets — and it earns
// exactly one attempt: the same failure again ends the run.
func TestFailedVerdictWithoutNextStepRetriesUnresolvedCriterionOnce(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for name, next := range map[string]string{
		"empty recommended_next":      "",
		"recommended_next is request": "Find every caller of parseConfig in this project.",
	} {
		for _, verdict := range []VerificationVerdict{VerificationFailed, VerificationInconclusive} {
			t.Run(name+"/"+string(verdict), func(t *testing.T) {
				run := newCallersRun(t, now)
				result := VerificationResult{
					Verdict: verdict, Summary: "no search was performed; callers were not listed",
					RecommendedNext: next, Retryable: true,
				}
				first := semanticFailureRun(t, run, result, now)
				want := "Address the next unresolved acceptance criterion: every caller of parseConfig in the workspace is listed"
				if first.Decision != DecisionRetry || first.NextObjective != want {
					t.Fatalf("first = %s %q (%q), want retry toward the unresolved criterion", first.Decision, first.NextObjective, first.Reason)
				}
				second := semanticFailureRun(t, run, result, now)
				if second.Decision != DecisionFailed || !strings.Contains(second.Reason, "retry rejected") {
					t.Fatalf("second = %s (%q), want the identical failure to end the run", second.Decision, second.Reason)
				}
				if run.Cycle != 2 {
					t.Fatalf("cycles = %d, want 2", run.Cycle)
				}
			})
		}
	}
}

// TestNonRetryableOrBlockedVerdictKeepsStopping: the fallback only replaces
// a missing next step of a retryable failure. A non-retryable failure and a
// blocked verdict are unchanged.
func TestNonRetryableOrBlockedVerdictKeepsStopping(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	for name, result := range map[string]VerificationResult{
		"failed, not retryable": {Verdict: VerificationFailed, Summary: "the request cannot be done", Retryable: false},
		"blocked, retryable":    {Verdict: VerificationBlocked, Summary: "waiting on an external service", Retryable: true},
	} {
		t.Run(name, func(t *testing.T) {
			run := newCallersRun(t, now)
			stop := semanticFailureRun(t, run, result, now)
			if stop.Decision == DecisionRetry || stop.Decision == DecisionContinue {
				t.Fatalf("decision = %s (%q), want a stop", stop.Decision, stop.Reason)
			}
		})
	}
}
