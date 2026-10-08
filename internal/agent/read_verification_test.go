package agent

import (
	"testing"
	"time"
)

func TestVerificationCannotOverrideRequiredReadCoverage(t *testing.T) {
	for _, tc := range []struct {
		name         string
		observations []ReadObservation
		wantComplete bool
	}{
		{name: "missing"},
		{name: "partial", observations: []ReadObservation{{Target: "large.txt", SourceDigest: "v1", StartLine: 1, EndLine: 400, TotalLines: int64Ptr(1500)}}},
		{name: "unknown_total", observations: []ReadObservation{{Target: "large.txt", SourceDigest: "v1", StartLine: 1, EndLine: 1500}}},
		{name: "gap", observations: []ReadObservation{
			{Target: "large.txt", SourceDigest: "v1", StartLine: 1, EndLine: 400, TotalLines: int64Ptr(1500)},
			{Target: "large.txt", SourceDigest: "v1", StartLine: 501, EndLine: 1500, TotalLines: int64Ptr(1500)},
		}},
		{name: "mixed_versions", observations: []ReadObservation{
			{Target: "large.txt", SourceDigest: "v1", StartLine: 1, EndLine: 400, TotalLines: int64Ptr(1500)},
			{Target: "large.txt", SourceDigest: "v2", StartLine: 401, EndLine: 1500, TotalLines: int64Ptr(1500)},
		}},
		{name: "complete", observations: []ReadObservation{{Target: "large.txt", SourceDigest: "v1", StartLine: 1, EndLine: 1500, TotalLines: int64Ptr(1500)}}, wantComplete: true},
		{name: "empty", observations: []ReadObservation{{Target: "large.txt", SourceDigest: "v1", StartLine: 1, EndLine: 0, TotalLines: int64Ptr(0)}}, wantComplete: true},
	} {
		for _, status := range []CriterionStatus{CriterionSatisfied, CriterionNotApplicable} {
			t.Run(tc.name+"/"+string(status), func(t *testing.T) {
				run, now := newTestRun(t, DefaultLimits())
				run.PinCriteria([]string{"Read the file large.txt", "Interpret its contents"})
				if err := run.BeginCycle("read and interpret", nil, now); err != nil {
					t.Fatal(err)
				}
				execution := ExecutionResult{ReadObservations: tc.observations}
				if err := run.CompleteExecution(execution, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := run.CompleteVerification(VerificationResult{
					Verdict:         VerificationPassed,
					CriteriaUpdates: []CriterionUpdate{{ID: "c1", Status: status}, {ID: "c2", Status: CriterionSatisfied}},
				}, now.Add(2*time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := run.WriteMemory(now.Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				stop := Decide(run, now.Add(4*time.Second))
				if got := stop.Decision == DecisionDone; got != tc.wantComplete {
					t.Fatalf("done = %t, want %t; criteria = %+v", got, tc.wantComplete, run.Criteria)
				}
				if !tc.wantComplete && run.LatestCycle().Verification.Verdict == VerificationPassed {
					t.Fatal("missing coverage recorded as a passed verification")
				}
				if run.Criteria[1].Status != CriterionSatisfied {
					t.Fatal("unrelated semantic criterion was changed")
				}
			})
		}
	}
}
