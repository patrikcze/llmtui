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
	if namedSuccess[0] != "ask_user succeeded" || namedSuccess[1] != "write_file succeeded (2 calls)" {
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
