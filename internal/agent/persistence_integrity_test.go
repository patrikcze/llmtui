package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func executorStageRun(t *testing.T, now time.Time) *AgentRun {
	t.Helper()
	run, err := NewRun("persist-integrity", "update the report and run the checks", DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginContract(now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteContract([]string{"report updated", "checks pass"}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("update the report", nil, now); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestAbandonCycleCommitsPartialExecution is the regression for audit P2-1:
// a run stopped inside the executor (no-progress, budget, provider failure)
// used to persist only its status, losing the cycle's receipts and changed
// files. AbandonCycle must commit them as a partial record, feed the
// evidence ledger and tool count, and end the run with the given decision.
func TestAbandonCycleCommitsPartialExecution(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := executorStageRun(t, now)
	live := ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "write_file", Detail: "report.md", Succeeded: true, Status: ActionExecuted},
			{Name: "list_dir", Succeeded: false, ErrorKind: ErrorToolExecution, Status: ActionBlocked},
		},
		ChangedFiles: []string{"report.md"},
	}
	if err := run.AbandonCycle(live, DecisionNoProgress, "repeated tool call blocked", now); err != nil {
		t.Fatalf("AbandonCycle: %v", err)
	}
	if run.Status != DecisionNoProgress || run.StopReason != "repeated tool call blocked" {
		t.Fatalf("status=%s reason=%q", run.Status, run.StopReason)
	}
	execution := run.LatestCycle().Execution
	if execution == nil || !execution.Partial || len(execution.ToolCalls) != 2 || execution.ChangedFiles[0] != "report.md" {
		t.Fatalf("partial execution = %+v", execution)
	}
	if execution.Objective != "update the report" {
		t.Errorf("objective = %q, want the cycle objective", execution.Objective)
	}
	// Budget usage counts executed calls only (ExecutedToolCalls): the
	// blocked list_dir is recorded but was never run.
	if run.ToolCalls != 1 {
		t.Errorf("tool calls = %d, want 1 executed", run.ToolCalls)
	}
	var sawFile bool
	for _, item := range run.Evidence {
		sawFile = sawFile || (item.Kind == EvidenceFile && item.Source == "report.md")
	}
	if !sawFile {
		t.Errorf("evidence ledger = %+v, want the changed file", run.Evidence)
	}
	if kind := run.Events[len(run.Events)-2].Kind; kind != "execution_abandoned" {
		t.Errorf("penultimate event = %q, want execution_abandoned", kind)
	}

	// The stored record must not alias the caller's live, still-growing
	// execution: later mutation of the caller's slices cannot rewrite it.
	live.ToolCalls[0].Name = "mutated"
	live.ChangedFiles[0] = "mutated"
	if execution.ToolCalls[0].Name != "write_file" || execution.ChangedFiles[0] != "report.md" {
		t.Fatal("partial execution aliases the caller's slices")
	}
}

func TestAbandonCycleRejectsInvalidTransitions(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := executorStageRun(t, now)
	if err := run.AbandonCycle(ExecutionResult{}, DecisionContinue, "not terminal", now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("non-terminal decision err = %v, want ErrInvalidTransition", err)
	}
	if run.Status != DecisionRunning || run.LatestCycle().Execution != nil {
		t.Fatal("a rejected AbandonCycle changed the run")
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "done"}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.AbandonCycle(ExecutionResult{}, DecisionFailed, "verifier stage", now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("verifier-stage err = %v, want ErrInvalidTransition", err)
	}
}

// TestCheckpointExecutionIsReplacedByCompleteExecution covers the live
// pause: the checkpoint is a partial record without accounting, and the
// cycle's eventual CompleteExecution replaces it and counts tool calls once.
func TestCheckpointExecutionIsReplacedByCompleteExecution(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := executorStageRun(t, now)
	calls := []ToolCallRecord{{Name: "read_file", Detail: "a.md", Succeeded: true, Status: ActionExecuted}}
	if err := run.CheckpointExecution(ExecutionResult{ToolCalls: calls}, now); err != nil {
		t.Fatal(err)
	}
	if got := run.LatestCycle().Execution; got == nil || !got.Partial || run.ToolCalls != 0 || run.Stage != StageExecutor {
		t.Fatalf("checkpoint execution=%+v toolCalls=%d stage=%s", got, run.ToolCalls, run.Stage)
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "done", ToolCalls: calls}, now); err != nil {
		t.Fatal(err)
	}
	if got := run.LatestCycle().Execution; got.Partial || run.ToolCalls != 1 {
		t.Fatalf("completed execution=%+v toolCalls=%d, want a non-partial record counted once", got, run.ToolCalls)
	}
}

// TestResumeMarksPartialEpisodeInterrupted keeps Resume's contract for a
// cycle paused with a checkpointed partial execution: it is unfinished work,
// never an ordinary completed checkpoint.
func TestResumeMarksPartialEpisodeInterrupted(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := executorStageRun(t, now)
	run.LatestCycle().Episode = &EpisodeCheckpoint{PolicyVersion: 1, EnabledAtStart: true}
	if err := run.CheckpointExecution(ExecutionResult{}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WaitForUserInput("which file?", now); err != nil {
		t.Fatal(err)
	}
	if err := run.Resume("continue", now); err != nil {
		t.Fatal(err)
	}
	if !run.LatestCycle().Episode.Interrupted {
		t.Fatal("resumed partial episode was not marked interrupted")
	}
}

// verboseRun builds a terminal multi-cycle run whose summaries are as large
// as the per-field bounds allow — the shape that pushed a real record past
// the default 64 KiB store limit (audit P2-2).
func verboseRun(t *testing.T, cycles int) *AgentRun {
	t.Helper()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	limits := DefaultLimits()
	limits.MaxCycles = cycles
	limits.MaxTokens = 10_000_000
	limits.MaxRepeatedFailures = cycles + 1
	run, err := NewRun("verbose-run", "summarize the logs and write a report", limits, now)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.BeginContract(now)
	_ = run.CompleteContract([]string{"logs summarized", "report written"}, now)
	long := strings.Repeat("The executor explains its findings in careful detail. ", 80)
	for c := 1; c <= cycles && run.Status == DecisionRunning; c++ {
		objective := run.Request
		if c > 1 {
			objective = run.Objective
		}
		if err := run.BeginCycle(objective, nil, now); err != nil {
			t.Fatalf("cycle %d: %v", c, err)
		}
		calls := make([]ToolCallRecord, 8)
		for i := range calls {
			calls[i] = ToolCallRecord{Name: "read_file", Detail: "logs/app.log", Succeeded: true, Summary: "completed", Status: ActionExecuted}
		}
		_ = run.CompleteExecution(ExecutionResult{Summary: long, ToolCalls: calls, NewEvidence: true}, now)
		_ = run.CompleteVerification(VerificationResult{
			Verdict: VerificationFailed, Summary: long, Retryable: true,
			RecommendedNext: "continue with step " + strings.Repeat("x", c) + long[:300], NewEvidence: true,
		}, now)
		_ = run.WriteMemory(now)
		_ = run.ApplyStop(Decide(run, now), now)
	}
	return run
}

// TestFileStoreCompactsOversizedTerminalRecord is the regression for audit
// P2-2: a terminal record larger than the store bound used to be rejected,
// leaving the previous (still "running") snapshot on disk for /agent resume
// to restart. The terminal status must now always be stored, compacted.
func TestFileStoreCompactsOversizedTerminalRecord(t *testing.T) {
	run := verboseRun(t, 6)
	if run.Status == DecisionRunning {
		t.Fatalf("fixture run is still running: %+v", run.Status)
	}
	full, err := encodePersistedRun(run, true)
	if err != nil {
		t.Fatal(err)
	}
	const limit = 64 * 1024
	if len(full) <= limit {
		t.Fatalf("fixture encodes to %d bytes; it must exceed %d to exercise compaction", len(full), limit)
	}

	store := NewFileStore(t.TempDir(), limit, 4)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatalf("Save oversized terminal run: %v", err)
	}
	loaded, err := store.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != run.Status || loaded.StopReason != run.StopReason || !loaded.Compacted {
		t.Fatalf("loaded status=%s reason=%q compacted=%v", loaded.Status, loaded.StopReason, loaded.Compacted)
	}
	if len(loaded.Cycles) != len(run.Cycles) || len(loaded.Criteria) != len(run.Criteria) {
		t.Fatalf("compaction dropped cycles or criteria: %d/%d cycles", len(loaded.Cycles), len(run.Cycles))
	}
	last := loaded.Cycles[len(loaded.Cycles)-1]
	if last.Execution.Summary != run.Cycles[len(run.Cycles)-1].Execution.Summary {
		t.Error("the newest cycle's summary was shortened")
	}
	if first := loaded.Cycles[0]; len(first.Execution.Summary) > compactedSummaryBytes+len("…") {
		t.Errorf("oldest cycle summary is %d bytes, want it compacted", len(first.Execution.Summary))
	}
	// Compaction touches only the persisted copy.
	if run.Compacted || len(run.Cycles[0].Execution.Summary) <= compactedSummaryBytes {
		t.Fatal("compaction mutated the live run")
	}
}

func TestFileStoreStillRejectsRecordThatCannotBeCompacted(t *testing.T) {
	run := verboseRun(t, 3)
	store := NewFileStore(t.TempDir(), 512, 4)
	if err := store.Save(context.Background(), run); !errors.Is(err, ErrCorruptRun) {
		t.Fatalf("Save err = %v, want ErrCorruptRun", err)
	}
}

// TestStoresRefuseOlderRevision is the regression for audit P3-1: save
// ordering relied on UpdatedAt, which several mutations do not advance, so a
// delayed older snapshot with an equal timestamp could overwrite a newer one.
func TestStoresRefuseOlderRevision(t *testing.T) {
	for name, store := range map[string]Store{
		"memory": NewMemoryStore(),
		"file":   NewFileStore(t.TempDir(), 64*1024, 4),
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
			run := executorStageRun(t, now)
			newer := *run
			newer.Revision = 5
			newer.Status = DecisionNoProgress
			older := *run
			older.Revision = 4
			if err := store.Save(context.Background(), &newer); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(context.Background(), &older); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Revision != 5 || loaded.Status != DecisionNoProgress {
				t.Fatalf("loaded revision=%d status=%s, want the newer snapshot", loaded.Revision, loaded.Status)
			}
		})
	}
}

// TestSavedIsNewerFallsBackToUpdatedAt keeps records written before
// Revision existed ordered exactly as before.
func TestSavedIsNewerFallsBackToUpdatedAt(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	saved := &AgentRun{UpdatedAt: now.Add(time.Second)}
	incoming := &AgentRun{UpdatedAt: now, Revision: 9}
	if !savedIsNewer(saved, incoming) {
		t.Error("legacy saved record with later UpdatedAt was not treated as newer")
	}
	if savedIsNewer(&AgentRun{UpdatedAt: now, Revision: 3}, &AgentRun{UpdatedAt: now, Revision: 3}) {
		t.Error("an equal revision must be replaceable")
	}
}

// TestDecodeRunAcceptsRecordWithoutIntegrityFields guards additive schema
// compatibility: a v1 record from before partial/revision/compacted existed
// still loads, with every new field at its zero value.
func TestDecodeRunAcceptsRecordWithoutIntegrityFields(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := executorStageRun(t, now)
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	delete(raw, "revision")
	delete(raw, "compacted")
	data, _ = json.Marshal(raw)
	loaded, err := decodeRun(data)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 0 || loaded.Compacted {
		t.Fatalf("loaded = %+v", loaded)
	}
}
