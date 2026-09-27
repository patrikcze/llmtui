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

// observationalReadTools are the workspace read-only tools whose typed
// "resource state" failures (see observationalErrorCodes) describe what the
// workspace looks like rather than a broken cycle. The names duplicate
// internal/tools' constants because this package must never import tools;
// internal/tui asserts they stay equal.
var observationalReadTools = map[string]bool{
	"read_file": true, "list_dir": true, "glob": true, "grep": true,
}

// observationalErrorCodes are the typed tools.ErrorInfo codes that report an
// observed resource state: the path does not exist, or the requested line
// window starts after the end of the file.
var observationalErrorCodes = map[string]bool{
	"not_found": true, "range_after_eof": true,
}

// ObservationalFailure reports whether a failed call is a typed observation
// of resource state by a workspace read-only tool — e.g. read_file on a path
// that does not exist. Such an outcome can be the very answer the task asked
// for ("does config.yaml exist?"), so it is evidence for the verifier to
// judge, never by itself a deterministic verdict that the cycle failed.
// Records without an ErrorCode (including every record persisted before the
// field existed) are never observational.
func (r ToolCallRecord) ObservationalFailure() bool {
	return !r.Succeeded && r.ErrorKind == ErrorToolExecution &&
		observationalReadTools[r.Name] && observationalErrorCodes[r.ErrorCode]
}

// ObservationalReadToolNames returns the tool names ObservationalFailure
// recognizes, for internal/tui's constant-parity test.
func ObservationalReadToolNames() []string {
	names := make([]string, 0, len(observationalReadTools))
	for name := range observationalReadTools {
		names = append(names, name)
	}
	return names
}

// ValidToolErrorCode reports whether code has the shape of a producer-owned
// typed error code: 1-64 bytes of lowercase ASCII letters, digits, or
// underscores. Anything else is dropped rather than persisted.
func ValidToolErrorCode(code string) bool {
	if code == "" || len(code) > 64 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

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
