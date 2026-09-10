package personalapps

import (
	"context"
	"strings"
	"testing"
	"time"
)

func freshMessage(accountID string, path []string, nativeID string, unread, flagged bool) BackendMessage {
	return BackendMessage{
		Ref: ResourceRef{
			Kind: KindMessage, Adapter: AdapterMail,
			AccountID: accountID, ContainerPath: path, NativeID: nativeID,
		},
		Received: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Unread:   unread,
		Flagged:  flagged,
	}
}

func fingerprintFor(m BackendMessage) string { return messageFingerprint(m) }

func bridgeMsgFrom(m BackendMessage) bridgeMessage {
	return bridgeMessage{
		AccountID: m.Ref.AccountID,
		Path:      m.Ref.ContainerPath,
		NativeID:  m.Ref.NativeID,
		Received:  m.Received.UTC().Format(time.RFC3339),
		Unread:    m.Unread,
		Flagged:   m.Flagged,
	}
}

func TestJXAMailMutatorSetReadAppliesWhenFresh(t *testing.T) {
	before := freshMessage("acct-1", []string{"INBOX"}, "42", true, false)
	after := before
	after.Unread = false

	fake := &multiCallRunner{steps: []func(bridgeRequest) (bridgeResponse, error){
		func(req bridgeRequest) (bridgeResponse, error) {
			if req.Op != "metadata" {
				t.Fatalf("expected first call to be metadata, got %q", req.Op)
			}
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: true, After: ptr(bridgeMsgFrom(before))}}}, nil
		},
		func(req bridgeRequest) (bridgeResponse, error) {
			if req.Op != "set_read" || req.SetRead == nil || req.SetRead.Read != false {
				t.Fatalf("unexpected second call: %+v", req)
			}
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: true, After: ptr(bridgeMsgFrom(after))}}}, nil
		},
	}}
	m := newJXAMailMutator(newJXAMailBackend(fake))

	target := MessageTarget{MessageID: "msg_1", ExpectedVersion: fingerprintFor(before)}
	rc := ResolvedChange{
		Change: Change{Type: ChangeMailSetRead, MailSetRead: &MailSetReadChange{Messages: []MessageTarget{target}, Read: false}},
		Refs:   map[Handle]ResourceRef{"msg_1": before.Ref},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if outcomes[0].Outcome != OutcomeApplied {
		t.Fatalf("outcome = %+v, want applied", outcomes[0])
	}
	if outcomes[0].ObservedVersion == "" || outcomes[0].ObservedVersion == target.ExpectedVersion {
		t.Fatalf("expected a new observed version distinct from the stale one, got %q", outcomes[0].ObservedVersion)
	}
}

func TestJXAMailMutatorSetFlagStaleIsNeverMutated(t *testing.T) {
	before := freshMessage("acct-1", []string{"INBOX"}, "42", true, false)
	mutateCalled := false
	fake := &multiCallRunner{steps: []func(bridgeRequest) (bridgeResponse, error){
		func(req bridgeRequest) (bridgeResponse, error) {
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: true, After: ptr(bridgeMsgFrom(before))}}}, nil
		},
		func(req bridgeRequest) (bridgeResponse, error) {
			mutateCalled = true
			return bridgeResponse{}, nil
		},
	}}
	m := newJXAMailMutator(newJXAMailBackend(fake))

	target := MessageTarget{MessageID: "msg_1", ExpectedVersion: "stale-does-not-match"}
	rc := ResolvedChange{
		Change: Change{Type: ChangeMailSetFlag, MailSetFlag: &MailSetFlagChange{Messages: []MessageTarget{target}, Flagged: true}},
		Refs:   map[Handle]ResourceRef{"msg_1": before.Ref},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if mutateCalled {
		t.Fatal("a stale target must never reach the mutation call")
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeStale {
		t.Fatalf("outcomes = %+v, want a single stale outcome", outcomes)
	}
}

func TestJXAMailMutatorPartialBatchMixesFreshAndStale(t *testing.T) {
	freshMsg := freshMessage("acct-1", []string{"INBOX"}, "1", true, false)
	staleMsg := freshMessage("acct-1", []string{"INBOX"}, "2", true, false)
	afterFresh := freshMsg
	afterFresh.Flagged = true

	fake := &multiCallRunner{steps: []func(bridgeRequest) (bridgeResponse, error){
		func(req bridgeRequest) (bridgeResponse, error) {
			return bridgeResponse{Results: []bridgeMutationItem{
				{Index: 0, OK: true, After: ptr(bridgeMsgFrom(freshMsg))},
				{Index: 1, OK: true, After: ptr(bridgeMsgFrom(staleMsg))},
			}}, nil
		},
		func(req bridgeRequest) (bridgeResponse, error) {
			if len(req.SetFlag.Refs) != 1 || req.SetFlag.Refs[0].NativeID != "1" {
				t.Fatalf("expected only the fresh target to reach the mutation call, got %+v", req.SetFlag.Refs)
			}
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: true, After: ptr(bridgeMsgFrom(afterFresh))}}}, nil
		},
	}}
	m := newJXAMailMutator(newJXAMailBackend(fake))

	targets := []MessageTarget{
		{MessageID: "msg_1", ExpectedVersion: fingerprintFor(freshMsg)},
		{MessageID: "msg_2", ExpectedVersion: "wrong-version"},
	}
	rc := ResolvedChange{
		Change: Change{Type: ChangeMailSetFlag, MailSetFlag: &MailSetFlagChange{Messages: targets, Flagged: true}},
		Refs: map[Handle]ResourceRef{
			"msg_1": freshMsg.Ref,
			"msg_2": staleMsg.Ref,
		},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if outcomes[0].Outcome != OutcomeApplied {
		t.Errorf("outcomes[0] = %+v, want applied", outcomes[0])
	}
	if outcomes[1].Outcome != OutcomeStale {
		t.Errorf("outcomes[1] = %+v, want stale", outcomes[1])
	}
	// Order must be preserved: outcomes correlate back to the original
	// Messages slice by position, not by whatever order the bridge replied.
	if outcomes[0].Target != "msg_1" || outcomes[1].Target != "msg_2" {
		t.Errorf("outcome targets = [%q %q], want [msg_1 msg_2]", outcomes[0].Target, outcomes[1].Target)
	}
}

func TestJXAMailMutatorMoveRejectsCrossAccountEvenIfRefsAllow(t *testing.T) {
	// Service.checkChangeShape already refuses a cross-account move before
	// a Mutator is ever reached; this proves the script-facing request
	// construction still can't produce one even if that upstream check were
	// ever bypassed — defense in depth, not the primary control.
	src := freshMessage("acct-1", []string{"INBOX"}, "1", true, false)
	dest := ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "acct-2", ContainerPath: []string{"Archive"}}

	fake := &multiCallRunner{steps: []func(bridgeRequest) (bridgeResponse, error){
		func(req bridgeRequest) (bridgeResponse, error) {
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: true, After: ptr(bridgeMsgFrom(src))}}}, nil
		},
		func(req bridgeRequest) (bridgeResponse, error) {
			if req.Move.Destination.AccountID != "acct-2" {
				t.Fatalf("unexpected destination account: %+v", req.Move.Destination)
			}
			// The script itself rejects a cross-account ref; simulate that.
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: false, Code: "invalid_request", Message: "cross-account move is not supported"}}}, nil
		},
	}}
	m := newJXAMailMutator(newJXAMailBackend(fake))

	target := MessageTarget{MessageID: "msg_1", ExpectedVersion: fingerprintFor(src)}
	rc := ResolvedChange{
		Change: Change{Type: ChangeMailMove, MailMove: &MailMoveChange{
			Messages: []MessageTarget{target}, DestinationMailboxID: "box_dest",
		}},
		Refs: map[Handle]ResourceRef{"msg_1": src.Ref, "box_dest": dest},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeFailed {
		t.Fatalf("outcomes = %+v, want a single failed outcome", outcomes)
	}
}

func TestJXAMailMutatorMoveSucceeds(t *testing.T) {
	src := freshMessage("acct-1", []string{"INBOX"}, "1", true, false)
	dest := ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "acct-1", ContainerPath: []string{"Archive"}}
	moved := freshMessage("acct-1", []string{"Archive"}, "99", true, false)

	fake := &multiCallRunner{steps: []func(bridgeRequest) (bridgeResponse, error){
		func(req bridgeRequest) (bridgeResponse, error) {
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: true, After: ptr(bridgeMsgFrom(src))}}}, nil
		},
		func(req bridgeRequest) (bridgeResponse, error) {
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: true, After: ptr(bridgeMsgFrom(moved))}}}, nil
		},
	}}
	m := newJXAMailMutator(newJXAMailBackend(fake))

	target := MessageTarget{MessageID: "msg_1", ExpectedVersion: fingerprintFor(src)}
	rc := ResolvedChange{
		Change: Change{Type: ChangeMailMove, MailMove: &MailMoveChange{
			Messages: []MessageTarget{target}, DestinationMailboxID: "box_dest",
		}},
		Refs: map[Handle]ResourceRef{"msg_1": src.Ref, "box_dest": dest},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeApplied {
		t.Fatalf("outcomes = %+v, want applied", outcomes)
	}
}

func TestJXAMailMutatorSaveDraft(t *testing.T) {
	sender := ResourceRef{Kind: KindAccount, Adapter: AdapterMail, AccountID: "acct-1"}
	draft := freshMessage("acct-1", []string{"Drafts"}, "5", false, false)

	fake := &fakeBridgeRunner{response: bridgeResponse{Messages: []bridgeMessage{bridgeMsgFrom(draft)}}}
	m := newJXAMailMutator(newJXAMailBackend(fake))

	rc := ResolvedChange{
		Change: Change{Type: ChangeMailSaveDraft, MailSaveDraft: &MailSaveDraftChange{
			SenderAccountID: "acct_sender", To: []string{"a@example.com"}, Subject: "hi", Body: "body",
		}},
		Refs: map[Handle]ResourceRef{"acct_sender": sender},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeApplied {
		t.Fatalf("outcomes = %+v, want a single applied outcome", outcomes)
	}
	if fake.lastRequest.Op != "save_draft" || fake.lastRequest.SaveDraft.SenderAccountID != "acct-1" {
		t.Fatalf("unexpected request sent: %+v", fake.lastRequest)
	}
}

func TestJXAMailMutatorSaveDraftFailurePropagates(t *testing.T) {
	sender := ResourceRef{Kind: KindAccount, Adapter: AdapterMail, AccountID: "acct-1"}
	fake := &fakeBridgeRunner{response: bridgeResponse{Error: &bridgeError{Code: "internal", Message: "boom"}}}
	m := newJXAMailMutator(newJXAMailBackend(fake))

	rc := ResolvedChange{
		Change: Change{Type: ChangeMailSaveDraft, MailSaveDraft: &MailSaveDraftChange{SenderAccountID: "acct_sender", Body: "body"}},
		Refs:   map[Handle]ResourceRef{"acct_sender": sender},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeUnknown {
		t.Fatalf("outcomes = %+v, want a single unknown outcome", outcomes)
	}
	// Reproduced live: a save_draft failure reached the user as the generic
	// "the draft request could not be completed" with no way to tell what
	// actually broke, while Mail had already silently left a partial draft
	// behind. The bridge's own message (here "boom") must survive into
	// Detail so a real failure is diagnosable instead of opaque.
	if !strings.Contains(outcomes[0].Detail, "boom") {
		t.Errorf("Detail = %q, want it to include the bridge's own message", outcomes[0].Detail)
	}
}

func TestJXAMailMutatorMutationCallFailureIsUnknownNotFailed(t *testing.T) {
	before := freshMessage("acct-1", []string{"INBOX"}, "42", true, false)
	fake := &multiCallRunner{steps: []func(bridgeRequest) (bridgeResponse, error){
		func(req bridgeRequest) (bridgeResponse, error) {
			return bridgeResponse{Results: []bridgeMutationItem{{Index: 0, OK: true, After: ptr(bridgeMsgFrom(before))}}}, nil
		},
		func(req bridgeRequest) (bridgeResponse, error) {
			return bridgeResponse{}, errBridgeOutputTooLarge
		},
	}}
	m := newJXAMailMutator(newJXAMailBackend(fake))

	target := MessageTarget{MessageID: "msg_1", ExpectedVersion: fingerprintFor(before)}
	rc := ResolvedChange{
		Change: Change{Type: ChangeMailSetRead, MailSetRead: &MailSetReadChange{Messages: []MessageTarget{target}, Read: false}},
		Refs:   map[Handle]ResourceRef{"msg_1": before.Ref},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// An error from the mutation call is not evidence nothing happened —
	// Mail may have partially processed the batch — so this must never
	// read as a safe-to-retry "failed", only "unknown".
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeUnknown {
		t.Fatalf("outcomes = %+v, want a single unknown outcome", outcomes)
	}
	if !strings.Contains(outcomes[0].Detail, "more output than allowed") {
		t.Errorf("Detail = %q, want it to include the underlying error's own message", outcomes[0].Detail)
	}
}

func ptr[T any](v T) *T { return &v }
