package agent

import (
	"testing"
	"time"
)

// TestVerifierNeedsUserInputStopsForUser locks in the verifier's own
// semantic detection path for a clarifying question: distinct from
// ExecutionResult.NeedsUserInput (set only when the user denies a tool
// approval), this fires when the verifier judges the executor's response to
// be substantively a question addressed to the user. Observed in a real
// run: the executor asked "Which source would you like me to check first?"
// but the run had no way to surface that question and instead ground
// through retries to DecisionFailed.
func TestVerifierNeedsUserInputStopsForUser(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("check the weather", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "asked which source to check first"}, now); err != nil {
		t.Fatal(err)
	}
	verify := VerificationResult{
		Verdict:        VerificationInconclusive,
		Summary:        "Which source would you like me to check first?",
		Retryable:      true,
		NeedsUserInput: true,
	}
	if err := run.CompleteVerification(verify, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMemory(now); err != nil {
		t.Fatal(err)
	}
	stop := Decide(run, now)
	if stop.Decision != DecisionNeedsUserInput {
		t.Fatalf("decision = %q, want needs_user_input", stop.Decision)
	}
	if stop.Reason != verify.Summary {
		t.Fatalf("reason = %q, want the executor's actual question %q", stop.Reason, verify.Summary)
	}
}

// TestVerifierNeedsUserInputOutranksConclusiveFailure mirrors the real
// screenshot scenario this fix addresses: the same cycle both failed a tool
// call (a conclusive deterministic failure, reflected here as a "failed"
// verdict already clamped by agentverify.ApplyDeterministicEvidence) and
// asked a genuine question. The question must still reach the user instead
// of being silently swallowed by the failure path.
func TestVerifierNeedsUserInputOutranksConclusiveFailure(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("fetch weather for three cities", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "one fetch failed"}, now); err != nil {
		t.Fatal(err)
	}
	verify := VerificationResult{
		Verdict:        VerificationFailed,
		Summary:        "Please confirm if I should proceed to fetch the next two URLs.",
		Retryable:      false,
		NeedsUserInput: true,
	}
	if err := run.CompleteVerification(verify, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMemory(now); err != nil {
		t.Fatal(err)
	}
	if got := Decide(run, now).Decision; got != DecisionNeedsUserInput {
		t.Fatalf("decision = %q, want needs_user_input even with a failed verdict present", got)
	}
}

// TestDecideRejectsIncompleteVerificationCycle is a defensive regression
// test for REL-001: while a verifier-attempt retry is still in progress (the
// executor has produced evidence but the verifier has not yet produced a
// verdict), the caller must never call Decide on that cycle — Task 3 wires
// the TUI to call Terminate(DecisionVerificationUnavailable, ...) instead.
// This test locks in that if Decide is ever called on this exact in-between
// state anyway, it fails closed as DecisionFailed rather than silently
// treating the missing verification as retryable.
func TestDecideRejectsIncompleteVerificationCycle(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("check the weather", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "fetched partial data", NewEvidence: true}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if run.LatestCycle().Execution == nil || run.LatestCycle().Verification != nil {
		t.Fatalf("cycle = %+v, want execution set and verification nil", run.LatestCycle())
	}

	stop := Decide(run, now.Add(2*time.Second))

	if stop.Decision != DecisionFailed {
		t.Fatalf("decision = %q, want failed", stop.Decision)
	}
	const wantReason = "cycle is missing execution or verification evidence"
	if stop.Reason != wantReason {
		t.Fatalf("reason = %q, want %q", stop.Reason, wantReason)
	}
}
