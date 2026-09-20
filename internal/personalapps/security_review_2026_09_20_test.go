package personalapps

import (
	"context"
	"testing"
)

// Regression tests for a 2026-09-20 finding: Execute snapshots scope/conn
// once, before queuing for s.adapter (the mutex that serializes all Mail and
// Calendar work), and then hands that same snapshot straight through to the
// read handlers and to changeApply's own "re-check policy and scope" loop.
// SetScope and Disconnect only take s.mu, not s.adapter, so a human revoking
// access while an earlier call holds s.adapter does not affect an operation
// already queued behind it: that operation's pre-queue snapshot still shows
// the old, now-revoked authorization by the time it is finally dispatched.
//
// Both tests below hold s.adapter themselves — standing in for some other
// slow in-flight operation — start the call under test in a goroutine, wait
// for it to clear its own pre-queue checks (signaled through PrivacyGate,
// the last thing Execute does before calling s.adapter.Lock()), revoke
// authorization, and only then release the lock. Without the fix (scope/conn
// re-read from s.mu immediately after s.adapter.Lock() succeeds, in
// service.go's Execute), the queued call still completes using the
// pre-revocation snapshot.

// gatedFixture wires a fixture whose PrivacyGate signals on ch every time
// Execute calls it, letting a test know exactly when a goroutine has cleared
// every check that runs before s.adapter.Lock().
func gatedFixture(t *testing.T, ch chan struct{}, mutate func(*Options)) *fixture {
	t.Helper()
	return newFixture(t, func(o *Options) {
		if mutate != nil {
			mutate(o)
		}
		orig := o.PrivacyGate
		o.PrivacyGate = func(ctx context.Context) error {
			err := orig(ctx)
			ch <- struct{}{}
			return err
		}
	})
}

func TestQueuedReadIsRevalidatedAgainstLiveAuthorizationAfterAdapterLock(t *testing.T) {
	gate := make(chan struct{}, 1)
	f := gatedFixture(t, gate, nil)

	// Stand in for some other in-flight operation already holding the
	// adapter mutex, so the read below must queue behind it.
	f.svc.adapter.Lock()

	done := make(chan Result, 1)
	go func() {
		req, err := ParseRequest([]byte(`{"operation":"mail_accounts"}`), Limits{})
		if err != nil {
			done <- Result{}
			return
		}
		done <- f.svc.Execute(context.Background(), req)
	}()

	<-gate // the read cleared its pre-queue checks and is now waiting for the lock
	f.svc.Disconnect(AdapterMail)
	f.svc.adapter.Unlock()

	res := <-done
	if res.Error == nil {
		t.Fatalf("result = %+v, want the queued read refused after a disconnect that happened while it waited", res)
	}
	if f.mail.calls != 0 {
		t.Fatal("a disconnected adapter was read from")
	}
}

// TestQueuedChangeApplyIsRevalidatedAgainstLiveScopeAfterAdapterLock isolates
// the changeApply-specific half of the finding: its own "re-check policy and
// scope now, not only at prepare time" loop (service.go, changeApply) used
// the exact scope/conn values Execute captured before queuing for
// s.adapter, so the comment's claim was not actually true for a call that
// had to wait. The public Disconnect/SetScope calls happen to also reset
// the plan store, which independently fails a queued change_apply at its
// plan lookup regardless of this bug — so this test flips s.conn directly
// (bypassing Disconnect) to prove changeApply's own re-check, not plan
// invalidation, is what rejects a change whose authorization no longer
// holds by the time execution actually starts.
func TestQueuedChangeApplyIsRevalidatedAgainstLiveScopeAfterAdapterLock(t *testing.T) {
	gate := make(chan struct{}, 8)
	f := gatedFixture(t, gate, func(o *Options) {
		o.Scope.MutationsEnabled = true
		o.Mutator = &fakeMutator{}
	})
	plan := f.movePlan(t)
	f.appr.planID, f.appr.digest = plan.PlanID, plan.Digest
	// movePlan's own setup calls (mail_mailboxes, mail_accounts,
	// mail_search, change_prepare) each signaled the gate once; drain them
	// so only the change_apply call under test is observed below.
	for len(gate) > 0 {
		<-gate
	}

	f.svc.adapter.Lock()

	done := make(chan Result, 1)
	go func() {
		req, err := ParseRequest([]byte(`{"operation":"change_apply","arguments":{"plan_id":"`+plan.PlanID+`"}}`), Limits{})
		if err != nil {
			done <- Result{}
			return
		}
		done <- f.svc.Execute(context.Background(), req)
	}()

	<-gate // change_apply cleared its pre-queue checks and is now waiting for the lock
	f.svc.mu.Lock()
	f.svc.conn.MailConnected = false
	f.svc.mu.Unlock()
	f.svc.adapter.Unlock()

	res := <-done
	if res.Error == nil {
		t.Fatalf("result = %+v, want change_apply refused once mail was no longer connected", res)
	}
	if f.svc.opts.Mutator.(*fakeMutator).calls != 0 {
		t.Fatal("a disconnected adapter was mutated")
	}
	if len(f.journal.intents) != 0 {
		t.Fatal("a disconnected adapter's change was journaled")
	}
}
