package agent

import "testing"

func TestPruneRecoveredToolErrors(t *testing.T) {
	// Two malformed ask_user calls, then a valid one, then a write — the
	// normal recover-and-proceed shape.
	exec := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "ask_user", Succeeded: false, ErrorKind: ErrorToolValidation},
			{Name: "ask_user", Succeeded: false, ErrorKind: ErrorToolValidation},
			{Name: "ask_user", Succeeded: true},
			{Name: "write_file", Succeeded: true},
		},
		Errors: []RunError{
			{Kind: ErrorToolValidation, Op: "ask_user", Message: "missing question"},
			{Kind: ErrorToolValidation, Op: "ask_user", Message: "missing question"},
		},
	}
	PruneRecoveredToolErrors(&exec)
	if len(exec.Errors) != 0 {
		t.Fatalf("Errors = %+v, want the recovered ask_user failures dropped", exec.Errors)
	}
	if len(exec.ToolCalls) != 4 {
		t.Fatalf("ToolCalls = %d, want the failed records kept", len(exec.ToolCalls))
	}
}

func TestPruneRecoveredToolErrorsKeepsUnrecoveredAndNonToolErrors(t *testing.T) {
	exec := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "read_file", Succeeded: false, ErrorKind: ErrorToolExecution},
			{Name: "write_file", Succeeded: true},
		},
		Errors: []RunError{
			{Kind: ErrorToolExecution, Op: "read_file"},     // read_file never succeeded → kept
			{Kind: ErrorPermissionDenied, Op: "write_file"}, // never "recovered" → kept
			{Kind: ErrorProvider, Op: "verify"},             // not a tool error → kept
		},
	}
	PruneRecoveredToolErrors(&exec)
	if len(exec.Errors) != 3 {
		t.Fatalf("Errors = %+v, want all three kept", exec.Errors)
	}
}

// TestRecoveredToolErrorsRequireSameResource is the Phase 1 fix for a
// previously characterized gap (see git history for
// TestRecoveredToolErrorsCurrentlyMatchOnlyByToolName): a failed read of one
// path is no longer treated as recovered by a later successful read of an
// unrelated path merely because both calls share a tool name. Recovery
// attribution now keys on the resource an error's ToolCallRecord names,
// mirrored onto RunError.Resource by agent.NewToolError.
func TestRecoveredToolErrorsRequireSameResource(t *testing.T) {
	exec := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "read_file", Detail: "missing.md", Succeeded: false, ErrorKind: ErrorToolExecution},
			{Name: "read_file", Detail: "other.md", Succeeded: true},
		},
		Errors: []RunError{{Kind: ErrorToolExecution, Op: "read_file", Resource: "missing.md", Message: "read missing.md: not found"}},
	}

	PruneRecoveredToolErrors(&exec)

	if len(exec.Errors) != 1 {
		t.Fatalf("errors = %+v, want the unrelated read of other.md to leave missing.md's failure unresolved", exec.Errors)
	}
}

// TestRecoveredToolErrorsResolveOnSameResourceCorrection proves the positive
// case: a failed call recovers when the correction targets the same
// resource, not merely the same tool.
func TestRecoveredToolErrorsResolveOnSameResourceCorrection(t *testing.T) {
	exec := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "read_file", Detail: "report.md", Succeeded: false, ErrorKind: ErrorToolExecution},
			{Name: "read_file", Detail: "report.md", Succeeded: true},
		},
		Errors: []RunError{{Kind: ErrorToolExecution, Op: "read_file", Resource: "report.md", Message: "transient read failure"}},
	}

	PruneRecoveredToolErrors(&exec)

	if len(exec.Errors) != 0 {
		t.Fatalf("errors = %+v, want the corrected re-read of report.md to resolve the failure", exec.Errors)
	}
}

// TestRecoveredToolErrorsWithoutResourceNeverRecovers proves the safe
// default for an error recorded with no resource identity (a pre-Phase-1
// persisted record, or a tool with no narrow detail): with nothing to
// attribute recovery to, it is never marked recovered by an unrelated
// same-tool success, matching this package's "cannot acquire stronger proof
// than originally recorded" rule for old data (see CLAUDE.md's Agent State
// Model notes and .claude/tasks/plans/llmtui-agent-evolution.md §7).
func TestRecoveredToolErrorsWithoutResourceNeverRecovers(t *testing.T) {
	exec := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "read_file", Detail: "missing.md", Succeeded: false, ErrorKind: ErrorToolExecution},
			{Name: "read_file", Detail: "other.md", Succeeded: true},
		},
		Errors: []RunError{{Kind: ErrorToolExecution, Op: "read_file", Message: "read missing.md: not found"}},
	}

	PruneRecoveredToolErrors(&exec)

	if len(exec.Errors) != 1 {
		t.Fatalf("errors = %+v, want an error with no resource identity to stay unresolved rather than match by tool name alone", exec.Errors)
	}
}

func TestMechanicallyCompleteIgnoresRecoveredFailure(t *testing.T) {
	recovered := ExecutionResult{
		Summary: "asked and wrote",
		ToolCalls: []ToolCallRecord{
			{Name: "ask_user", Succeeded: false, ErrorKind: ErrorToolValidation},
			{Name: "ask_user", Succeeded: true},
			{Name: "write_file", Succeeded: true},
		},
	}
	if !MechanicallyComplete(recovered) {
		t.Fatal("a cycle whose only failures were recovered should be mechanically complete")
	}

	unrecovered := ExecutionResult{
		Summary:   "tried and failed",
		ToolCalls: []ToolCallRecord{{Name: "read_file", Succeeded: false, ErrorKind: ErrorToolExecution}},
	}
	if MechanicallyComplete(unrecovered) {
		t.Fatal("a cycle whose tool never succeeded is not mechanically complete")
	}
}

func TestCollectEvidenceNamesSuccessfulTools(t *testing.T) {
	items := CollectEvidence(1, ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "ask_user", Succeeded: false, ErrorKind: ErrorToolValidation},
			{Name: "ask_user", Succeeded: true},
			{Name: "write_file", Succeeded: true},
			{Name: "write_file", Succeeded: true},
		},
	})
	var namedSuccess []string
	for _, it := range items {
		if it.Kind == EvidenceTool && it.Success {
			namedSuccess = append(namedSuccess, it.Summary)
		}
	}
	if len(namedSuccess) != 2 {
		t.Fatalf("named success items = %v, want one per distinct tool", namedSuccess)
	}
	if namedSuccess[0] != "ask_user succeeded and a user answer was received" || namedSuccess[1] != "write_file succeeded (2 calls)" {
		t.Fatalf("summaries = %v", namedSuccess)
	}
	// The earlier failure is still visible so the recovery arc reads.
	var sawFailure bool
	for _, it := range items {
		if it.Kind == EvidenceToolFailure && it.Source == "ask_user" {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatal("the recovered ask_user failure should still appear as evidence")
	}
}
