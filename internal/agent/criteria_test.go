package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPinCriteriaOnceBoundedAndStableIDs(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	texts := make([]string, MaxCriteria+3)
	for i := range texts {
		texts[i] = fmt.Sprintf("criterion %d", i+1)
	}
	run.PinCriteria(texts)
	if len(run.Criteria) != MaxCriteria {
		t.Fatalf("criteria = %d, want bounded to %d", len(run.Criteria), MaxCriteria)
	}
	if run.Criteria[0].ID != "c1" || run.Criteria[MaxCriteria-1].ID != fmt.Sprintf("c%d", MaxCriteria) {
		t.Fatalf("criterion IDs = %q..%q", run.Criteria[0].ID, run.Criteria[MaxCriteria-1].ID)
	}
	for _, criterion := range run.Criteria {
		if criterion.Status != CriterionPending {
			t.Fatalf("initial status = %q, want pending", criterion.Status)
		}
	}
	run.PinCriteria([]string{"a rewritten goalpost"})
	if len(run.Criteria) != MaxCriteria || run.Criteria[0].Text != "criterion 1" {
		t.Fatal("a second PinCriteria call must be ignored")
	}
}

func TestApplyCriteriaUpdatesIgnoresUnknownAndInvalid(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"first", "second"})
	run.ApplyCriteriaUpdates([]CriterionUpdate{
		{ID: "c1", Status: CriterionSatisfied, Note: "done"},
		{ID: "c99", Status: CriterionSatisfied},         // unknown ID
		{ID: "c2", Status: CriterionStatus("probably")}, // invalid status
	}, 3)
	if run.Criteria[0].Status != CriterionSatisfied || run.Criteria[0].UpdatedCycle != 3 {
		t.Fatalf("c1 = %+v", run.Criteria[0])
	}
	if run.Criteria[1].Status != CriterionPending {
		t.Fatalf("c2 = %+v, want untouched by an invalid status", run.Criteria[1])
	}
	if len(run.Criteria) != 2 {
		t.Fatal("updates must not add criteria")
	}
}

func TestCriteriaDriveDoneAndContinueDecisions(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	stop := completeCycle(t, run, now, "start work", VerificationResult{
		Verdict: VerificationPassed, Summary: "step one complete", Confidence: 0.9,
		ProposedCriteria: []string{"parse the input", "write the report"},
		CriteriaUpdates:  []CriterionUpdate{{ID: "c1", Status: CriterionSatisfied}},
	})
	if stop.Decision != DecisionContinue {
		t.Fatalf("decision = %q, want continue while c2 is unresolved", stop.Decision)
	}
	if stop.NextObjective == "" || !strings.Contains(stop.NextObjective, "write the report") {
		t.Fatalf("next objective = %q, want it to target the unresolved criterion", stop.NextObjective)
	}
	if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	stop = completeCycle(t, run, now.Add(6*time.Second), stop.NextObjective, VerificationResult{
		Verdict: VerificationPassed, Summary: "report written", Confidence: 0.9,
		CriteriaUpdates: []CriterionUpdate{{ID: "c2", Status: CriterionSatisfied}},
	})
	if stop.Decision != DecisionDone {
		t.Fatalf("decision = %q, want done once every pinned criterion is resolved", stop.Decision)
	}
	if len(run.Criteria) != 2 || run.Criteria[0].ID != "c1" {
		t.Fatalf("criteria = %+v, want the pinned set unchanged across cycles", run.Criteria)
	}
}

// A passed verdict may not end the run while pinned criteria are unresolved,
// even if the verifier reports nothing in remaining_criteria — the
// controller's own ledger is authoritative.
func TestVerifierCannotEndRunWithUnresolvedPinnedCriteria(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	stop := completeCycle(t, run, now, "start work", VerificationResult{
		Verdict: VerificationPassed, Summary: "claims everything is complete", Confidence: 1,
		ProposedCriteria: []string{"gather data", "produce summary"},
	})
	if stop.Decision != DecisionContinue {
		t.Fatalf("decision = %q, want continue while both criteria are pending", stop.Decision)
	}
}

func TestCriteriaFailureKeyIsPhrasingImmune(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxRepeatedFailures = 2
	run, now := newTestRun(t, limits)
	stop := completeCycle(t, run, now, "objective one", VerificationResult{
		Verdict: VerificationFailed, Summary: "the report is missing entirely", Retryable: true,
		RecommendedNext: "produce the report using tool output", StrategyChanged: true,
		ProposedCriteria: []string{"gather data", "produce report"},
		CriteriaUpdates:  []CriterionUpdate{{ID: "c1", Status: CriterionSatisfied}},
	})
	if stop.Decision != DecisionRetry || run.RepeatedFailures != 1 {
		t.Fatalf("stop = %+v repeated = %d", stop, run.RepeatedFailures)
	}
	if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Same unresolved set (c2), completely different phrasing everywhere the
	// legacy text-based key looked: the repeat must still be detected.
	stop = completeCycle(t, run, now.Add(6*time.Second), "objective two", VerificationResult{
		Verdict: VerificationFailed, Summary: "no summary document was generated", Retryable: true,
		RecommendedNext: "try synthesizing the summary from cached results", StrategyChanged: true,
	})
	if stop.Decision != DecisionFailed || run.RepeatedFailures != 2 {
		t.Fatalf("stop = %+v repeated = %d, want phrasing-immune repeat detection", stop, run.RepeatedFailures)
	}
}

func TestEvidenceLedgerBoundedAndCollected(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("work", nil, now); err != nil {
		t.Fatal(err)
	}
	exec := ExecutionResult{
		Summary: "ran tools and tests",
		ToolCalls: []ToolCallRecord{
			{Name: "read_file", Succeeded: true},
			{Name: "write_file", Succeeded: true},
		},
		TestsRun:     []TestResult{{Name: "go test ./...", Passed: true, Summary: "command passed"}},
		ChangedFiles: []string{"main.go"},
		NewEvidence:  true,
	}
	if err := run.CompleteExecution(exec, now); err != nil {
		t.Fatal(err)
	}
	wantKinds := map[EvidenceKind]bool{EvidenceTool: false, EvidenceTest: false, EvidenceFile: false}
	for _, item := range run.Evidence {
		wantKinds[item.Kind] = true
	}
	for kind, seen := range wantKinds {
		if !seen {
			t.Fatalf("evidence ledger missing kind %q: %+v", kind, run.Evidence)
		}
	}

	var flood []EvidenceItem
	for i := 0; i < MaxEvidence+40; i++ {
		flood = append(flood, EvidenceItem{Cycle: 1, Kind: EvidenceTool, Source: fmt.Sprintf("item-%d", i), Success: true})
	}
	run.AppendEvidence(flood)
	if len(run.Evidence) != MaxEvidence {
		t.Fatalf("evidence = %d, want bounded to %d", len(run.Evidence), MaxEvidence)
	}
	if run.Evidence[len(run.Evidence)-1].Source != fmt.Sprintf("item-%d", MaxEvidence+39) {
		t.Fatal("bounding must keep the most recent evidence")
	}
}

// The verifier's new_evidence claim is clamped by the executor's mechanical
// record: no observed tool activity means no new evidence, so a retry that
// leans on that claim alone is rejected.
func TestVerifierNewEvidenceClaimClampedToMechanicalRecord(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	if err := run.BeginCycle("work", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteExecution(ExecutionResult{Summary: "prose only, no tools"}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteVerification(VerificationResult{
		Verdict: VerificationFailed, Summary: "incomplete", Retryable: true, NewEvidence: true,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err := run.WriteMemory(now); err != nil {
		t.Fatal(err)
	}
	cycle := run.LatestCycle()
	if cycle.Verification.NewEvidence {
		t.Fatal("verifier new_evidence claim must be clamped when execution produced none")
	}
	if got := Decide(run, now).Decision; got != DecisionFailed {
		t.Fatalf("decision = %q, want failed: retry justified only by a clamped claim", got)
	}
}

func TestEvaluateDeterministic(t *testing.T) {
	cases := []struct {
		name       string
		exec       ExecutionResult
		conclusive bool
		verdict    VerificationVerdict
		retryable  bool
	}{
		{"clean execution is not conclusive", ExecutionResult{Summary: "done", ToolCalls: []ToolCallRecord{{Name: "read_file", Succeeded: true}}}, false, "", false},
		{"failed test", ExecutionResult{TestsRun: []TestResult{{Name: "go test", Passed: false}}}, true, VerificationFailed, true},
		{"failed tool", ExecutionResult{ToolCalls: []ToolCallRecord{{Name: "web_fetch", Succeeded: false, ErrorKind: ErrorToolExecution}}}, true, VerificationFailed, true},
		{"denied tool", ExecutionResult{ToolCalls: []ToolCallRecord{{Name: "write_file", Succeeded: false, ErrorKind: ErrorPermissionDenied}}}, true, VerificationBlocked, false},
		{"safety error", ExecutionResult{Errors: []RunError{{Kind: ErrorSafety}}}, true, VerificationBlocked, false},
		{"truncated reply", ExecutionResult{Errors: []RunError{{Kind: ErrorTruncated}}}, true, VerificationFailed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, conclusive := EvaluateDeterministic(tc.exec)
			if conclusive != tc.conclusive {
				t.Fatalf("conclusive = %v, want %v", conclusive, tc.conclusive)
			}
			if conclusive && (result.Verdict != tc.verdict || result.Retryable != tc.retryable) {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestMechanicallyComplete(t *testing.T) {
	ok := ExecutionResult{
		Summary:   "listed and reported",
		ToolCalls: []ToolCallRecord{{Name: "list_dir", Succeeded: true}},
		TestsRun:  []TestResult{{Name: "go test", Passed: true}},
	}
	if !MechanicallyComplete(ok) {
		t.Fatal("clean tool cycle should be mechanically complete")
	}
	for name, exec := range map[string]ExecutionResult{
		"no tools ran":     {Summary: "prose only"},
		"a tool failed":    {Summary: "s", ToolCalls: []ToolCallRecord{{Succeeded: false}}},
		"a test failed":    {Summary: "s", ToolCalls: []ToolCallRecord{{Succeeded: true}}, TestsRun: []TestResult{{Passed: false}}},
		"errors present":   {Summary: "s", ToolCalls: []ToolCallRecord{{Succeeded: true}}, Errors: []RunError{{Kind: ErrorProvider}}},
		"needs user input": {Summary: "s", ToolCalls: []ToolCallRecord{{Succeeded: true}}, NeedsUserInput: true},
		"empty summary":    {ToolCalls: []ToolCallRecord{{Succeeded: true}}},
	} {
		if MechanicallyComplete(exec) {
			t.Fatalf("%s: should not be mechanically complete", name)
		}
	}
}

func TestVerificationStateSurvivesPersistenceRoundtrip(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	stop := completeCycle(t, run, now, "start", VerificationResult{
		Verdict: VerificationFailed, Summary: "second criterion unresolved", Retryable: true,
		RecommendedNext: "produce the report", StrategyChanged: true,
		ProposedCriteria: []string{"gather data", "produce report"},
		CriteriaUpdates:  []CriterionUpdate{{ID: "c1", Status: CriterionSatisfied, Note: "data gathered"}},
	})
	if err := run.ApplyStop(stop, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(t.TempDir(), 64*1024, 4)
	if err := store.Save(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Criteria) != 2 || loaded.Criteria[0].Status != CriterionSatisfied || loaded.Criteria[1].Status != CriterionPending {
		t.Fatalf("loaded criteria = %+v", loaded.Criteria)
	}
	if len(loaded.Evidence) == 0 {
		t.Fatal("loaded run lost its evidence ledger")
	}
	unresolved := loaded.UnresolvedCriteria()
	if len(unresolved) != 1 || unresolved[0].ID != "c2" {
		t.Fatalf("unresolved after reload = %+v", unresolved)
	}
}
