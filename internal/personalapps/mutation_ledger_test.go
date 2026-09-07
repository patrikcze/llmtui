package personalapps

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func ledgerChange(t *testing.T, message Handle, planID string, read bool) ResolvedChange {
	t.Helper()
	return ResolvedChange{
		Change: Change{
			Type: ChangeMailSetRead,
			MailSetRead: &MailSetReadChange{
				changeKind: changeKind{Type: ChangeMailSetRead},
				Messages:   []MessageTarget{{MessageID: message, ExpectedVersion: "version-1"}},
				Read:       read,
			},
		},
		Refs: map[Handle]ResourceRef{
			message: messageRef("native-message-1", "INBOX"),
		},
		PlanID:     planID,
		PlanDigest: "different-plan-digest",
	}
}

func TestMutationLedgerRecognizesEquivalentResolvedEffectsAcrossPlans(t *testing.T) {
	dir := t.TempDir()
	first := ledgerChange(t, "msg_first", "plan_first", true)
	second := ledgerChange(t, "msg_second", "plan_second", true)
	second.Refs = map[Handle]ResourceRef{
		"msg_second": messageRef("native-message-1", "INBOX"),
	}

	firstKey, err := mutationKey(first)
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	secondKey, err := mutationKey(second)
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if firstKey != secondKey {
		t.Fatal("equivalent effects with different plans or handles have different keys")
	}

	ledger := NewMutationLedger(dir)
	decision, err := ledger.Begin(context.Background(), first)
	if err != nil || decision.State != MutationNew {
		t.Fatalf("first Begin = (%+v, %v), want new", decision, err)
	}

	// A fresh instance simulates a different session/workspace process.
	decision, err = NewMutationLedger(dir).Begin(context.Background(), second)
	if err != nil || decision.State != MutationIntentRecorded {
		t.Fatalf("recovered Begin = (%+v, %v), want recorded intent", decision, err)
	}
}

func TestMutationLedgerChangesIdentityForMateriallyDifferentDesiredState(t *testing.T) {
	read := ledgerChange(t, "msg_read", "plan_read", true)
	unread := ledgerChange(t, "msg_unread", "plan_unread", false)
	unread.Refs = map[Handle]ResourceRef{
		"msg_unread": messageRef("native-message-1", "INBOX"),
	}
	readKey, err := mutationKey(read)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	unreadKey, err := mutationKey(unread)
	if err != nil {
		t.Fatalf("unread key: %v", err)
	}
	if readKey == unreadKey {
		t.Fatal("materially different desired state has the same key")
	}
}

func TestMutationLedgerCalendarIdentityDoesNotDependOnOpaqueHandle(t *testing.T) {
	start := Timestamp{Time: time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)}
	end := Timestamp{Time: time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)}
	makeChange := func(handle Handle, planID string) ResolvedChange {
		return ResolvedChange{
			Change: Change{Type: ChangeCalendarCreateEvent, CalendarCreateEvent: &CalendarCreateEventChange{
				changeKind: changeKind{Type: ChangeCalendarCreateEvent},
				CalendarID: handle,
				Title:      "private meeting",
				Timezone:   "UTC",
				Start:      &start,
				End:        &end,
			}},
			Refs:   map[Handle]ResourceRef{handle: calendarRef()},
			PlanID: planID,
		}
	}
	first, err := mutationKey(makeChange("cal_first", "plan_first"))
	if err != nil {
		t.Fatalf("first key: %v", err)
	}
	second, err := mutationKey(makeChange("cal_second", "plan_second"))
	if err != nil {
		t.Fatalf("second key: %v", err)
	}
	if first != second {
		t.Fatal("calendar effect identity changed only because the opaque handle changed")
	}
}

func TestMutationLedgerPersistsOnlyDigestAndOutcomeCategory(t *testing.T) {
	dir := t.TempDir()
	change := ledgerChange(t, "msg_sensitive", "plan_sensitive", true)
	change.Refs = map[Handle]ResourceRef{
		"msg_sensitive": messageRef("native-message-sensitive", "Family"),
	}
	ledger := NewMutationLedger(dir)
	if _, err := ledger.Begin(context.Background(), change); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := ledger.Complete(context.Background(), change, []ItemOutcome{{Outcome: OutcomeApplied}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	records, err := mutationRecordsForTest(dir)
	if err != nil {
		t.Fatalf("read records: %v", err)
	}
	if len(records) != 2 || records[1].Phase != MutationVerifiedApplied {
		t.Fatalf("records = %+v, want intent then verified_applied", records)
	}
	data, err := os.ReadFile(dir + "/mutations.jsonl")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	for _, secret := range []string{"native-message-sensitive", "Family", "msg_sensitive", "plan_sensitive"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("journal leaked %q: %s", secret, data)
		}
	}
}

func TestMutationLedgerSerializesConcurrentBegins(t *testing.T) {
	dir := t.TempDir()
	change := ledgerChange(t, "msg_concurrent", "plan_concurrent", true)

	decisions := make(chan MutationDecision, 2)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			decision, err := NewMutationLedger(dir).Begin(context.Background(), change)
			decisions <- decision
			errs <- err
		}()
	}
	group.Wait()
	close(decisions)
	close(errs)

	states := make(map[MutationState]int)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Begin: %v", err)
		}
	}
	for decision := range decisions {
		states[decision.State]++
	}
	if states[MutationNew] != 1 || states[MutationIntentRecorded] != 1 {
		t.Fatalf("states = %v, want one new and one recorded intent", states)
	}
}
