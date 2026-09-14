package agent

// This file isolates the "recovered in-cycle tool failure" concept: a tool
// call that failed on bad arguments or a transient execution error, which the
// executor then corrected with a working call on the *same resource* in the
// same cycle. EvaluateDeterministic already ignores such failures for the
// verdict (only the trailing call decides); these helpers extend that same
// treatment to what the *semantic* verifier and MechanicallyComplete see, so
// a normal recover-and-proceed sequence (a malformed ask_user call, then a
// good one; a failed read of report.md, then a corrected read of report.md)
// is not read as a failed cycle. Recovery is resource-attributable, not
// tool-name-only: a failed read_file(missing.md) is never "recovered" merely
// because a later, unrelated read_file(other.md) succeeded — see
// lastResourceOutcome in receipts.go.

// recoveredToolError reports whether a typed execution error is a tool
// argument or tool execution failure the executor recovered from later in
// the same cycle, by a call on the same resource. Permission, safety,
// cancellation, timeout, truncation, provider, and invariant errors are
// never "recovered" — they abort or invalidate the cycle regardless of what
// ran afterwards.
func recoveredToolError(err RunError, lastOutcome map[string]bool) bool {
	switch err.Kind {
	case ErrorToolValidation, ErrorToolExecution:
		// Pre-Phase-1 records, and errors with no narrow resource identity,
		// have an empty Resource and so collapse to the coarser
		// tool-name-only key they were recorded with — see resourceKeyFor.
		return lastOutcome[resourceKeyFor(err.Op, err.Resource)]
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
	last := lastResourceOutcome(*execution)
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
