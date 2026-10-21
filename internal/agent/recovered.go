package agent

// This file isolates the "recovered in-cycle tool failure" concept: a tool
// call that failed on bad arguments or a transient execution error, which the
// executor then corrected with a working call on the *same resource* in the
// same cycle. EvaluateDeterministic already ignores such failures for the
// verdict (only the trailing call decides); these helpers extend that same
// treatment to what the *semantic* verifier and MechanicallyComplete see, so
// a normal recover-and-proceed sequence (a malformed ask_user call, then a
// good one; a failed read of report.md, then a corrected read of report.md;
// a path blocked outside the workspace, then a search inside it) is not read
// as a failed cycle. Recovery is resource-attributable, not
// tool-name-only: a failed read_file(missing.md) is never "recovered" merely
// because a later, unrelated read_file(other.md) succeeded — see
// lastResourceOutcome in receipts.go.

// recoveredToolError reports whether a typed execution error is a tool
// failure the executor recovered from later in the same cycle. Argument and
// execution failures recover by a call on the same resource; a safety block
// recovers as described at recoveredSafetyError. Permission, cancellation,
// timeout, truncation, provider, and invariant errors are never "recovered"
// — they abort or invalidate the cycle regardless of what ran afterwards.
func recoveredToolError(err RunError, execution ExecutionResult, lastOutcome map[string]bool) bool {
	switch err.Kind {
	case ErrorToolValidation, ErrorToolExecution:
		// Pre-Phase-1 records, and errors with no narrow resource identity,
		// have an empty Resource and so collapse to the coarser
		// tool-name-only key they were recorded with — see resourceKeyFor.
		return lastOutcome[resourceKeyFor(err.Op, err.Resource)]
	case ErrorSafety:
		return recoveredSafetyError(err, execution)
	default:
		return false
	}
}

// recoveredSafetyError reports whether a safety block is one the executor
// moved past in the same cycle. The block itself already held — the tool
// refused to act — so it escalates the run only when nothing ran after it:
// the cycle's last tool call was a safety block. A block followed by other
// calls (a path outside the workspace, then a glob inside it) is the same
// recover-and-proceed sequence as any other non-trailing tool failure. The
// blocked resource is never expected to succeed later, so recovery cannot be
// same-resource like recoveredToolError's; instead the error must match a
// recorded blocked call, and a safety error with no such receipt is never
// recovered.
func recoveredSafetyError(err RunError, execution ExecutionResult) bool {
	n := len(execution.ToolCalls)
	if err.Kind != ErrorSafety || n == 0 {
		return false
	}
	if last := execution.ToolCalls[n-1]; !last.Succeeded && last.ErrorKind == ErrorSafety {
		return false
	}
	key := resourceKeyFor(err.Op, err.Resource)
	for _, call := range execution.ToolCalls[:n-1] {
		if !call.Succeeded && call.ErrorKind == ErrorSafety && call.resourceKey() == key {
			return true
		}
	}
	return false
}

// hasUnrecoveredSafetyError reports whether execution recorded a safety
// block the executor did not move past (see recoveredSafetyError).
func hasUnrecoveredSafetyError(execution ExecutionResult) bool {
	for _, err := range execution.Errors {
		if err.Kind == ErrorSafety && !recoveredSafetyError(err, execution) {
			return true
		}
	}
	return false
}

// PruneRecoveredToolErrors drops execution.Errors entries for tool failures
// the executor recovered from within the same cycle. The failed
// ToolCallRecord entries are kept — they are still informative and still
// count toward tool-call budgets — only the derived typed errors are removed,
// so a recovered failure no longer dominates the evidence the semantic
// verifier reads.
func PruneRecoveredToolErrors(execution *ExecutionResult) {
	if execution == nil || len(execution.Errors) == 0 {
		return
	}
	last := lastResourceOutcome(*execution)
	kept := make([]RunError, 0, len(execution.Errors))
	for _, e := range execution.Errors {
		if !recoveredToolError(e, *execution, last) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(execution.Errors) {
		return
	}
	execution.Errors = kept
}
