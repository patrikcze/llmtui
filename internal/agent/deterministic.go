package agent

import "strings"

// InferMechanicalCriteria recognizes deliberately narrow, single-purpose
// requests after observing their runtime operations. Multi-part or ambiguous
// requests remain semantic so missing work cannot be blessed by inference.
func InferMechanicalCriteria(request string, execution ExecutionResult) []CriterionSpec {
	normalized := strings.ToLower(strings.TrimSpace(request))
	if normalized == "" || strings.Contains(normalized, "\n") || strings.Contains(normalized, ";") ||
		strings.Contains(normalized, " and ") || strings.Count(normalized, ",") > 0 {
		return nil
	}
	startsWith := func(prefixes ...string) bool {
		for _, prefix := range prefixes {
			if strings.HasPrefix(normalized, prefix) {
				return true
			}
		}
		return false
	}
	if startsWith("run ", "execute ") {
		if len(execution.TestsRun) == 1 {
			return []CriterionSpec{{Text: "requested test or check passes", Kind: CriterionTestResult, Target: execution.TestsRun[0].Name}}
		}
		if len(execution.ToolCalls) == 1 && execution.ToolCalls[0].Name == "run_command" {
			return []CriterionSpec{{Text: "requested command exits successfully", Kind: CriterionCommandExit, Target: "*"}}
		}
	}
	if startsWith("write ", "create ", "save ") && len(execution.ChangedFiles) == 1 {
		return []CriterionSpec{{Text: "requested file state is written", Kind: CriterionFileState, Target: execution.ChangedFiles[0]}}
	}
	return nil
}

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
	if n := len(execution.ToolCalls); n > 0 {
		if last := execution.ToolCalls[n-1]; !last.Succeeded {
			if last.ErrorKind == ErrorPermissionDenied {
				return deterministicVerdict(VerificationBlocked, "tool permission was denied", false, false), true
			}
			return deterministicVerdict(VerificationFailed, "deterministic tool failure: "+last.Name, true, false), true
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
