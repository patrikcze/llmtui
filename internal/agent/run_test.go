package agent

import (
	"errors"
	"testing"
	"time"
)

func newTestRun(t *testing.T, limits Limits) (*AgentRun, time.Time) {
	t.Helper()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	run, err := NewRun("run-1", "finish the requested change", limits, now)
	if err != nil {
		t.Fatal(err)
	}
	return run, now
}

func completeCycle(t *testing.T, run *AgentRun, now time.Time, objective string, verification VerificationResult) StopResult {
	t.Helper()
	if err := run.BeginCycle(objective, []string{"system", "user", "tools"}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(ExecutionResult{Objective: objective, Summary: "work completed", NewEvidence: true}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteVerification(verification, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMemory(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return Decide(run, now.Add(4*time.Second))
}

func TestOneCycleSuccessfulCompletion(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	stop := completeCycle(t, run, now, "implement the smallest fix", VerificationResult{
		Verdict: VerificationPassed, Summary: "tests pass", Confidence: 0.95,
	})
	if stop.Decision != DecisionDone {
		t.Fatalf("decision = %q, want done", stop.Decision)
	}
	if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.Status != DecisionDone || run.Cycle != 1 || len(run.Memory) != 1 {
		t.Fatalf("run = %+v", run)
	}
	wantKinds := []string{"run_started", "rules_loaded", "objective_selected", "execution_started", "execution_completed", "verification_started", "verification_completed", "memory_written", "run_done"}
	if len(run.Events) != len(wantKinds) {
		t.Fatalf("events = %d, want %d", len(run.Events), len(wantKinds))
	}
	for i, want := range wantKinds {
		if run.Events[i].Kind != want {
			t.Fatalf("event %d = %q, want %q", i, run.Events[i].Kind, want)
		}
	}
}

func TestVerifierFailureRequiresChangedRetryObjective(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	stop := completeCycle(t, run, now, "run parser test and fix it", VerificationResult{
		Verdict: VerificationFailed, Summary: "parser test still fails", Retryable: true,
		RecommendedNext: "inspect the failing escape-token case and rerun only that test",
		StrategyChanged: true,
	})
	if stop.Decision != DecisionRetry || stop.NextObjective == run.Objective {
		t.Fatalf("stop = %+v", stop)
	}
	if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle(stop.NextObjective, []string{"verification feedback"}, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.Cycle != 2 || run.Objective != stop.NextObjective {
		t.Fatalf("cycle=%d objective=%q", run.Cycle, run.Objective)
	}
}

func TestRetryWithoutProgressStops(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	stop := completeCycle(t, run, now, "same objective", VerificationResult{
		Verdict: VerificationFailed, Summary: "same failure", Retryable: true,
		RecommendedNext: "same objective",
	})
	if stop.Decision != DecisionFailed {
		t.Fatalf("decision = %q, want failed", stop.Decision)
	}
}

func TestRepeatedFailureStopsAtBound(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxRepeatedFailures = 2
	run, now := newTestRun(t, limits)
	v := VerificationResult{
		Verdict: VerificationFailed, Summary: "unchanged failure", Retryable: true,
		RecommendedNext: "changed objective", StrategyChanged: true,
	}
	stop := completeCycle(t, run, now, "objective one", v)
	if stop.Decision != DecisionRetry {
		t.Fatalf("first decision = %q", stop.Decision)
	}
	if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	stop = completeCycle(t, run, now.Add(6*time.Second), "objective two", v)
	if stop.Decision != DecisionFailed || run.RepeatedFailures != 2 {
		t.Fatalf("stop=%+v repeated=%d", stop, run.RepeatedFailures)
	}
}

func TestBudgetsAndPermissionDenial(t *testing.T) {
	t.Run("maximum tool calls", func(t *testing.T) {
		limits := DefaultLimits()
		limits.MaxToolCalls = 1
		run, now := newTestRun(t, limits)
		if err := run.BeginCycle("read one file", nil, now); err != nil {
			t.Fatal(err)
		}
		exec := ExecutionResult{Summary: "read", ToolCalls: []ToolCallRecord{
			{Name: "read_file", Succeeded: true},
			{Name: "read_file", Succeeded: false, ErrorKind: ErrorBudget},
		}}
		if err := run.CompleteExecution(exec, now); err != nil {
			t.Fatal(err)
		}
		if err := run.CompleteVerification(VerificationResult{Verdict: VerificationPassed}, now); err != nil {
			t.Fatal(err)
		}
		if err := run.WriteMemory(now); err != nil {
			t.Fatal(err)
		}
		if got := Decide(run, now).Decision; got != DecisionBudgetExhausted {
			t.Fatalf("decision = %q", got)
		}
	})

	t.Run("permission denial", func(t *testing.T) {
		run, now := newTestRun(t, DefaultLimits())
		if err := run.BeginCycle("write file", nil, now); err != nil {
			t.Fatal(err)
		}
		errDenied := NewError(ErrorPermissionDenied, "write_file", ErrPermissionDenied)
		if !errors.Is(errDenied, ErrPermissionDenied) {
			t.Fatal("RunError does not preserve errors.Is")
		}
		if err := run.CompleteExecution(ExecutionResult{Errors: []RunError{errDenied}, NeedsUserInput: true}, now); err != nil {
			t.Fatal(err)
		}
		if err := run.CompleteVerification(VerificationResult{Verdict: VerificationBlocked}, now); err != nil {
			t.Fatal(err)
		}
		if err := run.WriteMemory(now); err != nil {
			t.Fatal(err)
		}
		if got := Decide(run, now).Decision; got != DecisionNeedsUserInput {
			t.Fatalf("decision = %q", got)
		}
	})
}

func TestSafetyConstraintEscalates(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("inspect protected path", nil, now); err != nil {
		t.Fatal(err)
	}
	safetyErr := NewError(ErrorSafety, "read_file", errors.New("path resolves outside the workspace"))
	if err := run.CompleteExecution(ExecutionResult{Errors: []RunError{safetyErr}}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteVerification(VerificationResult{Verdict: VerificationBlocked, Summary: "workspace boundary held"}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMemory(now); err != nil {
		t.Fatal(err)
	}
	if got := Decide(run, now).Decision; got != DecisionEscalated {
		t.Fatalf("decision = %q, want escalated", got)
	}
}

func TestCancellationTimeoutAndMaximumCycle(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		run, now := newTestRun(t, DefaultLimits())
		run.Cancel("user pressed escape", now)
		if run.Status != DecisionCancelled || Decide(run, now).Decision != DecisionCancelled {
			t.Fatalf("run status = %q", run.Status)
		}
	})

	t.Run("elapsed timeout", func(t *testing.T) {
		limits := DefaultLimits()
		limits.MaxElapsed = time.Second
		run, now := newTestRun(t, limits)
		if err := run.BeginCycle("work", nil, now); err != nil {
			t.Fatal(err)
		}
		if err := run.CompleteExecution(ExecutionResult{Summary: "work"}, now); err != nil {
			t.Fatal(err)
		}
		if err := run.CompleteVerification(VerificationResult{Verdict: VerificationInconclusive, Retryable: true, TransientFailure: true}, now); err != nil {
			t.Fatal(err)
		}
		if err := run.WriteMemory(now); err != nil {
			t.Fatal(err)
		}
		if got := Decide(run, now.Add(time.Second)).Decision; got != DecisionBudgetExhausted {
			t.Fatalf("decision = %q", got)
		}
	})

	t.Run("elapsed run cannot resume", func(t *testing.T) {
		limits := DefaultLimits()
		limits.MaxElapsed = time.Second
		run, now := newTestRun(t, limits)
		if err := run.Resume("retry", now.Add(time.Second)); !errors.Is(err, ErrBudgetExhausted) {
			t.Fatalf("resume error = %v", err)
		}
	})

	t.Run("maximum cycles", func(t *testing.T) {
		limits := DefaultLimits()
		limits.MaxCycles = 1
		run, now := newTestRun(t, limits)
		stop := completeCycle(t, run, now, "work", VerificationResult{
			Verdict: VerificationFailed, Retryable: true, RecommendedNext: "different work", StrategyChanged: true,
		})
		if stop.Decision != DecisionBudgetExhausted {
			t.Fatalf("decision = %q", stop.Decision)
		}
	})
}

func TestTokenBudgetEnforcement(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxTokens = 10
	run, now := newTestRun(t, limits)
	run.RecordUsage(8, 4, now)
	if err := run.BeginCycle("work", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "work"}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteVerification(VerificationResult{Verdict: VerificationFailed, Retryable: true, TransientFailure: true}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMemory(now); err != nil {
		t.Fatal(err)
	}
	if got := Decide(run, now).Decision; got != DecisionBudgetExhausted {
		t.Fatalf("decision = %q", got)
	}
}

func TestResumeStartsFreshCycleWithoutReplayingWork(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	stop := completeCycle(t, run, now, "inspect missing input", VerificationResult{
		Verdict: VerificationBlocked, Summary: "required input is missing", Retryable: false,
	})
	if stop.Decision != DecisionParked {
		t.Fatalf("decision = %q", stop.Decision)
	}
	if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.Resume("use the newly supplied input", now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle(run.Objective, []string{"new_user_input"}, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.Cycle != 2 || run.Status != DecisionRunning || run.Stage != StageExecutor {
		t.Fatalf("run = %+v", run)
	}
	if run.Cycles[0].Execution == nil || run.Cycles[1].Execution != nil {
		t.Fatal("resume replayed or discarded prior observable execution")
	}
}

func TestInvalidTransitionAndMalformedVerdict(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.CompleteExecution(ExecutionResult{}, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v", err)
	}
	if err := run.BeginCycle("objective", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(ExecutionResult{}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteVerification(VerificationResult{Verdict: "probably"}, now); !errors.Is(err, ErrMalformedControl) {
		t.Fatalf("error = %v", err)
	}
}
