package personalapps

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakeCalendarBridgeRunner struct {
	runFn func([]byte) ([]byte, error)
}

func (f fakeCalendarBridgeRunner) run(_ context.Context, request []byte) ([]byte, error) {
	return f.runFn(request)
}

func calendarBridgeReply(t *testing.T, request []byte, response calendarBridgeResponse) []byte {
	t.Helper()
	var decoded calendarBridgeRequest
	if err := json.Unmarshal(request, &decoded); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	response.Version = calendarBridgeVersion
	response.RequestID = decoded.RequestID
	response.Operation = decoded.Operation
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return raw
}

func TestEventKitCalendarBackendCalendars(t *testing.T) {
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		return calendarBridgeReply(t, request, calendarBridgeResponse{Calendars: []calendarBridgeCalendar{{
			ID: "cal-1", Title: "Personal", Source: "iCloud", Writable: true,
		}}}), nil
	}})
	calendars, err := backend.Calendars(context.Background())
	if err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	if len(calendars) != 1 || calendars[0].Ref.NativeID != "cal-1" || calendars[0].Ref.AccountID != "cal-1" {
		t.Fatalf("Calendars = %+v, want one scoped native calendar", calendars)
	}
}

func TestEventKitCalendarBackendEventsRoundTrip(t *testing.T) {
	start := time.Date(2026, time.March, 29, 8, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: func(request []byte) ([]byte, error) {
		var decoded calendarBridgeRequest
		if err := json.Unmarshal(request, &decoded); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if decoded.Operation != "events" || len(decoded.CalendarIDs) != 1 || decoded.CalendarIDs[0] != "cal-1" {
			t.Fatalf("request = %+v, want a bounded calendar occurrence query", decoded)
		}
		return calendarBridgeReply(t, request, calendarBridgeResponse{Events: []calendarBridgeEvent{{
			ID: "occurrence-1", CalendarID: "cal-1", ItemID: "series-1", Title: "DST review",
			Start: start.Format(time.RFC3339Nano), End: end.Format(time.RFC3339Nano),
			Timezone: "Europe/Prague", Recurring: true, Detached: true, Busy: true,
		}}}), nil
	}})
	events, err := backend.Events(context.Background(), []ResourceRef{{
		Kind: KindCalendar, Adapter: AdapterCalendar, AccountID: "cal-1", NativeID: "cal-1",
	}}, Interval{Start: start, End: end})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Ref.ExternalID != "series-1" || !events[0].Recurring || !events[0].Detached {
		t.Fatalf("Events = %+v, want the occurrence identity and recurrence facts", events)
	}
}

func TestEventKitCalendarBackendRejectsBadResponses(t *testing.T) {
	tests := []struct {
		name  string
		runFn func([]byte) ([]byte, error)
		code  Code
	}{
		{
			name: "wrong request correlation",
			runFn: func(request []byte) ([]byte, error) {
				var decoded calendarBridgeRequest
				if err := json.Unmarshal(request, &decoded); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				raw, err := json.Marshal(calendarBridgeResponse{
					Version: calendarBridgeVersion, RequestID: "wrong", Operation: decoded.Operation,
				})
				if err != nil {
					t.Fatalf("marshal response: %v", err)
				}
				return raw, nil
			},
			code: CodeBridgeProtocolError,
		},
		{
			name: "helper permission denial",
			runFn: func(request []byte) ([]byte, error) {
				return calendarBridgeReply(t, request, calendarBridgeResponse{Error: &calendarBridgeError{Code: "permission_denied", Message: "denied"}}), nil
			},
			code: CodePermissionDenied,
		},
		{
			name:  "oversized helper output",
			runFn: func([]byte) ([]byte, error) { return nil, errCalendarBridgeOutputTooLarge },
			code:  CodeBridgeProtocolError,
		},
		{
			name:  "cancellation passes through",
			runFn: func([]byte) ([]byte, error) { return nil, context.Canceled },
			code:  CodeInternal,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := newEventKitCalendarBackend(fakeCalendarBridgeRunner{runFn: test.runFn})
			_, err := backend.Calendars(context.Background())
			if CodeOf(err) != test.code {
				t.Fatalf("CodeOf(%v) = %q, want %q", err, CodeOf(err), test.code)
			}
			if test.name == "cancellation passes through" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
		})
	}
}

func TestCalendarBridgeFramesAreExact(t *testing.T) {
	payload := []byte(`{"version":1}`)
	frame, err := frameCalendarBridgePayload(payload)
	if err != nil {
		t.Fatalf("frameCalendarBridgePayload: %v", err)
	}
	got, err := unframeCalendarBridgePayload(frame)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("unframe = %q, %v; want %q, nil", got, err, payload)
	}
	for _, frame := range [][]byte{nil, {0, 0, 0, 2, '{'}, {0, 16, 0, 1}} {
		if _, err := unframeCalendarBridgePayload(frame); CodeOf(err) != CodeBridgeProtocolError {
			t.Errorf("unframe(%v) code = %q, want protocol error", frame, CodeOf(err))
		}
	}
}
