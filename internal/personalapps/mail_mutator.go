package personalapps

import "context"

// jxaMailMutator implements Mutator for Mail's four supported change
// types (move, set_read, set_flag, save_draft) by round-tripping through
// the same bridgeRunner jxaMailBackend uses.
//
// Every mutating method re-reads each target's current metadata
// immediately before mutating and compares it against the fingerprint
// recorded when the target was last observed (MessageTarget.
// ExpectedVersion); a mismatch is reported as OutcomeStale and that item is
// never attempted. There is still a small window between that check and the
// mutation itself — two separate script invocations, not one atomic
// operation, since Mail's scripting interface offers no transaction to
// close it further. architecture.md accepts the analogous non-atomic
// check for Calendar's free-slot computation on the same grounds.
type jxaMailMutator struct {
	backend *jxaMailBackend
}

func newJXAMailMutator(backend *jxaMailBackend) *jxaMailMutator {
	return &jxaMailMutator{backend: backend}
}

func (m *jxaMailMutator) Apply(ctx context.Context, rc ResolvedChange) ([]ItemOutcome, error) {
	switch {
	case rc.Change.MailMove != nil:
		return m.move(ctx, rc)
	case rc.Change.MailSetRead != nil:
		return m.setState(ctx, rc.Change.MailSetRead.Messages, rc.Refs, "set_read", func(refs []bridgeMessageRef) bridgeRequest {
			return bridgeRequest{Op: "set_read", SetRead: &bridgeSetReadRequest{Refs: refs, Read: rc.Change.MailSetRead.Read}}
		})
	case rc.Change.MailSetFlag != nil:
		return m.setState(ctx, rc.Change.MailSetFlag.Messages, rc.Refs, "set_flag", func(refs []bridgeMessageRef) bridgeRequest {
			return bridgeRequest{Op: "set_flag", SetFlag: &bridgeSetFlagRequest{Refs: refs, Flagged: rc.Change.MailSetFlag.Flagged}}
		})
	case rc.Change.MailSaveDraft != nil:
		return m.saveDraft(ctx, rc)
	default:
		return nil, Errorf(CodeUnsupportedOperation, "mail mutator does not support this change type")
	}
}

// verifyFresh re-reads current metadata for each target and reports which
// indices still match their recorded expectation. Indices that fail come
// back with a ready-to-return ItemOutcome; indices that pass come back as
// their live ResourceRef, ready to hand to the actual mutation call.
func (m *jxaMailMutator) verifyFresh(ctx context.Context, targets []MessageTarget, refs map[Handle]ResourceRef) (okIdx []int, outcomes []ItemOutcome, liveRefs []ResourceRef, err error) {
	liveRefs = make([]ResourceRef, len(targets))
	for i, t := range targets {
		liveRefs[i] = refs[t.MessageID]
	}
	items, err := m.backend.metadata(ctx, liveRefs)
	if err != nil {
		return nil, nil, nil, err
	}
	outcomes = make([]ItemOutcome, len(targets))
	for i, t := range targets {
		item := items[i]
		if !item.OK {
			outcomes[i] = ItemOutcome{Target: t.MessageID, Outcome: OutcomeStale, Code: CodeStaleReference, Detail: "message not found at its recorded location"}
			continue
		}
		bm, derr := decodeBridgeMessage(*item.After)
		if derr != nil {
			outcomes[i] = ItemOutcome{Target: t.MessageID, Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "could not read the message's current state"}
			continue
		}
		if messageFingerprint(bm) != t.ExpectedVersion {
			outcomes[i] = ItemOutcome{Target: t.MessageID, Outcome: OutcomeStale, Code: CodePreconditionFailed, Detail: "observed state no longer matches what was expected when this change was prepared"}
			continue
		}
		okIdx = append(okIdx, i)
	}
	return okIdx, outcomes, liveRefs, nil
}

// setState implements both set_read and set_flag, which differ only in
// their request/response op names and the boolean field they carry.
func (m *jxaMailMutator) setState(ctx context.Context, targets []MessageTarget, refs map[Handle]ResourceRef, op string, build func([]bridgeMessageRef) bridgeRequest) ([]ItemOutcome, error) {
	okIdx, outcomes, liveRefs, err := m.verifyFresh(ctx, targets, refs)
	if err != nil {
		return nil, err
	}
	if len(okIdx) == 0 {
		return outcomes, nil
	}
	freshRefs := make([]bridgeMessageRef, len(okIdx))
	for i, idx := range okIdx {
		freshRefs[i] = refsToBridgeMessageRefs([]ResourceRef{liveRefs[idx]})[0]
	}
	resp, err := m.backend.call(ctx, build(freshRefs))
	if err != nil {
		// The precondition passed but the mutation call itself failed:
		// every item it would have covered is unknown, not "not applied" —
		// Mail may have partially processed the batch before the failure.
		for _, idx := range okIdx {
			outcomes[idx] = ItemOutcome{Target: targets[idx].MessageID, Outcome: OutcomeUnknown, Code: CodeOf(err), Detail: "the mutation request could not be completed"}
		}
		return outcomes, nil
	}
	if len(resp.Results) != len(okIdx) {
		for _, idx := range okIdx {
			outcomes[idx] = ItemOutcome{Target: targets[idx].MessageID, Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "the mutation response did not match the request"}
		}
		return outcomes, nil
	}
	for i, idx := range okIdx {
		outcomes[idx] = itemOutcomeFromResult(targets[idx].MessageID, resp.Results[i])
	}
	_ = op // op is used only for documentation/readability at call sites
	return outcomes, nil
}

func (m *jxaMailMutator) move(ctx context.Context, rc ResolvedChange) ([]ItemOutcome, error) {
	change := rc.Change.MailMove
	dest, ok := rc.Refs[change.DestinationMailboxID]
	if !ok {
		return nil, Errorf(CodeInternal, "move: destination mailbox was not resolved")
	}
	okIdx, outcomes, liveRefs, err := m.verifyFresh(ctx, change.Messages, rc.Refs)
	if err != nil {
		return nil, err
	}
	if len(okIdx) == 0 {
		return outcomes, nil
	}
	freshRefs := make([]bridgeMessageRef, len(okIdx))
	for i, idx := range okIdx {
		freshRefs[i] = refsToBridgeMessageRefs([]ResourceRef{liveRefs[idx]})[0]
	}
	req := bridgeMoveRequest{
		Refs: freshRefs,
		Destination: bridgeScope{
			AccountID: dest.AccountID,
			Path:      append([]string(nil), dest.ContainerPath...),
		},
	}
	resp, err := m.backend.call(ctx, bridgeRequest{Op: "move", Move: &req})
	if err != nil {
		for _, idx := range okIdx {
			outcomes[idx] = ItemOutcome{Target: change.Messages[idx].MessageID, Outcome: OutcomeUnknown, Code: CodeOf(err), Detail: "the move request could not be completed"}
		}
		return outcomes, nil
	}
	if len(resp.Results) != len(okIdx) {
		for _, idx := range okIdx {
			outcomes[idx] = ItemOutcome{Target: change.Messages[idx].MessageID, Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "the move response did not match the request"}
		}
		return outcomes, nil
	}
	for i, idx := range okIdx {
		outcomes[idx] = itemOutcomeFromResult(change.Messages[idx].MessageID, resp.Results[i])
		// A move can change a message's local id; this release does not
		// mint a replacement handle for it (Mutator has no access to the
		// Service's Registry), so a caller that wants to act on the moved
		// message again must re-resolve it with a fresh mail_search.
	}
	return outcomes, nil
}

func (m *jxaMailMutator) saveDraft(ctx context.Context, rc ResolvedChange) ([]ItemOutcome, error) {
	change := rc.Change.MailSaveDraft
	sender, ok := rc.Refs[change.SenderAccountID]
	if !ok {
		return nil, Errorf(CodeInternal, "save_draft: sender account was not resolved")
	}
	req := bridgeSaveDraftRequest{
		SenderAccountID: sender.AccountID,
		To:              change.To,
		Cc:              change.Cc,
		Bcc:             change.Bcc,
		Subject:         change.Subject,
		Body:            change.Body,
		IncludeQuote:    change.IncludeQuote,
	}
	if change.InReplyToMessageID != "" {
		replyRef, ok := rc.Refs[change.InReplyToMessageID]
		if !ok {
			return nil, Errorf(CodeInternal, "save_draft: in_reply_to message was not resolved")
		}
		r := refsToBridgeMessageRefs([]ResourceRef{replyRef})[0]
		req.InReplyTo = &r
	}
	resp, err := m.backend.call(ctx, bridgeRequest{Op: "save_draft", SaveDraft: &req})
	if err != nil {
		return []ItemOutcome{{Outcome: OutcomeUnknown, Code: CodeOf(err), Detail: "the draft request could not be completed"}}, nil
	}
	if len(resp.Messages) != 1 {
		return []ItemOutcome{{Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "the draft response did not confirm the saved draft"}}, nil
	}
	bm, err := decodeBridgeMessage(resp.Messages[0])
	if err != nil {
		return []ItemOutcome{{Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "the saved draft's confirmation could not be read"}}, nil
	}
	return []ItemOutcome{{Outcome: OutcomeApplied, ObservedVersion: messageFingerprint(bm)}}, nil
}

// itemOutcomeFromResult converts one bridgeMutationItem into the domain's
// ItemOutcome, classifying a script-reported failure by its code rather
// than defaulting every failure to OutcomeFailed: a not_found item means
// the message moved or vanished since it was verified fresh (OutcomeStale,
// not a plain failure), and anything else the script could not classify
// is OutcomeUnknown, never a silently-assumed OutcomeFailed that a caller
// might treat as safe to retry.
func itemOutcomeFromResult(target Handle, item bridgeMutationItem) ItemOutcome {
	if item.OK {
		out := ItemOutcome{Target: target, Outcome: OutcomeApplied}
		if item.After != nil {
			if bm, err := decodeBridgeMessage(*item.After); err == nil {
				out.ObservedVersion = messageFingerprint(bm)
			}
		}
		return out
	}
	switch item.Code {
	case "not_found":
		return ItemOutcome{Target: target, Outcome: OutcomeStale, Code: CodeStaleReference, Detail: "message no longer exists at its expected location"}
	case "invalid_request":
		return ItemOutcome{Target: target, Outcome: OutcomeFailed, Code: CodeInvalidRequest, Detail: item.Message}
	default:
		return ItemOutcome{Target: target, Outcome: OutcomeUnknown, Code: CodeInternal, Detail: "the mutation could not be verified"}
	}
}
