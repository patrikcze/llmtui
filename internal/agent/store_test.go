package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMemoryWriteAndReload(t *testing.T) {
	stores := map[string]Store{
		"memory": NewMemoryStore(),
		"file":   NewFileStore(t.TempDir(), 64*1024, 4),
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			run, now := newTestRun(t, DefaultLimits())
			stop := completeCycle(t, run, now, "bounded objective", VerificationResult{Verdict: VerificationPassed, Summary: "passed"})
			if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(context.Background(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.ID != run.ID || loaded.Status != DecisionDone || len(loaded.Memory) != 1 {
				t.Fatalf("loaded = %+v", loaded)
			}
		})
	}
}

func TestFileStoreCorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	run, now := newTestRun(t, DefaultLimits())
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "newer.json")
	if err := os.WriteFile(corrupt, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := now.Add(time.Hour)
	if err := os.Chtimes(corrupt, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "newer"); !errors.Is(err, ErrCorruptRun) {
		t.Fatalf("Load corrupt error = %v", err)
	}
	latest, err := store.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != run.ID {
		t.Fatalf("latest ID = %q, want %q", latest.ID, run.ID)
	}
}

// TestFileStoreLoadsPreReceiptRunWithoutInventingAttribution is the Phase 1
// backward-compatibility acceptance check: a run persisted before
// ToolCallRecord.Status and RunError.Resource existed must still load, and
// its recovery/completion evaluation must not silently gain stronger proof
// than what was originally recorded — see receipts.go and recovered.go.
func TestFileStoreLoadsPreReceiptRunWithoutInventingAttribution(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	// Hand-authored to mirror exactly what schema v1 produced before this
	// package's Status/Resource fields existed: no "status" on tool_calls,
	// no "resource" on errors.
	legacy := `{
		"version": 1,
		"id": "legacy-run",
		"request": "read two files",
		"status": "running",
		"limits": {"max_cycles": 8, "max_tool_calls": 32, "max_tokens": 100000, "max_elapsed": 1800000000000, "max_repeated_failures": 3},
		"cycles": [{
			"number": 1,
			"objective": "read two files",
			"execution": {
				"objective": "read two files",
				"tool_calls": [
					{"name": "read_file", "detail": "missing.md", "succeeded": false, "error_kind": "tool_execution"},
					{"name": "read_file", "detail": "other.md", "succeeded": true}
				],
				"errors": [
					{"kind": "tool_execution", "op": "read_file", "message": "read missing.md: not found"}
				]
			}
		}]
	}`
	if err := os.WriteFile(filepath.Join(dir, "legacy-run.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), "legacy-run")
	if err != nil {
		t.Fatal(err)
	}
	execution := *loaded.Cycles[0].Execution
	if got := execution.ToolCalls[0].Status; got != "" {
		t.Fatalf("legacy record Status = %q, want empty (unknown), not inferred", got)
	}
	if got := execution.Errors[0].Resource; got != "" {
		t.Fatalf("legacy record Resource = %q, want empty (unknown), not inferred", got)
	}
	// No resource identity was ever recorded for this error, so it must not
	// be treated as recovered by the unrelated successful read of other.md.
	PruneRecoveredToolErrors(&execution)
	if len(execution.Errors) != 1 {
		t.Fatalf("errors after prune = %+v, want the legacy failure to stay unresolved", execution.Errors)
	}
}

// TestStoreRoundTripsValidEpisodeCheckpoint proves an ordinary, well-formed
// EpisodeCheckpoint survives a save/load cycle unchanged — the additive
// field's happy path, not just the rejection path covered below.
func TestStoreRoundTripsValidEpisodeCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("finish the requested change", nil, now); err != nil {
		t.Fatal(err)
	}
	run.LatestCycle().Episode = &EpisodeCheckpoint{
		PolicyVersion: 1, EnabledAtStart: true, ExecutorRequests: 5, NoProgressNudges: 1,
		LastYieldReason: ReasonMechanicalObligation, UnresolvedCriterionIDs: []string{"c1"}, Revision: 2,
	}
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	ep := loaded.Cycles[0].Episode
	if ep == nil || ep.ExecutorRequests != 5 || ep.Revision != 2 || ep.LastYieldReason != ReasonMechanicalObligation {
		t.Fatalf("loaded checkpoint = %+v, want it preserved exactly", ep)
	}
}

// TestStoreRejectsCorruptEpisodeCheckpoint is harness plan §16's checkpoint
// validation requirement exercised through the real file-backed store, not
// just validateEpisodeCheckpoint's own unit test: a hand-authored record
// with a malformed checkpoint (negative counters here — a value this
// package's own writer could never produce) must fail to load instead of
// being silently accepted.
func TestStoreRejectsCorruptEpisodeCheckpoint(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	corrupt := `{
		"version": 1,
		"id": "corrupt-checkpoint-run",
		"request": "long task",
		"status": "running",
		"limits": {"max_cycles": 8, "max_tool_calls": 32, "max_tokens": 100000, "max_elapsed": 1800000000000, "max_repeated_failures": 3},
		"cycles": [{
			"number": 1,
			"objective": "long task",
			"episode": {"policy_version": 1, "executor_requests": -3}
		}]
	}`
	if err := os.WriteFile(filepath.Join(dir, "corrupt-checkpoint-run.json"), []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "corrupt-checkpoint-run"); !errors.Is(err, ErrCorruptRun) {
		t.Fatalf("Load error = %v, want ErrCorruptRun for a negative checkpoint counter", err)
	}
}

func TestFileStorePreservesAndRedactsRunStartContext(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	run, _ := newTestRun(t, DefaultLimits())
	run.StartContextCaptured = true
	run.StartSummary = "deployment blue api_key=super-secret-value"
	run.StartTurns = []ContextTurn{{Role: "user", Content: "use token=another-secret-value"}}
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.StartContextCaptured || !strings.Contains(loaded.StartSummary, "deployment blue") {
		t.Fatalf("loaded start context = %+v", loaded)
	}
	if strings.Contains(loaded.StartSummary, "super-secret-value") ||
		len(loaded.StartTurns) != 1 || strings.Contains(loaded.StartTurns[0].Content, "another-secret-value") {
		t.Fatalf("start context was not redacted: summary=%q turns=%+v", loaded.StartSummary, loaded.StartTurns)
	}
}

func TestStoreRoundTripsCriterionAssessmentAndDropsRedactedClaim(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginContract(now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteContractWithAssessments(
		[]string{"read report.md"},
		map[int]CriterionAssessmentSpec{
			0: {Version: 1, Proposition: "the report observation supports the criterion", EvidenceKind: CriterionAssessmentLocalRead, Target: "report.md"},
		},
		now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Criteria[0].Assessment == nil || loaded.Criteria[0].Assessment.Proposition != "the report observation supports the criterion" {
		t.Fatalf("round-tripped assessment = %+v", loaded.Criteria[0].Assessment)
	}

	secretRun, now := newTestRun(t, DefaultLimits())
	if err := secretRun.BeginContract(now); err != nil {
		t.Fatal(err)
	}
	if err := secretRun.CompleteContractWithAssessments(
		[]string{"read report.md"},
		map[int]CriterionAssessmentSpec{
			0: {Version: 1, Proposition: "token=super-secret-value", EvidenceKind: CriterionAssessmentLocalRead, Target: "report.md"},
		},
		now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), secretRun); err != nil {
		t.Fatal(err)
	}
	secretLoaded, err := store.Load(context.Background(), secretRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secretLoaded.Criteria[0].Assessment != nil {
		t.Fatalf("redaction changed claim instead of dropping it: %+v", secretLoaded.Criteria[0].Assessment)
	}
	if secretRun.Criteria[0].Assessment == nil {
		t.Fatal("persistence redaction mutated live run")
	}
}

func TestDecodeRunStripsInvalidOptionalAssessment(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginContract(now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteContract([]string{"read report.md"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	run.Criteria[0].Assessment = &CriterionAssessmentSpec{
		Version: 99, Proposition: "unsupported", EvidenceKind: CriterionAssessmentReceipts,
	}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := decodeRun(data)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Criteria[0].Assessment != nil {
		t.Fatalf("invalid assessment survived load: %+v", loaded.Criteria[0].Assessment)
	}
}

func TestFileStoreAtomicPermissionsAndBounds(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 1)
	run, now := newTestRun(t, DefaultLimits())
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, run.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
	run2, err := NewRun("run-2", "another", DefaultLimits(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), run2); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "run-2.json" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestFileStoreAtomicallyReplacesExistingRun(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	run, now := newTestRun(t, DefaultLimits())
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	run.Objective = "updated objective"
	run.UpdatedAt = now.Add(time.Second)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Objective != "updated objective" {
		t.Fatalf("objective = %q", loaded.Objective)
	}
}

func TestStoresRejectDelayedOlderSnapshot(t *testing.T) {
	stores := map[string]Store{
		"memory": NewMemoryStore(),
		"file":   NewFileStore(t.TempDir(), 64*1024, 4),
	}
	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			older, now := newTestRun(t, DefaultLimits())
			newer := *older
			newer.Objective = "newer state"
			newer.UpdatedAt = now.Add(time.Second)
			if err := store.Save(context.Background(), &newer); err != nil {
				t.Fatal(err)
			}
			if err := store.Save(context.Background(), older); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(context.Background(), older.ID)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Objective != "newer state" {
				t.Fatalf("delayed save replaced newer state: %+v", loaded)
			}
		})
	}
}

func TestStoreHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	run, _ := newTestRun(t, DefaultLimits())
	if err := NewMemoryStore().Save(ctx, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRedactsLikelySecrets(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir, 64*1024, 4)
	run, err := NewRun("secret-run", "use token=supersecret and Authorization: Bearer abcdefghijklmnop", DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run.Objective = "do not persist sk-abcdefghijklmnop"
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "secret-run.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"supersecret", "abcdefghijklmnop"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("persisted run leaked %q: %s", secret, data)
		}
	}
	loaded, err := store.Load(context.Background(), "secret-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded.Request, "[REDACTED]") {
		t.Fatalf("request was not redacted: %q", loaded.Request)
	}
}
