package agent

import (
	"errors"
	"testing"
)

// TestEvaluateDeterministicRecoveredToolFailureIsNotConclusive guards the
// missing-file-then-corrected-read pattern: a tool call fails, and the
// executor continues past it to further successful tool calls within the
// same cycle. Judging the cycle by any historical failure (rather than
// whether it actually stalled) previously forced a deterministic FAILED
// verdict here, which skipped semantic verification entirely under
// agent.verifier.mode: adaptive and then hit the retry-stall guard in
// policy.go (no new evidence, since a deterministic verdict never sets
// NewEvidence/StrategyChanged/RecommendedNext) — failing a run whose
// executor had, in fact, fully and correctly recovered.
func TestEvaluateDeterministicRecoveredToolFailureIsNotConclusive(t *testing.T) {
	execution := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "list_dir", Succeeded: true},
			{Name: "write_file", Succeeded: true},
			{Name: "read_file", Succeeded: false, ErrorKind: ErrorToolExecution},
			{Name: "read_file", Succeeded: true},
			{Name: "run_command", Succeeded: true},
		},
		Errors: []RunError{
			NewError(ErrorToolExecution, "read_file", errors.New("stat missing.txt: no such file or directory")),
		},
		Summary: "recovered from the expected missing-file error and finished",
	}
	if _, conclusive := EvaluateDeterministic(execution); conclusive {
		t.Error("conclusive = true, want false: a mid-cycle tool failure followed by further successful calls was recovered from, not a stall")
	}
}

// TestEvaluateDeterministicTrailingToolFailureIsConclusive guards the
// opposite case: the cycle's last tool call is the one that failed, with
// nothing after it — that is a genuine, unrecovered stall and must still be
// flagged.
func TestEvaluateDeterministicTrailingToolFailureIsConclusive(t *testing.T) {
	execution := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "list_dir", Succeeded: true},
			{Name: "read_file", Succeeded: false, ErrorKind: ErrorToolExecution},
		},
		Errors: []RunError{
			NewError(ErrorToolExecution, "read_file", errors.New("stat missing.txt: no such file or directory")),
		},
	}
	result, conclusive := EvaluateDeterministic(execution)
	if !conclusive {
		t.Fatal("conclusive = false, want true: the cycle ended on an unrecovered tool failure")
	}
	if result.Verdict != VerificationFailed {
		t.Errorf("verdict = %v, want VerificationFailed", result.Verdict)
	}
	if !result.Retryable {
		t.Error("Retryable = false, want true: an ordinary tool failure should still be retryable")
	}
}

// TestEvaluateDeterministicTrailingPermissionDeniedIsBlocked preserves the
// existing permission-denial classification for the trailing-call case.
func TestEvaluateDeterministicTrailingPermissionDeniedIsBlocked(t *testing.T) {
	execution := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "list_dir", Succeeded: true},
			{Name: "run_command", Succeeded: false, ErrorKind: ErrorPermissionDenied},
		},
	}
	result, conclusive := EvaluateDeterministic(execution)
	if !conclusive {
		t.Fatal("conclusive = false, want true")
	}
	if result.Verdict != VerificationBlocked {
		t.Errorf("verdict = %v, want VerificationBlocked", result.Verdict)
	}
	if result.Retryable {
		t.Error("Retryable = true, want false: a permission denial is not retryable on its own")
	}
}

// TestEvaluateDeterministicProviderErrorStaysConclusiveRegardlessOfPosition
// guards that execution-level errors unrelated to any specific tool call
// (ErrorProvider, ErrorInvariant) are not swept up in the tool-call recovery
// exemption — they have no paired ToolCallRecord to recover past.
func TestEvaluateDeterministicProviderErrorStaysConclusiveRegardlessOfPosition(t *testing.T) {
	execution := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "list_dir", Succeeded: true},
			{Name: "write_file", Succeeded: true},
		},
		Errors: []RunError{
			NewError(ErrorProvider, "chat", errors.New("provider returned malformed response")),
		},
	}
	result, conclusive := EvaluateDeterministic(execution)
	if !conclusive {
		t.Fatal("conclusive = false, want true: a provider-level error must still be conclusive even with only successful tool calls")
	}
	if result.Verdict != VerificationFailed {
		t.Errorf("verdict = %v, want VerificationFailed", result.Verdict)
	}
}

// TestEvaluateDeterministicNoEvidenceIsInconclusive preserves the baseline:
// no tests, no tool calls, no errors — nothing to be conclusive about.
func TestEvaluateDeterministicNoEvidenceIsInconclusive(t *testing.T) {
	if _, conclusive := EvaluateDeterministic(ExecutionResult{}); conclusive {
		t.Error("conclusive = true, want false for an empty execution result")
	}
}
