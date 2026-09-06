package personalapps

import (
	"context"
	"testing"
	"time"
)

func TestApprovalLedgerApprovesExactDigestOnly(t *testing.T) {
	clock := newFakeClock()
	l := NewApprovalLedger(clock.Now)

	if l.ApprovedPlan("plan_1", "digest_a") {
		t.Fatal("an unrecorded plan reports approved")
	}
	l.Approve("plan_1", "digest_a")
	if !l.ApprovedPlan("plan_1", "digest_a") {
		t.Fatal("an approved plan does not report approved")
	}
	if l.ApprovedPlan("plan_1", "digest_b") {
		t.Fatal("approval for one digest was honored for a different digest")
	}
	if l.ApprovedPlan("plan_2", "digest_a") {
		t.Fatal("approval for one plan id was honored for a different plan id")
	}
}

func TestApprovalLedgerDeny(t *testing.T) {
	l := NewApprovalLedger(newFakeClock().Now)
	l.Approve("plan_1", "digest_a")
	l.Deny("plan_1")
	if l.ApprovedPlan("plan_1", "digest_a") {
		t.Fatal("a denied plan still reports approved")
	}
}

func TestApprovalLedgerIgnoresEmptyValues(t *testing.T) {
	l := NewApprovalLedger(newFakeClock().Now)
	l.Approve("", "digest_a")
	l.Approve("plan_1", "")
	if l.ApprovedPlan("", "digest_a") || l.ApprovedPlan("plan_1", "") {
		t.Fatal("an empty plan id or digest was recorded as an approval")
	}
}

func TestApprovalLedgerExpires(t *testing.T) {
	clock := newFakeClock()
	l := NewApprovalLedger(clock.Now)
	l.Approve("plan_1", "digest_a")

	clock.advance(defaultPlanTTL - time.Second)
	if !l.ApprovedPlan("plan_1", "digest_a") {
		t.Fatal("approval expired before the plan TTL")
	}
	clock.advance(2 * time.Second)
	if l.ApprovedPlan("plan_1", "digest_a") {
		t.Fatal("approval survived past the plan TTL")
	}
}

func TestApprovalLedgerReset(t *testing.T) {
	l := NewApprovalLedger(newFakeClock().Now)
	l.Approve("plan_1", "digest_a")
	l.Reset()
	if l.ApprovedPlan("plan_1", "digest_a") {
		t.Fatal("an approval survived Reset")
	}
}

func TestExecuteRawParsesAndExecutes(t *testing.T) {
	f := newFixture(t, nil)
	res := f.svc.ExecuteRaw(context.Background(), []byte(`{"operation":"status"}`))
	if res.Error != nil {
		t.Fatalf("ExecuteRaw(status) failed: %+v", res.Error)
	}
	if res.Operation != OpStatus {
		t.Fatalf("Operation = %q, want %q", res.Operation, OpStatus)
	}
}

func TestExecuteRawReportsParseFailureAsAResult(t *testing.T) {
	f := newFixture(t, nil)
	res := f.svc.ExecuteRaw(context.Background(), []byte(`not json`))
	if res.Error == nil {
		t.Fatal("ExecuteRaw accepted malformed input")
	}
	if res.Error.Code != CodeInvalidRequest {
		t.Fatalf("code = %q, want %q", res.Error.Code, CodeInvalidRequest)
	}
	if res.Operation != "" {
		t.Fatalf("Operation = %q, want empty for an unparseable request", res.Operation)
	}
}

func TestPreparedPlanMirrorsChangePrepare(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	plan := f.movePlan(t)

	view, err := f.svc.PreparedPlan(plan.PlanID)
	if err != nil {
		t.Fatalf("PreparedPlan: %v", err)
	}
	if view.Digest != plan.Digest || view.ItemCount != plan.ItemCount || len(view.Summary) != len(plan.Summary) {
		t.Fatalf("PreparedPlan() = %+v, want it to match change_prepare's own view %+v", view, plan)
	}

	// It does not consume: the plan can still be applied afterward.
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest
	if res := f.apply(t, plan.PlanID); res.Error != nil {
		t.Fatalf("apply after PreparedPlan failed: %+v", res.Error)
	}
}

func TestPreparedPlanUnknown(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.svc.PreparedPlan("plan_ffffffffffffffffffffffffffffffff"); err == nil {
		t.Fatal("PreparedPlan accepted an unknown plan id")
	}
}

func TestPeekOperation(t *testing.T) {
	tests := map[string]Operation{
		`{"operation":"status"}`:                 OpStatus,
		`{"operation":"mail_search"}`:            OpMailSearch,
		`{"operation":"mail_send"}`:              "",
		`not json`:                               "",
		`{"operation":7}`:                        "",
		`{}`:                                     "",
		`{"operation":"status","approved":true}`: OpStatus, // display-only: not the strict decoder
	}
	for raw, want := range tests {
		if got := PeekOperation([]byte(raw)); got != want {
			t.Errorf("PeekOperation(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestPeekPlanID(t *testing.T) {
	tests := map[string]string{
		`{"operation":"change_apply","arguments":{"plan_id":"plan_abc"}}`: "plan_abc",
		`{"operation":"status"}`:                      "",
		`{"operation":"change_apply","arguments":{}}`: "",
		`not json`: "",
	}
	for raw, want := range tests {
		if got := PeekPlanID([]byte(raw)); got != want {
			t.Errorf("PeekPlanID(%s) = %q, want %q", raw, got, want)
		}
	}
}
