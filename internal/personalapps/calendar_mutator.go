package personalapps

import (
	"context"
	"time"
)

// eventKitCalendarMutator implements Mutator for Calendar's two supported
// change types (create, update) over the same eventKitCalendarBackend
// framed-JSON protocol the read path uses.
//
// update_event re-reads its target's current state immediately beforehand
// and compares it against the fingerprint recorded when the target was
// last observed (CalendarUpdateEventChange.ExpectedVersion) — the same
// pattern jxaMailMutator uses for Mail, for the same reason: the
// fingerprint is a Go-computed hash the companion has no way to recompute
// itself, so the freshness decision has to be made on this side, before
// the update request is even sent.
type eventKitCalendarMutator struct {
	backend *eventKitCalendarBackend
}

func newEventKitCalendarMutator(backend *eventKitCalendarBackend) *eventKitCalendarMutator {
	return &eventKitCalendarMutator{backend: backend}
}

func (m *eventKitCalendarMutator) Apply(ctx context.Context, rc ResolvedChange) ([]ItemOutcome, error) {
	switch {
	case rc.Change.CalendarCreateEvent != nil:
		return m.create(ctx, rc)
	case rc.Change.CalendarUpdateEvent != nil:
		return m.update(ctx, rc)
	default:
		return nil, Errorf(CodeUnsupportedOperation, "calendar mutator does not support this change type")
	}
}

func (m *eventKitCalendarMutator) create(ctx context.Context, rc ResolvedChange) ([]ItemOutcome, error) {
	c := rc.Change.CalendarCreateEvent
	cal, ok := rc.Refs[c.CalendarID]
	if !ok {
		return nil, Errorf(CodeInternal, "create_event: calendar was not resolved")
	}
	req := calendarBridgeCreateEvent{
		CalendarID: cal.NativeID,
		Title:      c.Title,
		Notes:      c.Notes,
		Location:   c.Location,
		Timezone:   c.Timezone,
	}
	switch {
	case c.Start != nil && c.End != nil:
		req.Start = c.Start.UTC().Format(time.RFC3339Nano)
		req.End = c.End.UTC().Format(time.RFC3339Nano)
	case c.AllDayStart != nil && c.AllDayEnd != nil:
		req.AllDayStart = c.AllDayStart.String()
		req.AllDayEnd = c.AllDayEnd.String()
	default:
		// request.go's validate() already requires exactly one interval
		// pair; this is a defensive fallback, not the primary control.
		return []ItemOutcome{{Outcome: OutcomeFailed, Code: CodeInvalidRequest, Detail: "create_event requires either a timed or an all-day interval"}}, nil
	}
	resp, err := m.backend.call(ctx, calendarBridgeRequest{Operation: "create_event", CreateEvent: &req})
	if err != nil {
		return []ItemOutcome{{Outcome: OutcomeUnknown, Code: CodeOf(err), Detail: "the create request could not be completed"}}, nil
	}
	if resp.Event == nil {
		return []ItemOutcome{{Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "the create response did not confirm the event"}}, nil
	}
	be, err := decodeCalendarBridgeEvent(*resp.Event)
	if err != nil {
		return []ItemOutcome{{Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "the created event's confirmation could not be read"}}, nil
	}
	return []ItemOutcome{{Outcome: OutcomeApplied, ObservedVersion: eventFingerprint(be)}}, nil
}

func (m *eventKitCalendarMutator) update(ctx context.Context, rc ResolvedChange) ([]ItemOutcome, error) {
	c := rc.Change.CalendarUpdateEvent
	ref, ok := rc.Refs[c.EventID]
	if !ok {
		return nil, Errorf(CodeInternal, "update_event: event was not resolved")
	}

	fresh, err := m.backend.Event(ctx, ref)
	if err != nil {
		if CodeOf(err) == CodeStaleReference {
			return []ItemOutcome{{Target: c.EventID, Outcome: OutcomeStale, Code: CodeStaleReference, Detail: "event no longer exists at its expected location"}}, nil
		}
		return nil, err
	}
	if eventFingerprint(fresh) != c.ExpectedVersion {
		return []ItemOutcome{{Target: c.EventID, Outcome: OutcomeStale, Code: CodePreconditionFailed, Detail: "observed state no longer matches what was expected when this change was prepared"}}, nil
	}
	if fresh.HasAttendees {
		return []ItemOutcome{{Target: c.EventID, Outcome: OutcomeFailed, Code: CodeUnsupportedOperation, Detail: "events with attendees cannot be updated"}}, nil
	}
	if fresh.Recurring {
		return []ItemOutcome{{Target: c.EventID, Outcome: OutcomeFailed, Code: CodeUnsupportedOperation, Detail: "recurring events cannot be updated"}}, nil
	}

	req := calendarBridgeUpdateEvent{
		EventID: ref.NativeID, CalendarID: ref.AccountID,
		ExpectedVersion: c.ExpectedVersion, Timezone: c.Timezone,
		Title: c.Title, Notes: c.Notes, Location: c.Location,
	}
	switch {
	case c.Start != nil && c.End != nil:
		req.Start = c.Start.UTC().Format(time.RFC3339Nano)
		req.End = c.End.UTC().Format(time.RFC3339Nano)
	case c.AllDayStart != nil && c.AllDayEnd != nil:
		req.AllDayStart = c.AllDayStart.String()
		req.AllDayEnd = c.AllDayEnd.String()
	}
	resp, err := m.backend.call(ctx, calendarBridgeRequest{Operation: "update_event", UpdateEvent: &req})
	if err != nil {
		return []ItemOutcome{{Target: c.EventID, Outcome: OutcomeUnknown, Code: CodeOf(err), Detail: "the update request could not be completed"}}, nil
	}
	if resp.Event == nil {
		return []ItemOutcome{{Target: c.EventID, Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "the update response did not confirm the event"}}, nil
	}
	be, err := decodeCalendarBridgeEvent(*resp.Event)
	if err != nil {
		return []ItemOutcome{{Target: c.EventID, Outcome: OutcomeUnknown, Code: CodeBridgeProtocolError, Detail: "the updated event's confirmation could not be read"}}, nil
	}
	return []ItemOutcome{{Target: c.EventID, Outcome: OutcomeApplied, ObservedVersion: eventFingerprint(be)}}, nil
}
