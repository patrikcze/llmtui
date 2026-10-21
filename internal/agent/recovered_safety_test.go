package agent

import (
	"errors"
	"testing"
)

// blockedOutsideWorkspace is the receipt pair a confined tool records when
// the model asks for a path outside the workspace: the call refused to act.
func blockedOutsideWorkspace(path string) (ToolCallRecord, RunError) {
	call := ToolCallRecord{Name: "directory_tree", Detail: path, ErrorKind: ErrorSafety, ErrorCode: "safety_block"}
	err := NewToolError(ErrorSafety, "directory_tree", path, errors.New("path "+path+" is outside the workspace"))
	return call, err
}

// recoveredSafetyExecution is the observed recover-and-proceed shape: a
// directory_tree call outside the workspace was blocked, then the model
// searched inside the workspace and finished the task.
func recoveredSafetyExecution() ExecutionResult {
	blocked, blockErr := blockedOutsideWorkspace("/tmp/smoke")
	return ExecutionResult{
		Summary: "report written and read back",
		ToolCalls: []ToolCallRecord{
			{Name: "read_file", Detail: "TASK.md", Succeeded: true},
			blocked,
			{Name: "glob", Detail: "*.txt", Succeeded: true},
			{Name: "write_file", Detail: "report.json", Succeeded: true},
			{Name: "read_file", Detail: "report.json", Succeeded: true},
		},
		Errors:      []RunError{blockErr},
		NewEvidence: true,
	}
}

func decideAfter(t *testing.T, execution ExecutionResult, verification VerificationResult) StopResult {
	t.Helper()
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("produce the report", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(execution, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteVerification(verification, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMemory(now); err != nil {
		t.Fatal(err)
	}
	return Decide(run, now)
}

func TestRecoveredSafetyBlockDoesNotEscalate(t *testing.T) {
	stop := decideAfter(t, recoveredSafetyExecution(), VerificationResult{Verdict: VerificationPassed, Summary: "report verified"})
	if stop.Decision != DecisionDone {
		t.Fatalf("decision = %q (%s), want done: the model moved past the blocked path", stop.Decision, stop.Reason)
	}
}

func TestTrailingSafetyBlockStillEscalates(t *testing.T) {
	cases := []struct {
		name  string
		calls func(blocked ToolCallRecord) []ToolCallRecord
	}{
		{"only call", func(blocked ToolCallRecord) []ToolCallRecord {
			return []ToolCallRecord{blocked}
		}},
		{"after successful calls", func(blocked ToolCallRecord) []ToolCallRecord {
			return []ToolCallRecord{{Name: "read_file", Detail: "TASK.md", Succeeded: true}, blocked}
		}},
		{"same path blocked again last", func(blocked ToolCallRecord) []ToolCallRecord {
			return []ToolCallRecord{blocked, {Name: "glob", Detail: "*.txt", Succeeded: true}, blocked}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocked, blockErr := blockedOutsideWorkspace("/tmp/smoke")
			calls := tc.calls(blocked)
			errs := make([]RunError, 0, len(calls))
			for _, call := range calls {
				if call.ErrorKind == ErrorSafety {
					errs = append(errs, blockErr)
				}
			}
			execution := ExecutionResult{Summary: "answer", ToolCalls: calls, Errors: errs}
			stop := decideAfter(t, execution, VerificationResult{Verdict: VerificationPassed, Summary: "claimed done"})
			if stop.Decision != DecisionEscalated {
				t.Fatalf("decision = %q (%s), want escalated", stop.Decision, stop.Reason)
			}
		})
	}
}

// TestTrailingSafetyBlockEscalatesWhenAnEarlierOneRecovered keeps one
// recovered block from excusing a later, different block that nothing ran
// after.
func TestTrailingSafetyBlockEscalatesWhenAnEarlierOneRecovered(t *testing.T) {
	first, firstErr := blockedOutsideWorkspace("/tmp/smoke")
	second, secondErr := blockedOutsideWorkspace("/etc")
	execution := ExecutionResult{
		Summary:   "answer",
		ToolCalls: []ToolCallRecord{first, {Name: "glob", Detail: "*.txt", Succeeded: true}, second},
		Errors:    []RunError{firstErr, secondErr},
	}
	if stop := decideAfter(t, execution, VerificationResult{Verdict: VerificationPassed}); stop.Decision != DecisionEscalated {
		t.Fatalf("decision = %q, want escalated", stop.Decision)
	}
}

// TestSafetyErrorWithoutBlockedReceiptEscalates keeps an unattributable
// safety error conservative: later successful calls cannot recover a block
// no recorded tool call accounts for.
func TestSafetyErrorWithoutBlockedReceiptEscalates(t *testing.T) {
	execution := ExecutionResult{
		Summary:   "answer",
		ToolCalls: []ToolCallRecord{{Name: "read_file", Detail: "a.txt", Succeeded: true}},
		Errors:    []RunError{NewToolError(ErrorSafety, "read_file", "/etc/passwd", errors.New("outside the workspace"))},
	}
	if stop := decideAfter(t, execution, VerificationResult{Verdict: VerificationPassed}); stop.Decision != DecisionEscalated {
		t.Fatalf("decision = %q, want escalated", stop.Decision)
	}
}

func TestEvaluateDeterministicRecoveredSafetyBlock(t *testing.T) {
	if result, conclusive := EvaluateDeterministic(recoveredSafetyExecution()); conclusive {
		t.Fatalf("result = %+v, want inconclusive: a recovered block is left to the verifier", result)
	}

	blocked, blockErr := blockedOutsideWorkspace("/tmp/smoke")
	trailing := ExecutionResult{
		ToolCalls: []ToolCallRecord{{Name: "glob", Detail: "*.txt", Succeeded: true}, blocked},
		Errors:    []RunError{blockErr},
	}
	result, conclusive := EvaluateDeterministic(trailing)
	if !conclusive || result.Verdict == VerificationPassed {
		t.Fatalf("result = %+v, conclusive = %v, want a conclusive non-pass for a trailing block", result, conclusive)
	}
}

func TestPruneRecoveredToolErrorsDropsOnlyRecoveredSafetyBlocks(t *testing.T) {
	recovered := recoveredSafetyExecution()
	PruneRecoveredToolErrors(&recovered)
	if len(recovered.Errors) != 0 {
		t.Fatalf("errors = %+v, want the recovered block dropped", recovered.Errors)
	}
	if got := recovered.ToolCalls[1]; got.ErrorKind != ErrorSafety || got.Succeeded {
		t.Fatalf("blocked receipt = %+v, want it kept", got)
	}

	blocked, blockErr := blockedOutsideWorkspace("/tmp/smoke")
	trailing := ExecutionResult{
		ToolCalls: []ToolCallRecord{{Name: "glob", Detail: "*.txt", Succeeded: true}, blocked},
		Errors:    []RunError{blockErr},
	}
	PruneRecoveredToolErrors(&trailing)
	if len(trailing.Errors) != 1 {
		t.Fatalf("errors = %+v, want the trailing block kept", trailing.Errors)
	}
}
