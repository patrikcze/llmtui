package agent

// This file isolates the "recovered in-cycle tool failure" concept: a tool
// call that failed on bad arguments or a transient execution error, which the
// executor then corrected with a working call for the same tool in the same
// cycle. EvaluateDeterministic already ignores such failures for the verdict
// (only the trailing call decides); these helpers extend that same treatment
// to what the *semantic* verifier and MechanicallyComplete see, so a normal
// recover-and-proceed sequence (a malformed ask_user call, then a good one)
// is not read as a failed cycle.

// lastToolOutcome maps each tool name to whether its final call in the cycle
// succeeded.
func lastToolOutcome(execution ExecutionResult) map[string]bool {
	out := make(map[string]bool, len(execution.ToolCalls))
	for _, call := range execution.ToolCalls {
		out[call.Name] = call.Succeeded
	}
	return out
}

// recoveredToolError reports whether a typed execution error is a tool
// argument or tool execution failure the executor recovered from later in the
// same cycle. Permission, safety, cancellation, timeout, truncation,
// provider, and invariant errors are never "recovered" — they abort or
// invalidate the cycle regardless of what ran afterwards.
func recoveredToolError(err RunError, lastOutcome map[string]bool) bool {
	switch err.Kind {
	case ErrorToolValidation, ErrorToolExecution:
		return lastOutcome[err.Op]
	default:
		return false
	}
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
	last := lastToolOutcome(*execution)
	kept := make([]RunError, 0, len(execution.Errors))
	for _, e := range execution.Errors {
		if !recoveredToolError(e, last) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(execution.Errors) {
		return
	}
	execution.Errors = kept
}
