package agent

import "strings"

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
	for _, tool := range execution.ToolCalls {
		if tool.Succeeded {
			continue
		}
		if tool.ErrorKind == ErrorPermissionDenied {
			return deterministicVerdict(VerificationBlocked, "tool permission was denied", false, false), true
		}
		return deterministicVerdict(VerificationFailed, "deterministic tool failure: "+tool.Name, true, false), true
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
		case ErrorToolValidation, ErrorToolExecution, ErrorProvider, ErrorInvariant:
			return deterministicVerdict(VerificationFailed, "deterministic execution error: "+string(runErr.Kind), true, false), true
		}
	}
	return VerificationResult{}, false
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

// MechanicallyComplete reports whether a cycle's execution is clean enough
// that deterministic evidence alone may stand in for semantic verification
// under the adaptive policy: at least one tool actually ran, everything that
// ran succeeded, every test passed, nothing errored, no user input is
// pending, and the executor produced a visible summary. A mechanically clean
// but semantically wrong answer can pass this gate — that is the documented
// adaptive trade-off; `agent.verifier.mode: always` restores full rigor.
func MechanicallyComplete(execution ExecutionResult) bool {
	if len(execution.ToolCalls) == 0 || len(execution.Errors) > 0 || execution.NeedsUserInput {
		return false
	}
	for _, tool := range execution.ToolCalls {
		if !tool.Succeeded {
			return false
		}
	}
	for _, test := range execution.TestsRun {
		if !test.Passed {
			return false
		}
	}
	return strings.TrimSpace(execution.Summary) != ""
}
