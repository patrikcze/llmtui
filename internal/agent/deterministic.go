package agent

import "fmt"

// EvaluateDeterministic derives a verdict from mechanical evidence alone.
// It is conclusive (ok=true) only for observable failure or blockage — a
// failed test, a failed or denied tool call, a typed execution error. It
// never concludes success: absence of failure is not proof of completion.
// Callers use it both to skip a semantic verification whose verdict would
// be discarded anyway and to clamp an optimistic semantic verdict.
func EvaluateDeterministic(execution ExecutionResult) (VerificationResult, bool) {
	for _, test := range execution.TestsRun {
		if !test.Passed {
			return deterministicVerdict(VerificationFailed, "deterministic test failure: "+test.Name, true, false), true
		}
	}
	// Only the cycle's most recent tool call decides whether it ended in
	// blockage. A tool call failing and being recovered from later in the
	// same cycle — try a path, get "not found", read the corrected path — is
	// a normal, expected agentic sequence, not a stall; only a failure
	// nothing ran after means the cycle actually stopped there. Permission
	// denial is still checked below regardless of position because it
	// aborts the executor rather than being something later calls run past.
	//
	// A trailing *observational* failure (a read-only tool reporting that a
	// path does not exist, say) is not a stall either: it may be exactly the
	// answer the task asked for, so it is left to the verifier to judge
	// rather than concluded here. See ToolCallRecord.ObservationalFailure.
	if n := len(execution.ToolCalls); n > 0 {
		if last := execution.ToolCalls[n-1]; !last.Succeeded && !last.ObservationalFailure() {
			if last.ErrorKind == ErrorPermissionDenied {
				return deterministicVerdict(VerificationBlocked, "tool permission was denied", false, false), true
			}
			verdict := deterministicVerdict(VerificationFailed, "deterministic tool failure: "+last.Name, true, false)
			// A controller-authored recovery objective makes the first
			// occurrence of this failure eligible for one bounded retry (the
			// stop policy's "objective changed" rule). An identical failure in
			// the retry cycle yields the same objective again, so the policy
			// then rejects a further retry — it cannot loop.
			verdict.RecommendedNext = RecoveryObjective(last)
			return verdict, true
		}
	}
	for _, runErr := range execution.Errors {
		switch runErr.Kind {
		case ErrorPermissionDenied:
			return deterministicVerdict(VerificationBlocked, "execution requires user permission", false, false), true
		case ErrorSafety:
			return deterministicVerdict(VerificationBlocked, "execution encountered a safety constraint", false, false), true
		case ErrorCancelled:
			return deterministicVerdict(VerificationBlocked, "execution was cancelled", false, false), true
		case ErrorTimeout:
			return deterministicVerdict(VerificationFailed, "deterministic execution timeout", true, true), true
		case ErrorTruncated:
			// The reply was cut off by max_tokens: it may be garbled or a
			// dropped tool call reduced to plain text. Never trust any read
			// of a possibly-incomplete answer as success.
			return deterministicVerdict(VerificationFailed, "deterministic execution error: response truncated by max_tokens", true, true), true
		case ErrorProvider, ErrorInvariant:
			return deterministicVerdict(VerificationFailed, "deterministic execution error: "+string(runErr.Kind), true, false), true
		case ErrorToolValidation, ErrorToolExecution:
			// Always recorded 1:1 with a ToolCallRecord (see
			// recordAgentToolResultsCount), so the trailing-call check above
			// already covers these — checking again here would judge the
			// same recovered-or-not failure twice and undo that recovery
			// exemption for the exact case it exists to handle.
		}
	}
	return VerificationResult{}, false
}

// RecoveryObjective is the controller-authored next objective after a cycle
// ended on a failed tool call. It is built only from bounded, controller-
// recorded receipt fields — the tool name, its dedup-safe detail (see
// ToolCallRecord.Detail), and its typed error kind/code — never from model
// prose or tool output. The detail is quoted so it reads as data.
func RecoveryObjective(call ToolCallRecord) string {
	target := call.Name
	if call.Detail != "" {
		target = fmt.Sprintf("%s(%q)", call.Name, call.Detail)
	}
	cause := string(call.ErrorKind)
	if call.ErrorCode != "" {
		cause += "/" + call.ErrorCode
	}
	if cause == "" {
		cause = "error"
	}
	return truncate(fmt.Sprintf("Recover from the failed %s call (%s), then complete the current objective.", target, cause), 512)
}

func deterministicVerdict(verdict VerificationVerdict, summary string, retryable, transient bool) VerificationResult {
	return VerificationResult{
		Verdict:          verdict,
		Summary:          summary,
		Evidence:         []string{summary},
		Retryable:        retryable,
		Confidence:       1,
		TransientFailure: transient,
	}
}
