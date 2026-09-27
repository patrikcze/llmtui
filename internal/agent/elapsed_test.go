package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// pausedExecutorRun returns a run that entered its first executor cycle at
// start and paused for the human at start+active.
func pausedExecutorRun(t *testing.T, start time.Time, active time.Duration) *AgentRun {
	t.Helper()
	limits := DefaultLimits()
	limits.MaxElapsed = 30 * time.Minute
	run, err := NewRun("paused-run", "do the task", limits, start)
	if err != nil {
		t.Fatal(err)
	}
	run.Criteria = []Criterion{{ID: "c1", Text: "done", Status: CriterionPending}}
	run.Stage = StageTrigger
	if err := run.BeginCycle("do the task", nil, start); err != nil {
		t.Fatal(err)
	}
	if err := run.WaitForUserInput("Which file?", start.Add(active)); err != nil {
		t.Fatal(err)
	}
	return run
}

// TestElapsedExcludesHumanWait is the audit P3-9 regression: time spent
// waiting for the human no longer counts toward agent.max_elapsed.
func TestElapsedExcludesHumanWait(t *testing.T) {
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := pausedExecutorRun(t, start, 10*time.Minute)

	answeredAt := start.Add(10*time.Minute + 2*time.Hour)
	if got := run.Elapsed(answeredAt); got != 10*time.Minute {
		t.Fatalf("elapsed during a pause = %s, want 10m (the 2h wait excluded)", got)
	}
	if err := run.ContinueExecutorWithUserInput(answeredAt); err != nil {
		t.Fatal(err)
	}
	if run.PausedFor != 2*time.Hour || !run.PausedAt.IsZero() {
		t.Fatalf("PausedFor=%s PausedAt=%v, want 2h and a closed pause", run.PausedFor, run.PausedAt)
	}
	later := answeredAt.Add(5 * time.Minute)
	if got := run.Elapsed(later); got != 15*time.Minute {
		t.Fatalf("elapsed after the answer = %s, want 15m", got)
	}
	if got := run.RemainingElapsed(later); got != 15*time.Minute {
		t.Fatalf("remaining = %s, want 15m", got)
	}
	// Active time still exhausts the budget.
	if got := run.Elapsed(answeredAt.Add(20 * time.Minute)); got < run.Limits.MaxElapsed {
		t.Fatalf("elapsed = %s, want the budget spent by 30m of active time", got)
	}
}

func TestResumeAfterLongInputWaitIsNotBudgetExhausted(t *testing.T) {
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := pausedExecutorRun(t, start, 10*time.Minute)
	if err := run.Resume("continue with the answer", start.Add(3*time.Hour)); err != nil {
		t.Fatalf("resume after a 3h wait: %v, want success (only 10m active)", err)
	}
	if run.Status != DecisionRunning || run.PausedFor != 3*time.Hour-10*time.Minute {
		t.Fatalf("status=%s PausedFor=%s", run.Status, run.PausedFor)
	}
}

func TestResumeStillEnforcesActiveElapsed(t *testing.T) {
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := pausedExecutorRun(t, start, 31*time.Minute)
	err := run.Resume("continue", start.Add(2*time.Hour))
	if !errors.Is(err, ErrBudgetExhausted) || !strings.Contains(err.Error(), "maximum elapsed time") {
		t.Fatalf("resume after 31m of active time = %v, want ErrBudgetExhausted", err)
	}
}

func TestParkedTimeStillCountsTowardElapsed(t *testing.T) {
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run, err := NewRun("parked-run", "do the task", DefaultLimits(), start)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Terminate(DecisionParked, "stopped", start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := run.Elapsed(start.Add(time.Hour)); got != time.Hour {
		t.Fatalf("elapsed = %s, want 1h: only needs_user_input waits are excluded", got)
	}
}

func TestFileStoreRoundTripsPauseAccounting(t *testing.T) {
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	run := pausedExecutorRun(t, start, 10*time.Minute)
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.PausedAt.Equal(run.PausedAt) {
		t.Fatalf("PausedAt = %v, want %v", loaded.PausedAt, run.PausedAt)
	}
	if got := loaded.Elapsed(start.Add(5 * time.Hour)); got != 10*time.Minute {
		t.Fatalf("elapsed after reload = %s, want 10m", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, run.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	corrupt := strings.Replace(string(data), `"limits"`, `"paused_for":-1,"limits"`, 1)
	if err := os.WriteFile(filepath.Join(dir, run.ID+".json"), []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), run.ID); !errors.Is(err, ErrCorruptRun) {
		t.Fatalf("load with negative paused_for = %v, want ErrCorruptRun", err)
	}
}
