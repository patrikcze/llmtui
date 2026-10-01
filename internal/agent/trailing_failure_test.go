package agent

import (
	"strings"
	"testing"
	"time"
)

// trailingFailureRun drives one contract-pinned run through a single executor
// cycle whose last tool call is failed, deterministic verification, and the
// stop policy — the exact adaptive-mode path from audit finding P1-1.
func trailingFailureRun(t *testing.T, run *AgentRun, failed ToolCallRecord, now time.Time) StopResult {
	t.Helper()
	objective := run.Request
	if run.Cycle > 0 {
		objective = run.Objective
	}
	if err := run.BeginCycle(objective, nil, now); err != nil {
		t.Fatalf("BeginCycle: %v", err)
	}
	execution := ExecutionResult{
		Summary:     "the executor reported its answer",
		NewEvidence: true,
		ToolCalls:   []ToolCallRecord{failed},
	}
	if err := run.CompleteExecution(execution, now); err != nil {
		t.Fatalf("CompleteExecution: %v", err)
	}
	verdict, conclusive := EvaluateDeterministic(execution)
	if !conclusive {
		t.Fatalf("EvaluateDeterministic conclusive = false, want true for %+v", failed)
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

func newContractRun(t *testing.T, now time.Time) *AgentRun {
	t.Helper()
	run, err := NewRun("trailing-failure", "update the version in settings.yaml", DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginContract(now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteContract([]string{"settings.yaml contains the new version"}, now); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestTrailingToolFailureRetriesOnceThenFails is the regression for audit
// P1-1: a cycle ending on an unrecovered tool failure used to end the whole
// run as failed in cycle 1, because the deterministic verdict carried no
// changed objective and the stop policy rejected the retry it marked
// Retryable. The first occurrence must now earn exactly one recovery cycle;
// an identical failure in that cycle must still end the run.
func TestTrailingToolFailureRetriesOnceThenFails(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := newContractRun(t, now)
	failed := ToolCallRecord{
		Name: "edit_file", Detail: "settings.yaml", Succeeded: false,
		ErrorKind: ErrorToolExecution, ErrorCode: "match_not_found", Status: ActionExecuted,
	}

	first := trailingFailureRun(t, run, failed, now)
	if first.Decision != DecisionRetry {
		t.Fatalf("first failure decision = %s (%q), want retry", first.Decision, first.Reason)
	}
	want := RecoveryObjective(failed)
	if first.NextObjective != want {
		t.Fatalf("next objective = %q, want %q", first.NextObjective, want)
	}
	for _, fragment := range []string{`edit_file("settings.yaml")`, "tool_execution/match_not_found"} {
		if !strings.Contains(want, fragment) {
			t.Errorf("recovery objective %q is missing %q", want, fragment)
		}
	}

	second := trailingFailureRun(t, run, failed, now)
	if second.Decision != DecisionFailed {
		t.Fatalf("identical second failure decision = %s (%q), want failed", second.Decision, second.Reason)
	}
	if run.Cycle != 2 {
		t.Errorf("cycles = %d, want 2 (one recovery attempt only)", run.Cycle)
	}
}

// TestEvaluateDeterministicObservationalTrailingFailure guards the
// negative-answer case: a read-only tool reporting that a path does not
// exist can itself be the answer, so it must reach the verifier instead of
// being concluded as a failed cycle. Only the typed, closed set of
// observational codes on read-only tools qualifies; the same code on a
// mutating tool, an untyped failure, or a legacy record does not.
func TestEvaluateDeterministicObservationalTrailingFailure(t *testing.T) {
	cases := []struct {
		name           string
		call           ToolCallRecord
		wantConclusive bool
	}{
		{"read_file not_found", ToolCallRecord{Name: "read_file", Detail: "config.yaml", ErrorKind: ErrorToolExecution, ErrorCode: "not_found"}, false},
		{"list_dir not_found", ToolCallRecord{Name: "list_dir", Detail: "docs", ErrorKind: ErrorToolExecution, ErrorCode: "not_found"}, false},
		{"read_file range_after_eof", ToolCallRecord{Name: "read_file", Detail: "a.go", ErrorKind: ErrorToolExecution, ErrorCode: "range_after_eof"}, false},
		{"legacy record without code", ToolCallRecord{Name: "read_file", Detail: "config.yaml", ErrorKind: ErrorToolExecution}, true},
		{"other read failure code", ToolCallRecord{Name: "read_file", Detail: "a.bin", ErrorKind: ErrorToolExecution, ErrorCode: "unsupported_content"}, true},
		{"mutating tool not_found", ToolCallRecord{Name: "edit_file", Detail: "a.go", ErrorKind: ErrorToolExecution, ErrorCode: "not_found"}, true},
		{"validation error", ToolCallRecord{Name: "read_file", ErrorKind: ErrorToolValidation, ErrorCode: "invalid_arguments"}, true},
		{"safety block", ToolCallRecord{Name: "read_file", Detail: "/etc/passwd", ErrorKind: ErrorSafety, ErrorCode: "safety_block"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			execution := ExecutionResult{ToolCalls: []ToolCallRecord{tc.call}, Summary: "answer"}
			result, conclusive := EvaluateDeterministic(execution)
			if conclusive != tc.wantConclusive {
				t.Fatalf("conclusive = %v (%+v), want %v", conclusive, result, tc.wantConclusive)
			}
		})
	}
}

// TestEvaluateDeterministicPermissionDenialHasNoRecoveryObjective keeps the
// denial outcome exactly as before: blocked, not retryable, and no
// controller-authored objective that could route around the user's choice.
func TestEvaluateDeterministicPermissionDenialHasNoRecoveryObjective(t *testing.T) {
	execution := ExecutionResult{ToolCalls: []ToolCallRecord{{Name: "write_file", Detail: "a.txt", ErrorKind: ErrorPermissionDenied}}}
	result, conclusive := EvaluateDeterministic(execution)
	if !conclusive || result.Verdict != VerificationBlocked || result.Retryable || result.RecommendedNext != "" {
		t.Fatalf("denial result = %+v conclusive=%v, want blocked, non-retryable, no recommended next", result, conclusive)
	}
}

func TestRecoveryObjectiveWithoutDetailOrCode(t *testing.T) {
	got := RecoveryObjective(ToolCallRecord{Name: "run_command", ErrorKind: ErrorTimeout})
	want := "Recover from the failed run_command call (timeout), then complete the current objective."
	if got != want {
		t.Errorf("RecoveryObjective = %q, want %q", got, want)
	}
}

func TestValidToolErrorCode(t *testing.T) {
	for code, want := range map[string]bool{
		"not_found": true, "range_after_eof": true, "e2big": true,
		"": false, "Not_Found": false, "not found": false, "a-b": false,
		strings.Repeat("a", 65): false,
	} {
		if got := ValidToolErrorCode(code); got != want {
			t.Errorf("ValidToolErrorCode(%q) = %v, want %v", code, got, want)
		}
	}
}

// TestBoundExecutionDropsMalformedErrorCode ensures only well-formed codes
// are persisted; a malformed value is cleared rather than stored.
func TestBoundExecutionDropsMalformedErrorCode(t *testing.T) {
	execution := ExecutionResult{ToolCalls: []ToolCallRecord{
		{Name: "read_file", ErrorCode: "not_found"},
		{Name: "read_file", ErrorCode: "Not Found\n"},
	}}
	boundExecution(&execution)
	if execution.ToolCalls[0].ErrorCode != "not_found" || execution.ToolCalls[1].ErrorCode != "" {
		t.Errorf("bounded codes = %q, %q; want not_found and empty", execution.ToolCalls[0].ErrorCode, execution.ToolCalls[1].ErrorCode)
	}
}

// TestRejectedRetryKeepsVerifierCause: when a repeated failure's retry is
// rejected, the stop reason still names what failed. Seen live: a run whose
// requested file write never succeeded ended only with "retry rejected
// because it has no changed objective…", which read as if the failure had
// gone unverified.
func TestRejectedRetryKeepsVerifierCause(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	run := newContractRun(t, now)
	failed := ToolCallRecord{
		Name: "write_file", Detail: "summary.md", Succeeded: false,
		ErrorKind: ErrorToolValidation, ErrorCode: "invalid_arguments", Status: ActionExecuted,
	}
	trailingFailureRun(t, run, failed, now)
	second := trailingFailureRun(t, run, failed, now)
	if second.Decision != DecisionFailed {
		t.Fatalf("decision = %s (%q), want failed", second.Decision, second.Reason)
	}
	for _, want := range []string{"deterministic tool failure: write_file", "retry rejected because"} {
		if !strings.Contains(second.Reason, want) {
			t.Errorf("stop reason %q is missing %q", second.Reason, want)
		}
	}
}
