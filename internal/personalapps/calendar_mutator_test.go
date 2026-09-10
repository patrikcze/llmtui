package personalapps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func freshEvent(calendarID, eventID string, start, end time.Time, hasAttendees, recurring bool) BackendEvent {
	return BackendEvent{
		Ref:         ResourceRef{Kind: KindEvent, Adapter: AdapterCalendar, AccountID: calendarID, NativeID: eventID},
		CalendarRef: ResourceRef{Kind: KindCalendar, Adapter: AdapterCalendar, AccountID: calendarID, NativeID: calendarID},
		Title:       "Existing", Interval: Interval{Start: start, End: end},
		HasAttendees: hasAttendees, Recurring: recurring,
	}
}

func calendarBridgeEventFrom(e BackendEvent) calendarBridgeEvent {
	return calendarBridgeEvent{
		ID: e.Ref.NativeID, CalendarID: e.Ref.AccountID, ItemID: e.Ref.ExternalID,
		Title: e.Title, Start: e.Interval.Start.UTC().Format(time.RFC3339Nano),
		End: e.Interval.End.UTC().Format(time.RFC3339Nano), AllDay: e.AllDay,
		Attendees: e.HasAttendees, Recurring: e.Recurring,
	}
}

func TestEventKitCalendarMutatorCreateEventTimed(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	var gotOp string
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		var decoded calendarBridgeRequest
		if err := json.Unmarshal(request, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		gotOp = decoded.Operation
		if decoded.CreateEvent == nil || decoded.CreateEvent.CalendarID != "cal-1" || decoded.CreateEvent.Start == "" {
			t.Fatalf("unexpected create_event request: %+v", decoded.CreateEvent)
		}
		created := freshEvent("cal-1", "evt-1", start, end, false, false)
		return calendarBridgeReply(t, request, calendarBridgeResponse{Event: ptr(calendarBridgeEventFrom(created))}), nil
	}})
	m := newEventKitCalendarMutator(backend)

	ts := func(t time.Time) *Timestamp { return &Timestamp{Time: t} }
	rc := ResolvedChange{
		Change: Change{Type: ChangeCalendarCreateEvent, CalendarCreateEvent: &CalendarCreateEventChange{
			CalendarID: "cal_h", Title: "New event", Start: ts(start), End: ts(end),
		}},
		Refs: map[Handle]ResourceRef{"cal_h": {Kind: KindCalendar, Adapter: AdapterCalendar, NativeID: "cal-1"}},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if gotOp != "create_event" {
		t.Fatalf("op = %q, want create_event", gotOp)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeApplied || outcomes[0].ObservedVersion == "" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestEventKitCalendarMutatorCreateEventAllDay(t *testing.T) {
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		var decoded calendarBridgeRequest
		if err := json.Unmarshal(request, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if decoded.CreateEvent == nil || decoded.CreateEvent.AllDayStart != "2026-03-01" || decoded.CreateEvent.AllDayEnd != "2026-03-02" {
			t.Fatalf("unexpected create_event request: %+v", decoded.CreateEvent)
		}
		created := freshEvent("cal-1", "evt-1", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), false, false)
		created.AllDay = true
		return calendarBridgeReply(t, request, calendarBridgeResponse{Event: ptr(calendarBridgeEventFrom(created))}), nil
	}})
	m := newEventKitCalendarMutator(backend)

	rc := ResolvedChange{
		Change: Change{Type: ChangeCalendarCreateEvent, CalendarCreateEvent: &CalendarCreateEventChange{
			CalendarID:  "cal_h",
			Title:       "All day",
			AllDayStart: &DateOnly{Year: 2026, Month: 3, Day: 1},
			AllDayEnd:   &DateOnly{Year: 2026, Month: 3, Day: 2},
		}},
		Refs: map[Handle]ResourceRef{"cal_h": {Kind: KindCalendar, Adapter: AdapterCalendar, NativeID: "cal-1"}},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeApplied {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

// TestEventKitCalendarMutatorCreateEventFailurePropagatesMessage guards the
// same diagnostic gap fixed for mail: a create_event failure reached the
// user as the generic "the create request could not be completed" with no
// way to tell what actually went wrong. The underlying error's own message
// must survive into Detail.
func TestEventKitCalendarMutatorCreateEventFailurePropagatesMessage(t *testing.T) {
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		return nil, errors.New("boom-calendar")
	}})
	m := newEventKitCalendarMutator(backend)

	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	ts := func(t time.Time) *Timestamp { return &Timestamp{Time: t} }
	rc := ResolvedChange{
		Change: Change{Type: ChangeCalendarCreateEvent, CalendarCreateEvent: &CalendarCreateEventChange{
			CalendarID: "cal_h", Title: "New event", Start: ts(start), End: ts(start.Add(time.Hour)),
		}},
		Refs: map[Handle]ResourceRef{"cal_h": {Kind: KindCalendar, Adapter: AdapterCalendar, NativeID: "cal-1"}},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeUnknown {
		t.Fatalf("outcomes = %+v, want a single unknown outcome", outcomes)
	}
	if !strings.Contains(outcomes[0].Detail, "boom-calendar") {
		t.Errorf("Detail = %q, want it to include the underlying error's own message", outcomes[0].Detail)
	}
}

func TestEventKitCalendarMutatorCreateEventRequiresAnInterval(t *testing.T) {
	called := false
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		called = true
		return nil, nil
	}})
	m := newEventKitCalendarMutator(backend)

	rc := ResolvedChange{
		Change: Change{Type: ChangeCalendarCreateEvent, CalendarCreateEvent: &CalendarCreateEventChange{CalendarID: "cal_h", Title: "No interval"}},
		Refs:   map[Handle]ResourceRef{"cal_h": {Kind: KindCalendar, Adapter: AdapterCalendar, NativeID: "cal-1"}},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if called {
		t.Fatal("the bridge must not be called for a change missing its interval")
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeFailed {
		t.Fatalf("outcomes = %+v, want a single failed outcome", outcomes)
	}
}

func TestEventKitCalendarMutatorUpdateAppliesWhenFresh(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	before := freshEvent("cal-1", "evt-1", start, end, false, false)
	expected := eventFingerprint(before)

	updateSent := false
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		var decoded calendarBridgeRequest
		if err := json.Unmarshal(request, &decoded); err != nil {
			t.Fatalf("decode: %v", err)
		}
		switch decoded.Operation {
		case "event":
			return calendarBridgeReply(t, request, calendarBridgeResponse{Event: ptr(calendarBridgeEventFrom(before))}), nil
		case "update_event":
			updateSent = true
			if decoded.UpdateEvent.Title == nil || *decoded.UpdateEvent.Title != "Updated" {
				t.Fatalf("unexpected update_event request: %+v", decoded.UpdateEvent)
			}
			after := before
			after.Title = "Updated"
			return calendarBridgeReply(t, request, calendarBridgeResponse{Event: ptr(calendarBridgeEventFrom(after))}), nil
		default:
			t.Fatalf("unexpected op %q", decoded.Operation)
			return nil, nil
		}
	}})
	m := newEventKitCalendarMutator(backend)

	title := "Updated"
	rc := ResolvedChange{
		Change: Change{Type: ChangeCalendarUpdateEvent, CalendarUpdateEvent: &CalendarUpdateEventChange{
			EventID: "evt_h", ExpectedVersion: expected, Title: &title,
		}},
		Refs: map[Handle]ResourceRef{"evt_h": before.Ref},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !updateSent {
		t.Fatal("expected the update_event call to have been sent")
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeApplied {
		t.Fatalf("outcomes = %+v", outcomes)
	}
}

func TestEventKitCalendarMutatorUpdateStaleIsNeverSent(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	before := freshEvent("cal-1", "evt-1", start, start.Add(time.Hour), false, false)

	updateSent := false
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		var decoded calendarBridgeRequest
		_ = json.Unmarshal(request, &decoded)
		if decoded.Operation == "update_event" {
			updateSent = true
		}
		return calendarBridgeReply(t, request, calendarBridgeResponse{Event: ptr(calendarBridgeEventFrom(before))}), nil
	}})
	m := newEventKitCalendarMutator(backend)

	rc := ResolvedChange{
		Change: Change{Type: ChangeCalendarUpdateEvent, CalendarUpdateEvent: &CalendarUpdateEventChange{
			EventID: "evt_h", ExpectedVersion: "stale-version",
		}},
		Refs: map[Handle]ResourceRef{"evt_h": before.Ref},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if updateSent {
		t.Fatal("a stale target must never reach the update_event call")
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeStale {
		t.Fatalf("outcomes = %+v, want a single stale outcome", outcomes)
	}
}

func TestEventKitCalendarMutatorUpdateRejectsAttendeeBearingEvent(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	before := freshEvent("cal-1", "evt-1", start, start.Add(time.Hour), true /* attendees */, false)
	expected := eventFingerprint(before)

	updateSent := false
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		var decoded calendarBridgeRequest
		_ = json.Unmarshal(request, &decoded)
		if decoded.Operation == "update_event" {
			updateSent = true
		}
		return calendarBridgeReply(t, request, calendarBridgeResponse{Event: ptr(calendarBridgeEventFrom(before))}), nil
	}})
	m := newEventKitCalendarMutator(backend)

	rc := ResolvedChange{
		Change: Change{Type: ChangeCalendarUpdateEvent, CalendarUpdateEvent: &CalendarUpdateEventChange{
			EventID: "evt_h", ExpectedVersion: expected,
		}},
		Refs: map[Handle]ResourceRef{"evt_h": before.Ref},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if updateSent {
		t.Fatal("an attendee-bearing event must never reach the update_event call")
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeFailed {
		t.Fatalf("outcomes = %+v, want a single failed outcome", outcomes)
	}
}

func TestEventKitCalendarMutatorUpdateRejectsRecurringEvent(t *testing.T) {
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	before := freshEvent("cal-1", "evt-1", start, start.Add(time.Hour), false, true /* recurring */)
	expected := eventFingerprint(before)

	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		return calendarBridgeReply(t, request, calendarBridgeResponse{Event: ptr(calendarBridgeEventFrom(before))}), nil
	}})
	m := newEventKitCalendarMutator(backend)

	rc := ResolvedChange{
		Change: Change{Type: ChangeCalendarUpdateEvent, CalendarUpdateEvent: &CalendarUpdateEventChange{
			EventID: "evt_h", ExpectedVersion: expected,
		}},
		Refs: map[Handle]ResourceRef{"evt_h": before.Ref},
	}
	outcomes, err := m.Apply(context.Background(), rc)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Outcome != OutcomeFailed {
		t.Fatalf("outcomes = %+v, want a single failed outcome", outcomes)
	}
}
