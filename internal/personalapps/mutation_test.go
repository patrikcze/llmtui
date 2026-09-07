package personalapps

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// mutableFixture wires a service with mutations enabled and a mutator.
func mutableFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	return newFixture(t, func(o *Options) {
		o.Scope.MutationsEnabled = true
		if mutate != nil {
			mutate(o)
		}
	})
}

// movePlan runs a search, then prepares a move of the found message into
// Archive using the version the search reported.
func (f *fixture) movePlan(t *testing.T) PlanView {
	t.Helper()
	boxes := f.mailboxHandles(t)
	account := f.accountHandle(t)
	search := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	msg := data(t, search, "messages").([]MessageSummaryView)[0]

	res := f.mustRun(t, `{"operation":"change_prepare","arguments":{"changes":[{"type":"mail_move",`+
		`"messages":[{"message_id":"`+string(msg.ID)+`","expected_version":"`+msg.Version+`"}],`+
		`"destination_mailbox_id":"`+string(boxes["Archive"])+`"}]}}`)
	view, ok := res.Data.(PlanView)
	if !ok {
		t.Fatalf("Data is %T, want PlanView", res.Data)
	}
	return view
}

func (f *fixture) apply(t *testing.T, planID string) Result {
	t.Helper()
	return f.run(t, `{"operation":"change_apply","arguments":{"plan_id":"`+planID+`"}}`)
}

func TestChangeOperationsRefusedWhenMutationsAreDisabled(t *testing.T) {
	f := newFixture(t, nil) // mutations off by default
	boxes := f.mailboxHandles(t)
	account := f.accountHandle(t)
	search := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	msg := data(t, search, "messages").([]MessageSummaryView)[0]

	res := f.run(t, `{"operation":"change_prepare","arguments":{"changes":[{"type":"mail_move",`+
		`"messages":[{"message_id":"`+string(msg.ID)+`","expected_version":"`+msg.Version+`"}],`+
		`"destination_mailbox_id":"`+string(boxes["Archive"])+`"}]}}`)
	if res.Error == nil || res.Error.Code != CodeUnsupportedOperation {
		t.Fatalf("result = %+v, want change_prepare refused with mutations off", res)
	}

	res = f.apply(t, "plan_000102030405060708090a0b0c0d0e0f")
	if res.Error == nil || res.Error.Code != CodeUnsupportedOperation {
		t.Fatalf("result = %+v, want change_apply refused with mutations off", res)
	}
}

func TestChangePrepareWritesNothing(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	mutator := f.svc.opts.Mutator.(*fakeMutator)

	plan := f.movePlan(t)
	if mutator.calls != 0 {
		t.Fatal("change_prepare called the mutator")
	}
	if len(f.journal.intents) != 0 {
		t.Fatal("change_prepare recorded a mutation intent")
	}
	if !plan.RequiresApproval {
		t.Fatal("a prepared plan did not say it needs approval")
	}
	if plan.ItemCount != 1 || len(plan.Summary) != 1 {
		t.Fatalf("plan = %+v, want one summarized item", plan)
	}
	if plan.ExpiresAt.IsZero() {
		t.Fatal("a prepared plan has no expiry")
	}
	// The model-facing preview must not carry the message subject or body.
	if strings.Contains(strings.Join(plan.Summary, " "), "Status update") {
		t.Fatalf("the preview leaked message content: %v", plan.Summary)
	}
}

func TestChangeApplyRequiresHumanApproval(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	mutator := f.svc.opts.Mutator.(*fakeMutator)
	plan := f.movePlan(t)

	// Possessing the plan ID is not authorization.
	res := f.apply(t, plan.PlanID)
	if res.Error == nil || res.Error.Code != CodePreconditionFailed {
		t.Fatalf("result = %+v, want an unapproved apply refused", res)
	}
	if mutator.calls != 0 {
		t.Fatal("an unapproved plan reached the mutator")
	}
	if len(f.journal.intents) != 0 {
		t.Fatal("an unapproved plan was journaled")
	}
}

// Approval binds to one exact plan and digest, so approving one change set
// never carries over to another.
func TestChangeApplyApprovalIsBoundToTheExactPlan(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	plan := f.movePlan(t)

	f.appr.planID, f.appr.digest = plan.PlanID, "a-different-digest"
	if res := f.apply(t, plan.PlanID); res.Error == nil {
		t.Fatal("an approval for a different digest applied the plan")
	}

	f.appr.planID, f.appr.digest = "plan_ffffffffffffffffffffffffffffffff", plan.Digest
	if res := f.apply(t, plan.PlanID); res.Error == nil {
		t.Fatal("an approval for a different plan id applied the plan")
	}
}

func TestChangeApplyRefusesWithoutAJournal(t *testing.T) {
	f := mutableFixture(t, func(o *Options) {
		o.Mutator = &fakeMutator{}
		o.Journal = nil
	})
	mutator := f.svc.opts.Mutator.(*fakeMutator)
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	res := f.apply(t, plan.PlanID)
	if res.Error == nil || res.Error.Code != CodeJournalUnavailable {
		t.Fatalf("result = %+v, want the mutation refused with no ledger", res)
	}
	if mutator.calls != 0 {
		t.Fatal("a mutation ran with no operation ledger")
	}
}

func TestChangeApplyRefusesWhenIntentCannotBeRecorded(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	mutator := f.svc.opts.Mutator.(*fakeMutator)
	f.journal.intentEr = errors.New("disk full")
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	res := f.apply(t, plan.PlanID)
	if res.Error == nil || res.Error.Code != CodeJournalUnavailable {
		t.Fatalf("result = %+v, want the mutation refused when intent could not be recorded", res)
	}
	if mutator.calls != 0 {
		t.Fatal("a mutation ran although its intent was not durable")
	}
}

func TestChangeApplyRecordsIntentBeforeActing(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	res := f.apply(t, plan.PlanID)
	if res.Error != nil {
		t.Fatalf("apply failed: %+v", res.Error)
	}
	view := res.Data.(ApplyView)
	if view.Applied != 1 || view.Unknown != 0 || view.Failed != 0 {
		t.Fatalf("ApplyView = %+v, want one applied item", view)
	}
	if got := strings.Join(f.journal.order, ","); got != "intent,outcome" {
		t.Fatalf("journal order = %q, want intent before outcome", got)
	}
	if res.Status != StatusOK {
		t.Fatalf("Status = %q, want %q", res.Status, StatusOK)
	}
}

func TestChangeApplyConsumesThePlan(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	if res := f.apply(t, plan.PlanID); res.Error != nil {
		t.Fatalf("first apply failed: %+v", res.Error)
	}
	res := f.apply(t, plan.PlanID)
	if res.Error == nil || res.Error.Code != CodeStaleReference {
		t.Fatalf("result = %+v, want the second apply of one plan refused", res)
	}
	if f.svc.opts.Mutator.(*fakeMutator).calls != 1 {
		t.Fatal("the mutator ran twice for one approved plan")
	}
}

func TestChangeApplyExpiredPlan(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	f.clock.advance(defaultPlanTTL + time.Minute)
	res := f.apply(t, plan.PlanID)
	if res.Error == nil || res.Error.Code != CodeStaleReference {
		t.Fatalf("result = %+v, want an expired plan refused", res)
	}
	if f.svc.opts.Mutator.(*fakeMutator).calls != 0 {
		t.Fatal("an expired plan reached the mutator")
	}
}

// An adapter error is not evidence that nothing happened: a timeout can
// follow a successful side effect.
func TestChangeApplyReportsUnknownOutcomes(t *testing.T) {
	f := mutableFixture(t, func(o *Options) {
		o.Mutator = &fakeMutator{err: &Error{Code: CodeBridgeProtocolError, Message: "the helper stopped responding"}}
	})
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	res := f.apply(t, plan.PlanID)
	if res.Status != StatusOutcomeUnknown {
		t.Fatalf("Status = %q, want %q", res.Status, StatusOutcomeUnknown)
	}
	view := res.Data.(ApplyView)
	if view.Unknown != 1 || view.Applied != 0 {
		t.Fatalf("ApplyView = %+v, want one unknown outcome", view)
	}
	if len(res.Warnings) == 0 || !strings.Contains(strings.Join(res.Warnings, " "), "must not be retried automatically") {
		t.Fatalf("Warnings = %v, want the retry ban stated", res.Warnings)
	}
	// The plan is consumed either way: a repeat needs a fresh preview.
	if res := f.apply(t, plan.PlanID); res.Error == nil {
		t.Fatal("an uncertain plan could be applied again")
	}
}

func TestChangeApplyDoesNotRetryUnknownEffectFromPersistentLedger(t *testing.T) {
	f := mutableFixture(t, func(o *Options) {
		o.Mutator = &fakeMutator{err: &Error{Code: CodeBridgeProtocolError, Message: "the helper stopped responding"}}
		o.Journal = NewMutationLedger(t.TempDir())
	})
	mutator := f.svc.opts.Mutator.(*fakeMutator)
	first := f.movePlan(t)
	f.appr.planID, f.appr.digest = first.PlanID, first.Digest
	if res := f.apply(t, first.PlanID); res.Status != StatusOutcomeUnknown {
		t.Fatalf("first apply status = %q, want %q", res.Status, StatusOutcomeUnknown)
	}
	if mutator.calls != 1 {
		t.Fatalf("first mutator calls = %d, want 1", mutator.calls)
	}

	// A fresh plan and new human approval still cannot make an interrupted
	// effect retry itself. Fresh observations that change the effect identity
	// are required before an intentional repeat can be represented.
	second := f.movePlan(t)
	f.appr.planID, f.appr.digest = second.PlanID, second.Digest
	res := f.apply(t, second.PlanID)
	if res.Status != StatusOutcomeUnknown {
		t.Fatalf("second apply status = %q, want %q", res.Status, StatusOutcomeUnknown)
	}
	if mutator.calls != 1 {
		t.Fatalf("uncertain effect ran again; mutator calls = %d, want 1", mutator.calls)
	}
}

// A denial that proves nothing happened is reported as not applied, not as
// an unknown outcome that blocks a legitimate later attempt.
func TestChangeApplyKnownRefusalIsNotUnknown(t *testing.T) {
	f := mutableFixture(t, func(o *Options) {
		o.Mutator = &fakeMutator{err: &Error{Code: CodeReadOnlyCalendar, Message: "the calendar does not accept writes"}}
	})
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	res := f.apply(t, plan.PlanID)
	view := res.Data.(ApplyView)
	if view.Unknown != 0 {
		t.Fatalf("ApplyView = %+v, want a proven refusal reported as not applied", view)
	}
	if res.Status != StatusOK {
		t.Fatalf("Status = %q, want %q for a change that provably did not happen", res.Status, StatusOK)
	}
}

func TestChangeApplyStopsAfterAnUncertainItem(t *testing.T) {
	f := mutableFixture(t, func(o *Options) {
		o.Mutator = &fakeMutator{outcomes: [][]ItemOutcome{
			{{Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError}},
			{{Outcome: OutcomeApplied}},
		}}
	})
	boxes := f.mailboxHandles(t)
	account := f.accountHandle(t)
	search := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	msg := data(t, search, "messages").([]MessageSummaryView)[0]

	prepared := f.mustRun(t, `{"operation":"change_prepare","arguments":{"changes":[`+
		`{"type":"mail_move","messages":[{"message_id":"`+string(msg.ID)+`","expected_version":"`+msg.Version+`"}],`+
		`"destination_mailbox_id":"`+string(boxes["Archive"])+`"},`+
		`{"type":"mail_set_flag","messages":[{"message_id":"`+string(msg.ID)+`","expected_version":"`+msg.Version+`"}],"flagged":true}]}}`)
	plan := prepared.Data.(PlanView)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	res := f.apply(t, plan.PlanID)
	if res.Status != StatusOutcomeUnknown {
		t.Fatalf("Status = %q, want %q", res.Status, StatusOutcomeUnknown)
	}
	if calls := f.svc.opts.Mutator.(*fakeMutator).calls; calls != 1 {
		t.Fatalf("the mutator ran %d times, want it stopped after the uncertain item", calls)
	}
	view := res.Data.(ApplyView)
	notApplied := 0
	for _, o := range view.Outcomes {
		if o.Outcome == OutcomeNotApplied {
			notApplied++
		}
	}
	if notApplied == 0 {
		t.Fatalf("outcomes = %+v, want the remaining items reported as not applied", view.Outcomes)
	}
}

// The change happened but could not be recorded: that is uncertain state,
// not success.
func TestChangeApplyOutcomeRecordFailureIsUncertain(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	f.journal.outcomeEr = errors.New("fsync failed")
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	res := f.apply(t, plan.PlanID)
	if res.Status != StatusOutcomeUnknown {
		t.Fatalf("Status = %q, want %q when the outcome could not be recorded", res.Status, StatusOutcomeUnknown)
	}
}

func TestChangeApplyRechecksScopeAfterPreparation(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest

	// The human disconnects mail between approval and execution.
	f.svc.Disconnect(AdapterMail)
	res := f.apply(t, plan.PlanID)
	if res.Error == nil {
		t.Fatalf("result = %+v, want the apply refused after disconnect", res)
	}
	if f.svc.opts.Mutator.(*fakeMutator).calls != 0 {
		t.Fatal("a disconnected adapter was mutated")
	}
}

func TestChangePrepareRejectsCrossAccountMoves(t *testing.T) {
	f := mutableFixture(t, func(o *Options) {
		o.Scope.AllowedAccounts = []string{testAccount, "acct-native-2"}
		o.Mutator = &fakeMutator{}
	})
	f.mail.accounts = append(f.mail.accounts, BackendAccount{
		Ref:         ResourceRef{Kind: KindAccount, Adapter: AdapterMail, AccountID: "acct-native-2"},
		DisplayName: "Personal",
	})
	f.mail.mailboxes = append(f.mail.mailboxes, BackendMailbox{
		Ref:         ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "acct-native-2", ContainerPath: []string{"Archive"}},
		DisplayName: "Other Archive",
	})

	listed := f.mustRun(t, `{"operation":"mail_accounts"}`)
	accounts := data(t, listed, "accounts").([]AccountView)
	if len(accounts) != 2 {
		t.Fatalf("mail_accounts returned %d accounts, want both in scope", len(accounts))
	}
	boxes := map[string]Handle{}
	for _, account := range accounts {
		res := f.mustRun(t, `{"operation":"mail_mailboxes","arguments":{"account_id":"`+string(account.ID)+`"}}`)
		for _, box := range data(t, res, "mailboxes").([]MailboxView) {
			boxes[box.Name] = box.ID
		}
	}
	search := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(accounts[0].ID)+`"]}}`)
	msg := data(t, search, "messages").([]MessageSummaryView)[0]

	res := f.run(t, `{"operation":"change_prepare","arguments":{"changes":[{"type":"mail_move",`+
		`"messages":[{"message_id":"`+string(msg.ID)+`","expected_version":"`+msg.Version+`"}],`+
		`"destination_mailbox_id":"`+string(boxes["Other Archive"])+`"}]}}`)
	if res.Error == nil || res.Error.Code != CodeUnsupportedOperation {
		t.Fatalf("result = %+v, want a cross-account move refused", res)
	}
}

func TestChangePrepareRejectsUnknownHandles(t *testing.T) {
	f := mutableFixture(t, func(o *Options) { o.Mutator = &fakeMutator{} })
	res := f.run(t, `{"operation":"change_prepare","arguments":{"changes":[{"type":"mail_set_read",`+
		`"messages":[{"message_id":"msg_000102030405060708090a0b","expected_version":"v1"}],"read":true}]}}`)
	if res.Error == nil || res.Error.Code != CodeStaleReference {
		t.Fatalf("result = %+v, want an unissued handle refused", res)
	}
}

func TestResolvedChangeIsACopy(t *testing.T) {
	captured := make(chan ResolvedChange, 1)
	f := mutableFixture(t, func(o *Options) {
		o.Mutator = mutatorFunc(func(rc ResolvedChange) ([]ItemOutcome, error) {
			captured <- rc
			return []ItemOutcome{{Outcome: OutcomeApplied}}, nil
		})
	})
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest
	if res := f.apply(t, plan.PlanID); res.Error != nil {
		t.Fatalf("apply failed: %+v", res.Error)
	}

	rc := <-captured
	if rc.PlanID != plan.PlanID || rc.PlanDigest != plan.Digest {
		t.Fatalf("ResolvedChange = %+v, want it tagged with the approved plan", rc)
	}
	if len(rc.Refs) == 0 {
		t.Fatal("the mutator was handed no resolved resources")
	}
	for h, ref := range rc.Refs {
		if h.Kind() == KindMessage && ref.NativeID != "m1" {
			t.Fatalf("handle %q resolved to %q, want the observed message", h, ref.NativeID)
		}
	}
}

// mutatorFunc adapts a function to the Mutator interface.
type mutatorFunc func(ResolvedChange) ([]ItemOutcome, error)

func (f mutatorFunc) Apply(_ context.Context, rc ResolvedChange) ([]ItemOutcome, error) {
	return f(rc)
}
