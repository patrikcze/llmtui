package agent

import (
	"errors"
	"reflect"
	"strings"
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

func TestLiveUserInputPauseResumesSameExecutorCycle(t *testing.T) {
	now := time.Now()
	run, err := NewRun("run-ask", "configure deployment", DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("configure deployment", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WaitForUserInput("Which environment?", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.Status != DecisionNeedsUserInput || run.Stage != StageExecutor || run.Cycle != 1 {
		t.Fatalf("paused run = %+v", run)
	}
	if err := run.ContinueExecutorWithUserInput(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.Status != DecisionRunning || run.Stage != StageExecutor || run.Cycle != 1 {
		t.Fatalf("continued run = %+v", run)
	}
}

func TestPersistedUserInputPauseUsesFreshCycleResume(t *testing.T) {
	now := time.Now()
	run, err := NewRun("run-persisted-ask", "configure deployment", DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("configure deployment", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WaitForUserInput("Which environment?", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.Resume("use staging", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.Stage != StageStopCheck || run.Status != DecisionRunning || run.Cycle != 1 {
		t.Fatalf("resumed persisted run = %+v", run)
	}
	if err := run.BeginCycle("use staging", []string{"new_user_input"}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.Cycle != 2 || run.Stage != StageExecutor {
		t.Fatalf("fresh resumed cycle = %+v", run)
	}
}

func TestNeedsUserInputRunCanBeCancelled(t *testing.T) {
	now := time.Now()
	run, err := NewRun("run-cancel-ask", "configure deployment", DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("configure deployment", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WaitForUserInput("Which environment?", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	run.Cancel("cancelled by user", now.Add(2*time.Second))
	if run.Status != DecisionCancelled || run.StopReason != "cancelled by user" {
		t.Fatalf("cancelled run = %+v", run)
	}
}

// TestTerminateVerificationUnavailablePreservesExecution guards REL-001's
// fix: when the verifier itself fails (provider error, timeout, or repeated
// malformed JSON) after a successful CompleteExecution, the caller must be
// able to terminate the run as DecisionVerificationUnavailable without
// losing the executor's already-observed evidence — Terminate is valid from
// any active stage, including mid-verification, and must not touch the
// cycle it did not complete.
func TestTerminateVerificationUnavailablePreservesExecution(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("check the weather", []string{"system", "user", "tools"}, now); err != nil {
		t.Fatal(err)
	}
	exec := ExecutionResult{
		Objective: "check the weather",
		Summary:   "fetched partial data",
		ToolCalls: []ToolCallRecord{
			{Name: "web_fetch", Detail: "https://weather.com/prague", Succeeded: true},
		},
		NewEvidence: true,
	}
	if err := run.CompleteExecution(exec, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.Stage != StageVerifier {
		t.Fatalf("stage = %q, want verifier", run.Stage)
	}
	// Capture the post-CompleteExecution state (which normalizes nil slices
	// to empty ones — see boundExecution) as the baseline, since the
	// assertion under test is that Terminate itself leaves it untouched, not
	// that CompleteExecution is a no-op transform of its input.
	wantExecution := *run.LatestCycle().Execution

	if err := run.Terminate(DecisionVerificationUnavailable, "verifier exhausted its retry budget with malformed JSON", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	if run.Status != DecisionVerificationUnavailable {
		t.Fatalf("status = %q, want verification_unavailable", run.Status)
	}
	cycle := run.LatestCycle()
	if cycle.Execution == nil {
		t.Fatal("execution was discarded by Terminate")
	}
	if !reflect.DeepEqual(*cycle.Execution, wantExecution) {
		t.Fatalf("execution = %+v, want unchanged %+v", *cycle.Execution, wantExecution)
	}
	if cycle.Verification != nil {
		t.Fatalf("verification = %+v, want nil (verifier never produced a verdict)", cycle.Verification)
	}
	// Unlike ApplyStop, Terminate does not set CompletedAt for any decision
	// it already handles (DecisionFailed, DecisionBudgetExhausted,
	// DecisionNoProgress from internal/tui call sites) — this is consistent
	// with that existing generic contract, not a gap specific to this new
	// decision. See the task report for the full rationale.
	if !cycle.CompletedAt.IsZero() {
		t.Fatalf("completed at = %v, want zero", cycle.CompletedAt)
	}
}

// TestResumeFromVerificationUnavailable confirms Resume treats
// DecisionVerificationUnavailable the same as Parked/NeedsUserInput: a
// fresh cycle with no replay of the incomplete verification attempt. Other
// terminal statuses must continue to refuse resume.
func TestResumeFromVerificationUnavailable(t *testing.T) {
	t.Run("succeeds and starts a fresh cycle without replay", func(t *testing.T) {
		run, now := newTestRun(t, DefaultLimits())
		if err := run.BeginCycle("check the weather", nil, now); err != nil {
			t.Fatal(err)
		}
		if err := run.CompleteExecution(ExecutionResult{Summary: "fetched partial data", NewEvidence: true}, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := run.Terminate(DecisionVerificationUnavailable, "verifier timed out", now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := run.Resume("retry verification with a fresh cycle", now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if run.Status != DecisionRunning || run.Stage != StageStopCheck {
			t.Fatalf("run = %+v", run)
		}
		if err := run.BeginCycle(run.Objective, []string{"system"}, now.Add(4*time.Second)); err != nil {
			t.Fatal(err)
		}
		if run.Cycle != 2 || run.Cycles[0].Execution == nil || run.Cycles[1].Execution != nil {
			t.Fatal("resume replayed or discarded prior observable execution")
		}
	})

	t.Run("other terminal statuses still refuse resume", func(t *testing.T) {
		for _, decision := range []Decision{DecisionDone, DecisionFailed, DecisionBudgetExhausted, DecisionEscalated, DecisionCancelled, DecisionNoProgress} {
			decision := decision
			t.Run(string(decision), func(t *testing.T) {
				run, now := newTestRun(t, DefaultLimits())
				if err := run.BeginCycle("work", nil, now); err != nil {
					t.Fatal(err)
				}
				if err := run.CompleteExecution(ExecutionResult{Summary: "work"}, now); err != nil {
					t.Fatal(err)
				}
				if err := run.Terminate(decision, "terminal", now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := run.Resume("try again", now.Add(2*time.Second)); !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("resume error = %v, want ErrInvalidTransition", err)
				}
			})
		}
	})
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

// TestWriteMemoryRecordsToolCallRecap guards the fix for a real observed
// regression: once a run's prior-cycle raw tool traffic stops being resent
// to the executor (see internal/tui/pipeline.go's history projection),
// MemoryEntry.ToolCalls is the only remaining way the next cycle can know
// it already tried a given URL/path/query and whether it succeeded.
// Without it, a retry cycle blindly repeats already-tried actions.
func TestWriteMemoryRecordsToolCallRecap(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("check the weather", []string{"system", "user", "tools"}, now); err != nil {
		t.Fatal(err)
	}
	exec := ExecutionResult{
		Objective: "check the weather",
		Summary:   "found partial data",
		ToolCalls: []ToolCallRecord{
			{Name: "web_fetch", Detail: "https://weather.com/prague", Succeeded: true},
			{Name: "web_fetch", Detail: "https://accuweather.com/prague", Succeeded: false, ErrorKind: ErrorToolExecution},
		},
		NewEvidence: true,
	}
	if err := run.CompleteExecution(exec, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteVerification(VerificationResult{
		Verdict: VerificationInconclusive, Summary: "some sources missing", Retryable: true, StrategyChanged: true,
	}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMemory(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(run.Memory) != 1 {
		t.Fatalf("Memory = %+v, want one entry", run.Memory)
	}
	want := []string{
		"web_fetch(https://weather.com/prague) succeeded",
		"web_fetch(https://accuweather.com/prague) failed: tool_execution",
	}
	got := run.Memory[0].ToolCalls
	if len(got) != len(want) {
		t.Fatalf("ToolCalls = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ToolCalls[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestCompleteVerificationBoundsUserOptions confirms UserOptions is bounded
// the same way every other verifier-controlled string list is
// (Evidence/FailedCriteria/RemainingCriteria/ProposedCriteria) — a picker
// overlay with hundreds of entries or paragraph-length options would not be
// usable, regardless of what a model returns.
func TestCompleteVerificationBoundsUserOptions(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("check the weather", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "asked which city first"}, now); err != nil {
		t.Fatal(err)
	}
	huge := make([]string, 100)
	for i := range huge {
		huge[i] = strings.Repeat("x", 1000)
	}
	if err := run.CompleteVerification(VerificationResult{
		Verdict: VerificationInconclusive, Summary: "pick a city", NeedsUserInput: true, UserOptions: huge,
	}, now); err != nil {
		t.Fatal(err)
	}
	got := run.LatestCycle().Verification.UserOptions
	if len(got) != 16 {
		t.Fatalf("UserOptions length = %d, want bounded to 16", len(got))
	}
	if !strings.HasSuffix(got[0], "…") || len(got[0]) > 260 {
		t.Fatalf("UserOptions[0] = %d bytes, want truncated to ~256 bytes with an ellipsis suffix", len(got[0]))
	}
}

// TestRecordContextCompressionAppendsDiagnosticEvent locks in the
// observability fix for diagnosing whether context-budget compression ate
// evidence a cycle needed: without this, "did truncation fire, and how
// much was cut" can only be reconstructed after the fact from message
// sizes, as a real stalled/hallucinating run required. A
// contextmgr.Decide().Compress == true request must be visible directly in
// the persisted run's events.
func TestRecordContextCompressionAppendsDiagnosticEvent(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("check the weather", []string{"system", "user"}, now); err != nil {
		t.Fatal(err)
	}
	before := len(run.Events)
	run.RecordContextCompression("summarize", 30000, 28672, now.Add(time.Second))
	if len(run.Events) != before+1 {
		t.Fatalf("Events = %d, want %d", len(run.Events), before+1)
	}
	event := run.Events[len(run.Events)-1]
	if event.Kind != "context_compressed" {
		t.Fatalf("event kind = %q, want context_compressed", event.Kind)
	}
	for _, want := range []string{"summarize", "30000", "28672"} {
		if !strings.Contains(event.Detail, want) {
			t.Fatalf("event detail = %q, missing %q", event.Detail, want)
		}
	}
}

// TestRecordContextCompressionOnNilRunIsSafe mirrors RecordUsage's existing
// nil-receiver contract so callers checked only by higher-level agent-run
// activity flags cannot panic if invoked at the wrong time.
func TestRecordContextCompressionOnNilRunIsSafe(t *testing.T) {
	var run *AgentRun
	run.RecordContextCompression("truncate", 100, 50, time.Now())
}
