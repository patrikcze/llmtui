package agent

// This file is the Phase 1 receipt/recovery-attribution layer: it replaces
// tool-name-only recovery ("a failed read_file is recovered by any later
// successful read_file, of anything") with resource-attributable recovery
// ("a failed read of report.md is recovered only by a later successful
// action on report.md, or an explicit semantic resolution"). See finding #1
// in .claude/tasks/plans/llmtui-agent-evolution.md §4.

// ActionStatus classifies what actually happened to an attempted tool call,
// independent of ToolCallRecord.Succeeded (which only reports the outcome of
// a call that did run). A denied or ledger-blocked call has Succeeded=false
// and a Status other than ActionExecuted: no side effect, and no resource
// state change, can be inferred from that failure alone.
type ActionStatus string

const (
	// ActionExecuted means the call actually ran; Succeeded reports whether
	// it completed without error.
	ActionExecuted ActionStatus = "executed"
	// ActionDenied means the user rejected the call before execution.
	ActionDenied ActionStatus = "denied"
	// ActionBlocked means the runtime withheld execution before it could
	// happen — a no-progress repeat block, a budget ceiling, or an
	// unavailable/undisclosed tool — never a tool-level outcome.
	ActionBlocked ActionStatus = "blocked"
	// ActionUnknown means the call was accepted but no result was ever
	// correlated back to it (see toolBatchPlan.mergeResults' "missing"
	// branch). Whether it ran is genuinely unknown; it must never be
	// silently treated as either success or failure.
	ActionUnknown ActionStatus = "unknown"
)

// resourceKeyFor combines a tool name and its dedup-relevant resource detail
// (see ToolCallRecord's doc comment for exactly what "detail" is and is not
// — a file path, URL, or search pattern, never a full command line or raw
// argument blob) into one attribution key. An empty detail collapses to the
// tool name alone: both a call with no narrow resource identity (ask_user,
// discovery) and a legacy RunError with no recorded Resource fall back to
// this same tool-name-only key, which is the attribution those cases already
// had and is appropriate for them.
func resourceKeyFor(name, detail string) string {
	if detail == "" {
		return name
	}
	return name + "\x1f" + detail
}

// resourceKey identifies the specific resource/action instance a recorded
// tool call acted on, for attributable recovery.
func (r ToolCallRecord) resourceKey() string {
	return resourceKeyFor(r.Name, r.Detail)
}

// lastResourceOutcome maps each resource key to whether its final recorded
// call in the cycle succeeded. It is the resource-attributable replacement
// for a prior tool-name-only map: a failed read of report.md is no longer
// "recovered" by an unrelated successful read of other.md, because they now
// map to different keys instead of colliding on "read_file".
func lastResourceOutcome(execution ExecutionResult) map[string]bool {
	out := make(map[string]bool, len(execution.ToolCalls))
	for _, call := range execution.ToolCalls {
		out[call.resourceKey()] = call.Succeeded
	}
	return out
}
