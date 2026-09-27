package agent

import (
	"testing"
	"time"
)

// TestExecutedToolCallsIsTheSingleBudgetDefinition pins the one definition
// of tool-budget usage (audit P3-3): executed calls only, excluding
// ask_user; legacy records without a Status count as executed.
func TestExecutedToolCallsIsTheSingleBudgetDefinition(t *testing.T) {
	records := []ToolCallRecord{
		{Name: "read_file", Status: ActionExecuted, Succeeded: true},
		{Name: "read_file", Status: ActionExecuted, Succeeded: false},
		{Name: "list_dir"}, // legacy, pre-Status record
		{Name: "ask_user", Status: ActionExecuted, Succeeded: true},
		{Name: "read_file", Status: ActionRejected},
		{Name: "read_file", Status: ActionBlocked},
		{Name: "write_file", Status: ActionDenied},
		{Name: "grep", Status: ActionUnknown},
	}
	if got := ExecutedToolCalls(records); got != 3 {
		t.Fatalf("ExecutedToolCalls = %d, want 3", got)
	}
}

func TestCompleteExecutionCountsOnlyExecutedToolCalls(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run, err := NewRun("count", "read the notes", DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("read the notes", nil, now); err != nil {
		t.Fatal(err)
	}
	execution := ExecutionResult{Summary: "done", ToolCalls: []ToolCallRecord{
		{Name: "read_file", Status: ActionRejected},
		{Name: "read_file", Status: ActionExecuted, Succeeded: true},
		{Name: "read_file", Status: ActionBlocked},
	}}
	if err := run.CompleteExecution(execution, now); err != nil {
		t.Fatal(err)
	}
	if run.ToolCalls != 1 {
		t.Fatalf("run.ToolCalls = %d, want 1", run.ToolCalls)
	}
}
