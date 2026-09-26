package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func int64Ptr(v int64) *int64 { return &v }

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

func TestValidateCriterionAssessmentSpec(t *testing.T) {
	tests := []struct {
		name string
		spec CriterionAssessmentSpec
		want bool
	}{
		{
			name: "receipts with exact command detail",
			spec: CriterionAssessmentSpec{Version: 1, Proposition: "the test receipt supports the criterion", EvidenceKind: CriterionAssessmentReceipts, Target: "go test ./..."},
			want: true,
		},
		{
			name: "local read workspace path",
			spec: CriterionAssessmentSpec{Version: 1, Proposition: "the file observation supports the criterion", EvidenceKind: CriterionAssessmentLocalRead, Target: "docs/report.md"},
			want: true,
		},
		{
			name: "unsupported version",
			spec: CriterionAssessmentSpec{Version: 2, Proposition: "claim", EvidenceKind: CriterionAssessmentReceipts},
		},
		{
			name: "empty proposition",
			spec: CriterionAssessmentSpec{Version: 1, EvidenceKind: CriterionAssessmentReceipts},
		},
		{
			name: "local read requires target",
			spec: CriterionAssessmentSpec{Version: 1, Proposition: "claim", EvidenceKind: CriterionAssessmentLocalRead},
		},
		{
			name: "absolute target",
			spec: CriterionAssessmentSpec{Version: 1, Proposition: "claim", EvidenceKind: CriterionAssessmentLocalRead, Target: "/etc/passwd"},
		},
		{
			name: "parent target",
			spec: CriterionAssessmentSpec{Version: 1, Proposition: "claim", EvidenceKind: CriterionAssessmentLocalRead, Target: "../report.md"},
		},
		{
			name: "glob target",
			spec: CriterionAssessmentSpec{Version: 1, Proposition: "claim", EvidenceKind: CriterionAssessmentLocalRead, Target: "docs/*.md"},
		},
		{
			name: "url target",
			spec: CriterionAssessmentSpec{Version: 1, Proposition: "claim", EvidenceKind: CriterionAssessmentReceipts, Target: "https://example.test"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateCriterionAssessmentSpec(tt.spec) == nil; got != tt.want {
				t.Fatalf("valid = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPinTypedCriteriaWithAssessmentsIsAtomicAndCopiesMetadata(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	assessment := CriterionAssessmentSpec{
		Version: 1, Proposition: "the receipt supports the criterion", EvidenceKind: CriterionAssessmentReceipts,
	}
	specs := []CriterionSpec{{Text: "produce the report", Kind: CriterionSemantic, Assessment: &assessment}}
	if err := run.PinTypedCriteriaWithAssessments(specs); err != nil {
		t.Fatal(err)
	}
	assessment.Proposition = "rewritten after pinning"
	if got := run.Criteria[0].Assessment.Proposition; got != "the receipt supports the criterion" {
		t.Fatalf("pinned proposition = %q, want immutable copy", got)
	}
	view := run.UnresolvedCriteria()
	view[0].Assessment.Proposition = "rewritten through a criterion snapshot"
	if got := run.Criteria[0].Assessment.Proposition; got != "the receipt supports the criterion" {
		t.Fatalf("snapshot mutation changed pinned proposition to %q", got)
	}

	bad, _ := newTestRun(t, DefaultLimits())
	badSpecs := []CriterionSpec{{Text: "produce the report", Kind: CriterionSemantic, Assessment: &CriterionAssessmentSpec{
		Version: 1, Proposition: "claim", EvidenceKind: CriterionAssessmentLocalRead,
	}}}
	if err := bad.PinTypedCriteriaWithAssessments(badSpecs); err == nil {
		t.Fatal("invalid assessment unexpectedly pinned")
	}
	if len(bad.Criteria) != 0 {
		t.Fatalf("criteria after rejected assessment = %+v, want empty", bad.Criteria)
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

// A verifier that both proposes and resolves every criterion on the
// establishing cycle (the same-turn path the verifier prompt now explicitly
// invites) must be able to end the run after a single cycle — the
// controller places no structural floor of "at least one extra
// verification-only cycle" once criteria are pinned. This is the mechanical
// half of closing the establish-cycle gap documented in
// .claude/tasks/agent-loop-establish-cycle-criteria-fix.md; whether a given
// model actually volunteers same-turn updates is a prompting/model
// question this test does not (and cannot) cover.
func TestCriteriaCanResolveOnEstablishingCycle(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	stop := completeCycle(t, run, now, "start work", VerificationResult{
		Verdict: VerificationPassed, Summary: "both criteria satisfied by this cycle's evidence", Confidence: 0.95,
		ProposedCriteria: []string{"parse the input", "write the report"},
		CriteriaUpdates: []CriterionUpdate{
			{ID: "c1", Status: CriterionSatisfied},
			{ID: "c2", Status: CriterionSatisfied},
		},
	})
	if stop.Decision != DecisionDone {
		t.Fatalf("decision = %q, want done: every proposed criterion was resolved in the same turn", stop.Decision)
	}
	if len(run.Criteria) != 2 || run.Criteria[0].Status != CriterionSatisfied || run.Criteria[1].Status != CriterionSatisfied {
		t.Fatalf("criteria = %+v, want both satisfied", run.Criteria)
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

// An inconclusive verifier may lack access to redacted raw tool output even
// though its own per-criterion updates confirm every controller-owned
// requirement. That mismatch must finish the run: retrying would replay
// completed work or solicit irrelevant user input.
func TestInconclusiveVerifierCannotRetryResolvedPinnedCriteria(t *testing.T) {
	run, now := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"read report.md and report its heading"})

	stop := completeCycle(t, run, now, "read report.md", VerificationResult{
		Verdict:         VerificationInconclusive,
		Summary:         "raw file content is redacted from verification",
		Retryable:       true,
		RecommendedNext: "ask the user to provide report.md",
		CriteriaUpdates: []CriterionUpdate{{ID: "c1", Status: CriterionSatisfied}},
	})
	if stop.Decision != DecisionDone {
		t.Fatalf("decision = %q, want done after every pinned criterion is satisfied", stop.Decision)
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

// TestTestCriterionSatisfiedByFreshSameCycleEvidence proves the Phase 2
// freshness fix does not penalize the ordinary edit-then-verify sequence: a
// test that passes in the same cycle as a file change is evidence about the
// current workspace state, not stale evidence about an earlier one.
func TestTestCriterionSatisfiedByFreshSameCycleEvidence(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinTypedCriteria([]CriterionSpec{{
		Text:   "go test passes",
		Kind:   CriterionTestResult,
		Target: "go test ./...",
	}})
	run.ApplyDeterministicCriteria(ExecutionResult{
		TestsRun:     []TestResult{{Name: "go test ./...", Passed: true}},
		ChangedFiles: []string{"main.go"},
	}, 1)

	if run.Criteria[0].Status != CriterionSatisfied {
		t.Fatalf("criterion = %+v, want same-cycle test-result satisfaction", run.Criteria[0])
	}
	if run.Criteria[0].Target != "go test ./..." {
		t.Fatalf("criterion target = %q", run.Criteria[0].Target)
	}
}

// TestTestCriterionGoesStaleAfterLaterCycleChangesAFile is the Phase 2 fix
// for a previously characterized gap (see git history for
// TestTestCriterionHasNoFreshnessRelationToChangedFiles): a test criterion
// satisfied in one cycle no longer stays satisfied forever once a later
// cycle changes a file — the prior proof is about a workspace version that
// no longer exists.
func TestTestCriterionGoesStaleAfterLaterCycleChangesAFile(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinTypedCriteria([]CriterionSpec{{
		Text:   "go test passes",
		Kind:   CriterionTestResult,
		Target: "go test ./...",
	}})
	run.ApplyDeterministicCriteria(ExecutionResult{
		TestsRun: []TestResult{{Name: "go test ./...", Passed: true}},
	}, 1)
	if run.Criteria[0].Status != CriterionSatisfied {
		t.Fatalf("cycle 1 criterion = %+v, want satisfied", run.Criteria[0])
	}

	run.ApplyDeterministicCriteria(ExecutionResult{
		ChangedFiles: []string{"main.go"},
	}, 2)

	if run.Criteria[0].Status != CriterionPending {
		t.Fatalf("cycle 2 criterion = %+v, want stale (pending) after the later edit", run.Criteria[0])
	}
	if run.Criteria[0].Note == "" {
		t.Fatal("stale criterion note = \"\", want an explicit invalidation reason")
	}
}

// TestTestCriterionReSatisfiedWhenLaterCycleAlsoReruns proves invalidation
// and re-evaluation happen in the same pass: a later cycle that both changes
// a file and reruns the test immediately re-satisfies the criterion with
// fresh evidence rather than requiring an extra cycle.
func TestTestCriterionReSatisfiedWhenLaterCycleAlsoReruns(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinTypedCriteria([]CriterionSpec{{
		Text:   "go test passes",
		Kind:   CriterionTestResult,
		Target: "go test ./...",
	}})
	run.ApplyDeterministicCriteria(ExecutionResult{
		TestsRun: []TestResult{{Name: "go test ./...", Passed: true}},
	}, 1)

	run.ApplyDeterministicCriteria(ExecutionResult{
		ChangedFiles: []string{"main.go"},
		TestsRun:     []TestResult{{Name: "go test ./...", Passed: true}},
	}, 2)

	if run.Criteria[0].Status != CriterionSatisfied {
		t.Fatalf("cycle 2 criterion = %+v, want fresh rerun to re-satisfy immediately", run.Criteria[0])
	}
	if run.Criteria[0].UpdatedCycle != 2 {
		t.Fatalf("updated cycle = %d, want 2", run.Criteria[0].UpdatedCycle)
	}
}

// TestTestCriterionUsesLatestSameCycleResultNotFirst characterizes a gap
// staleAfterMutation cannot see: it only invalidates proof across strictly
// later cycles, but a single cycle can already contain several tool/test
// rounds (many consecutive turns inside one StageExecutor episode). Within
// one cycle, evaluateCriterion scanned forward and returned on the *first*
// matching test, so an early pass → edit → later failing rerun of the same
// test was reported satisfied — the early pass wrongly stayed authoritative
// over the later, more current failure.
func TestTestCriterionUsesLatestSameCycleResultNotFirst(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinTypedCriteria([]CriterionSpec{{
		Text:   "go test passes",
		Kind:   CriterionTestResult,
		Target: "go test ./...",
	}})
	run.ApplyDeterministicCriteria(ExecutionResult{
		TestsRun: []TestResult{
			{Name: "go test ./...", Passed: true},
			{Name: "go test ./...", Passed: false},
		},
	}, 1)

	if run.Criteria[0].Status != CriterionFailed {
		t.Fatalf("criterion = %+v, want the later same-cycle failure to be authoritative, not the earlier pass", run.Criteria[0])
	}
}

// TestCommandExitCriterionUsesLatestSameCycleResultNotFirst is the
// CriterionCommandExit half of the same gap.
func TestCommandExitCriterionUsesLatestSameCycleResultNotFirst(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinTypedCriteria([]CriterionSpec{{
		Text:   "the command exits cleanly",
		Kind:   CriterionCommandExit,
		Target: "*",
	}})
	run.ApplyDeterministicCriteria(ExecutionResult{
		ToolCalls: []ToolCallRecord{
			{Name: "run_command", Detail: "go build ./...", Succeeded: true},
			{Name: "run_command", Detail: "go build ./...", Succeeded: false},
		},
	}, 1)

	if run.Criteria[0].Status != CriterionFailed {
		t.Fatalf("criterion = %+v, want the later same-cycle failure to be authoritative, not the earlier success", run.Criteria[0])
	}
}

// TestFileStateCriterionUnaffectedByStaleness proves the conservative
// invalidation is scoped to test-result and command-exit criteria: a
// file-state criterion is about the file's current content, so a later edit
// must still resolve it normally rather than being treated as invalidating
// its own evidence.
func TestFileStateCriterionUnaffectedByStaleness(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinTypedCriteria([]CriterionSpec{{
		Text:   "result.txt is written",
		Kind:   CriterionFileState,
		Target: "result.txt",
	}})
	run.ApplyDeterministicCriteria(ExecutionResult{ChangedFiles: []string{"result.txt"}}, 1)
	if run.Criteria[0].Status != CriterionSatisfied {
		t.Fatalf("cycle 1 criterion = %+v, want satisfied", run.Criteria[0])
	}

	run.ApplyDeterministicCriteria(ExecutionResult{ChangedFiles: []string{"unrelated.go"}}, 2)

	if run.Criteria[0].Status != CriterionSatisfied {
		t.Fatalf("cycle 2 criterion = %+v, want file-state criteria unaffected by unrelated later edits", run.Criteria[0])
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

func TestTypedCriteriaUseOnlyRuntimeObservations(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinTypedCriteria([]CriterionSpec{
		{Text: "tests pass", Kind: CriterionTestResult, Target: "go test ./..."},
		{Text: "report exists", Kind: CriterionFileState, Target: "report.md"},
		{Text: "meaning is correct", Kind: CriterionSemantic},
	})
	run.ApplyDeterministicCriteria(ExecutionResult{
		Summary:      "model claims everything passed",
		TestsRun:     []TestResult{{Name: "go test ./...", Passed: true}},
		ChangedFiles: []string{"report.md"},
	}, 1)
	if run.Criteria[0].Status != CriterionSatisfied || run.Criteria[1].Status != CriterionSatisfied {
		t.Fatalf("deterministic criteria = %+v", run.Criteria)
	}
	if run.Criteria[2].Status != CriterionPending {
		t.Fatal("executor prose resolved a semantic criterion")
	}
	semantic := run.UnresolvedSemanticCriteria()
	if len(semantic) != 1 || semantic[0].Text != "meaning is correct" {
		t.Fatalf("semantic criteria = %+v", semantic)
	}
}

func TestExactReadCriterionUsesObservedReadOnly(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"Read the file report.md", "Read report.md and report its heading"})
	run.ApplyDeterministicCriteria(ExecutionResult{
		ToolCalls: []ToolCallRecord{{Name: "read_file", Detail: "report.md", Succeeded: true}},
		ReadObservations: []ReadObservation{
			{Target: "report.md", SourceDigest: "d1", StartLine: 1, EndLine: 3, TotalLines: int64Ptr(3)},
		},
	}, 1)
	if run.Criteria[0].Status != CriterionSatisfied {
		t.Fatalf("exact read criterion = %+v, want satisfied", run.Criteria[0])
	}
	if run.Criteria[1].Status != CriterionPending {
		t.Fatalf("combined criterion = %+v, want pending for semantic verification", run.Criteria[1])
	}
}

// TestExactReadCriterionRequiresFullCoverage is the Phase 4 fix for harness
// plan §4 finding #3: a read_file call that succeeded but only delivered a
// narrow window of a larger file must not mechanically satisfy an exact-read
// criterion — the model never saw the rest of the content.
func TestExactReadCriterionRequiresFullCoverage(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"Read the file report.md"})
	run.ApplyDeterministicCriteria(ExecutionResult{
		ToolCalls: []ToolCallRecord{{Name: "read_file", Detail: "report.md", Succeeded: true}},
		ReadObservations: []ReadObservation{
			{Target: "report.md", SourceDigest: "d1", StartLine: 1, EndLine: 10, TotalLines: int64Ptr(200)},
		},
	}, 1)
	if run.Criteria[0].Status != CriterionPending {
		t.Fatalf("criterion = %+v, want still pending: only 10 of 200 lines were delivered", run.Criteria[0])
	}
}

// TestExactReadCriterionShorthandGrammarIsCoverageAware proves the
// "read_files:<path>" shorthand a real contract-establishing model produces
// (see ExactReadCriterionTarget) is fully wired into the same coverage-aware
// proof as the natural-language grammar, not just recognized in isolation:
// partial coverage stays pending, full coverage (via PinTypedCriteria's
// CriterionSemantic path, matching how the contract step actually pins it)
// satisfies it, and PendingExactReadObligations reports it as an actionable
// obligation while incomplete.
func TestExactReadCriterionShorthandGrammarIsCoverageAware(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"read_files:bignotes.txt"})

	if got := run.PendingExactReadObligations(ExecutionResult{}); len(got) != 1 || got[0].Target != "bignotes.txt" {
		t.Fatalf("obligations = %+v, want the outstanding bignotes.txt obligation before any read", got)
	}

	partial := ExecutionResult{ReadObservations: []ReadObservation{
		{Target: "bignotes.txt", SourceDigest: "d1", StartLine: 1, EndLine: 500, TotalLines: int64Ptr(1200)},
	}}
	run.ApplyDeterministicCriteria(partial, 1)
	if run.Criteria[0].Status != CriterionPending {
		t.Fatalf("criterion = %+v, want pending: only 500 of 1200 lines were delivered", run.Criteria[0])
	}
	if got := run.PendingExactReadObligations(partial); len(got) != 1 {
		t.Fatalf("obligations = %+v, want the obligation to remain outstanding after partial coverage", got)
	}

	full := ExecutionResult{ReadObservations: []ReadObservation{
		{Target: "bignotes.txt", SourceDigest: "d1", StartLine: 1, EndLine: 500, TotalLines: int64Ptr(1200)},
		{Target: "bignotes.txt", SourceDigest: "d1", StartLine: 501, EndLine: 1200, TotalLines: int64Ptr(1200)},
	}}
	run.ApplyDeterministicCriteria(full, 1)
	if run.Criteria[0].Status != CriterionSatisfied {
		t.Fatalf("criterion = %+v, want satisfied: the union now covers all 1200 lines", run.Criteria[0])
	}
	if got := run.PendingExactReadObligations(full); len(got) != 0 {
		t.Fatalf("obligations = %+v, want none: the read already achieved full coverage", got)
	}
}

// TestExactReadCriterionUnionOfPartialReadsSatisfies proves coverage can be
// established across more than one call in the same cycle, as long as the
// unioned windows are gapless and agree on both the source version and total
// line count.
func TestExactReadCriterionUnionOfPartialReadsSatisfies(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"Read the file report.md"})
	run.ApplyDeterministicCriteria(ExecutionResult{
		ReadObservations: []ReadObservation{
			{Target: "report.md", SourceDigest: "d1", StartLine: 1, EndLine: 100, TotalLines: int64Ptr(200)},
			{Target: "report.md", SourceDigest: "d1", StartLine: 101, EndLine: 200, TotalLines: int64Ptr(200)},
		},
	}, 1)
	if run.Criteria[0].Status != CriterionSatisfied {
		t.Fatalf("criterion = %+v, want satisfied: the two windows together cover the whole file", run.Criteria[0])
	}
}

// TestExactReadCriterionGapBetweenPartialReadsDoesNotSatisfy proves a gap
// between two delivered windows (e.g. lines 1-50 and 151-200 of a 200-line
// file) never mechanically satisfies coverage, even though both reads
// succeeded and share the same source version.
func TestExactReadCriterionGapBetweenPartialReadsDoesNotSatisfy(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"Read the file report.md"})
	run.ApplyDeterministicCriteria(ExecutionResult{
		ReadObservations: []ReadObservation{
			{Target: "report.md", SourceDigest: "d1", StartLine: 1, EndLine: 50, TotalLines: int64Ptr(200)},
			{Target: "report.md", SourceDigest: "d1", StartLine: 151, EndLine: 200, TotalLines: int64Ptr(200)},
		},
	}, 1)
	if run.Criteria[0].Status != CriterionPending {
		t.Fatalf("criterion = %+v, want pending: lines 51-150 were never delivered", run.Criteria[0])
	}
}

// TestExactReadCriterionMixedSourceVersionsNeverCombine proves that if the
// file changed between two reads (different SourceDigest), their windows are
// never unioned into one coverage claim even if the ranges would otherwise
// be gapless — each read only proves what it saw of *its own* version.
func TestExactReadCriterionMixedSourceVersionsNeverCombine(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"Read the file report.md"})
	run.ApplyDeterministicCriteria(ExecutionResult{
		ReadObservations: []ReadObservation{
			{Target: "report.md", SourceDigest: "d1", StartLine: 1, EndLine: 100, TotalLines: int64Ptr(200)},
			{Target: "report.md", SourceDigest: "d2", StartLine: 101, EndLine: 200, TotalLines: int64Ptr(200)},
		},
	}, 1)
	if run.Criteria[0].Status != CriterionPending {
		t.Fatalf("criterion = %+v, want pending: the two reads saw different file versions", run.Criteria[0])
	}
}

func TestReadCoverageCompleteTable(t *testing.T) {
	ptr := int64Ptr
	cases := []struct {
		name string
		obs  []ReadObservation
		want bool
	}{
		{"no observations", nil, false},
		{"unknown total", []ReadObservation{{Target: "a.txt", StartLine: 1, EndLine: 5}}, false},
		{"empty file known", []ReadObservation{{Target: "a.txt", TotalLines: ptr(0)}}, true},
		{"single full read", []ReadObservation{{Target: "a.txt", StartLine: 1, EndLine: 5, TotalLines: ptr(5)}}, true},
		{"byte-only observation never covers", []ReadObservation{{Target: "a.txt", TotalLines: ptr(5)}}, false},
		{"case-insensitive target match", []ReadObservation{{Target: "A.TXT", StartLine: 1, EndLine: 5, TotalLines: ptr(5)}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readCoverageComplete("a.txt", tc.obs); got != tc.want {
				t.Fatalf("readCoverageComplete = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAppendReadObservationBoundedAndSequenced(t *testing.T) {
	var execution ExecutionResult
	for i := 0; i < MaxReadObservations+5; i++ {
		AppendReadObservation(&execution, ReadObservation{Target: "a.txt", StartLine: 1, EndLine: 1})
	}
	if len(execution.ReadObservations) != MaxReadObservations {
		t.Fatalf("len = %d, want bounded at %d", len(execution.ReadObservations), MaxReadObservations)
	}
	last := execution.ReadObservations[len(execution.ReadObservations)-1]
	if last.Sequence != MaxReadObservations+5 {
		t.Fatalf("last sequence = %d, want %d: trimming must not renumber survivors", last.Sequence, MaxReadObservations+5)
	}
}

func TestExactReadCriterionTarget(t *testing.T) {
	cases := []struct {
		text       string
		wantTarget string
		wantOK     bool
	}{
		{"Read the file named report.md.", "report.md", true},
		{"Read report.md", "report.md", true},
		{"READ THE FILE report.md", "report.md", true},
		{"Read report.md and report its heading", "report.md and report its heading", true},
		{"Inspect wrapper.c", "", false},
		{"Compare implementation behavior", "", false},
		{"Read ", "", false},
		{"", "", false},
		// Compact shorthand a contract-establishing model produces in the
		// wild (observed repeatedly from gemma-4-e4b via LM Studio) instead
		// of a natural-language sentence for an otherwise identical request.
		{"read_files:bignotes.txt", "bignotes.txt", true},
		{"read_files:2026-09-25-weather-brno.md", "2026-09-25-weather-brno.md", true},
		{"READ_FILES:Report.MD", "report.md", true},
		{"read_file:report.md", "report.md", true},
		{"read_files:", "", false},
		{"read_files:  ", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.text, func(t *testing.T) {
			target, ok := ExactReadCriterionTarget(tc.text)
			if ok != tc.wantOK || target != tc.wantTarget {
				t.Fatalf("ExactReadCriterionTarget(%q) = (%q, %v), want (%q, %v)", tc.text, target, ok, tc.wantTarget, tc.wantOK)
			}
		})
	}
}

// TestPendingExactReadObligationsSkipsAlreadyProven proves the whole point
// of previewing with evaluateCriterion instead of just checking
// run.Criteria[i].Status: ApplyDeterministicCriteria has not run yet this
// cycle (it only runs at verification commit today), so Status is still
// CriterionPending even after a successful matching read_file call. Without
// consulting execution directly, a naive "Status == Pending" obligation
// check would keep reporting this criterion outstanding forever — exactly
// the false no-progress loop the harness plan's freshness prerequisite
// exists to prevent.
func TestPendingExactReadObligationsSkipsAlreadyProven(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"Read the file report.md"})
	if run.Criteria[0].Status != CriterionPending {
		t.Fatalf("criterion = %+v, want pending before any execution", run.Criteria[0])
	}

	execution := ExecutionResult{
		ToolCalls: []ToolCallRecord{{Name: "read_file", Detail: "report.md", Succeeded: true}},
		ReadObservations: []ReadObservation{
			{Target: "report.md", SourceDigest: "d1", StartLine: 1, EndLine: 3, TotalLines: int64Ptr(3)},
		},
	}
	if got := run.PendingExactReadObligations(execution); len(got) != 0 {
		t.Fatalf("obligations = %+v, want none: the read already delivered full coverage this cycle", got)
	}
	// Status itself is still untouched — PendingExactReadObligations must
	// never mutate criterion state, only preview it.
	if run.Criteria[0].Status != CriterionPending {
		t.Fatalf("criterion = %+v, want Status left untouched by a preview call", run.Criteria[0])
	}
}

// TestPendingExactReadObligationsStillPendingForPartialCoverage proves a
// read_file call that succeeded but only delivered part of a larger file
// still leaves the obligation outstanding — the coverage-aware Phase 4
// counterpart to TestExactReadCriterionRequiresFullCoverage, exercised
// through the yield-eligibility preview rather than ApplyDeterministicCriteria.
func TestPendingExactReadObligationsStillPendingForPartialCoverage(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"Read the file report.md"})

	execution := ExecutionResult{
		ReadObservations: []ReadObservation{
			{Target: "report.md", SourceDigest: "d1", StartLine: 1, EndLine: 10, TotalLines: int64Ptr(200)},
		},
	}
	got := run.PendingExactReadObligations(execution)
	if len(got) != 1 || got[0].Target != "report.md" {
		t.Fatalf("obligations = %+v, want the still-outstanding report.md obligation: only 10 of 200 lines were delivered", got)
	}
}

// TestPendingExactReadObligationsReportsUnprovenTarget is the positive case:
// no matching successful read has happened yet, so the criterion is a real
// outstanding obligation and its target is exposed for the yield adapter.
func TestPendingExactReadObligationsReportsUnprovenTarget(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinCriteria([]string{"Read the file named zscaler_wrapper.c", "Compare implementation behavior"})

	got := run.PendingExactReadObligations(ExecutionResult{})
	if len(got) != 1 {
		t.Fatalf("obligations = %+v, want exactly the one exact-read criterion (not the semantic comparison one)", got)
	}
	if got[0].CriterionID != run.Criteria[0].ID || got[0].Target != "zscaler_wrapper.c" {
		t.Fatalf("obligation = %+v, want {%s, zscaler_wrapper.c}", got[0], run.Criteria[0].ID)
	}
}

// TestPendingExactReadObligationsIgnoresNonSemanticCriteria proves Phase 2's
// narrow grammar only ever applies to CriterionSemantic (or legacy-untyped)
// criteria — a typed command_exit/test_result/file_state/user_input
// criterion is never reinterpreted as a read obligation just because its
// text happens to start with "read".
func TestPendingExactReadObligationsIgnoresNonSemanticCriteria(t *testing.T) {
	run, _ := newTestRun(t, DefaultLimits())
	run.PinTypedCriteria([]CriterionSpec{{Text: "read access.log", Kind: CriterionFileState, Target: "access.log"}})

	if got := run.PendingExactReadObligations(ExecutionResult{}); len(got) != 0 {
		t.Fatalf("obligations = %+v, want none: a typed file_state criterion is not a yield-eligible read obligation", got)
	}
}

func TestInferMechanicalCriteriaRejectsMultipartRequests(t *testing.T) {
	execution := ExecutionResult{TestsRun: []TestResult{{Name: "go test ./...", Passed: true}}}
	if got := InferMechanicalCriteria("run the tests", execution); len(got) != 1 || got[0].Kind != CriterionTestResult {
		t.Fatalf("single mechanical request = %+v", got)
	}
	if got := InferMechanicalCriteria("run the tests and write a report", execution); len(got) != 0 {
		t.Fatalf("multipart request inferred unsafe criteria: %+v", got)
	}
}
