package personalapps

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// calendarBridgeVersion is the protocol spoken by the Go adapter and the
// EventKit companion. A mismatch is always rejected: calendar data has no
// safe best-effort decoding path.
const calendarBridgeVersion = 1

const maxCalendarBridgeFrameBytes = 1024 * 1024

var errCalendarBridgeOutputTooLarge = errors.New("calendar bridge output exceeded the size limit")

// calendarBridgeRunner performs one framed request/response exchange. It is
// deliberately smaller than CalendarBackend so protocol tests do not require
// EventKit, a helper executable, or a macOS permission prompt.
type calendarBridgeRunner interface {
	run(context.Context, []byte) ([]byte, error)
}

type eventKitCalendarBackend struct {
	runner calendarBridgeRunner
}

func newEventKitCalendarBackend(runner calendarBridgeRunner) *eventKitCalendarBackend {
	return &eventKitCalendarBackend{runner: runner}
}

type calendarBridgeRequest struct {
	Version     int                        `json:"version"`
	RequestID   string                     `json:"request_id"`
	Operation   string                     `json:"operation"`
	CalendarIDs []string                   `json:"calendar_ids,omitempty"`
	Event       *calendarBridgeEventRef    `json:"event,omitempty"`
	Start       string                     `json:"start,omitempty"`
	End         string                     `json:"end,omitempty"`
	CreateEvent *calendarBridgeCreateEvent `json:"create_event,omitempty"`
	UpdateEvent *calendarBridgeUpdateEvent `json:"update_event,omitempty"`
}

// calendarBridgeCreateEvent creates one ordinary personal event. Exactly
// one of (Start, End) or (AllDayStart, AllDayEnd) is set — the companion
// rejects a request carrying neither or both, the same rule
// CalendarCreateEventChange.validate already enforces on the request that
// produced this change.
type calendarBridgeCreateEvent struct {
	CalendarID  string `json:"calendar_id"`
	Title       string `json:"title"`
	Notes       string `json:"notes,omitempty"`
	Location    string `json:"location,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
	Start       string `json:"start,omitempty"`
	End         string `json:"end,omitempty"`
	AllDayStart string `json:"all_day_start,omitempty"`
	AllDayEnd   string `json:"all_day_end,omitempty"`
}

// calendarBridgeUpdateEvent patches explicitly named fields of one event.
// A nil-vs-empty distinction matters for Title/Notes/Location: only a
// present field is changed, so these are pointers even though the wire
// value is a plain string once present.
type calendarBridgeUpdateEvent struct {
	EventID         string  `json:"event_id"`
	CalendarID      string  `json:"calendar_id"`
	ExpectedVersion string  `json:"expected_version"`
	Timezone        string  `json:"timezone,omitempty"`
	Title           *string `json:"title,omitempty"`
	Notes           *string `json:"notes,omitempty"`
	Location        *string `json:"location,omitempty"`
	Start           string  `json:"start,omitempty"`
	End             string  `json:"end,omitempty"`
	AllDayStart     string  `json:"all_day_start,omitempty"`
	AllDayEnd       string  `json:"all_day_end,omitempty"`
}

type calendarBridgeEventRef struct {
	CalendarID string `json:"calendar_id"`
	EventID    string `json:"event_id"`
	Occurrence string `json:"occurrence,omitempty"`
}

type calendarBridgeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type calendarBridgeCalendar struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Source   string `json:"source"`
	Writable bool   `json:"writable"`
	Shared   bool   `json:"shared"`
}

type calendarBridgeEvent struct {
	ID         string `json:"id"`
	CalendarID string `json:"calendar_id"`
	ItemID     string `json:"item_id"`
	Title      string `json:"title"`
	Location   string `json:"location"`
	Notes      string `json:"notes"`
	Start      string `json:"start"`
	End        string `json:"end"`
	AllDay     bool   `json:"all_day"`
	Timezone   string `json:"timezone"`
	Recurring  bool   `json:"recurring"`
	Detached   bool   `json:"detached"`
	Canceled   bool   `json:"canceled"`
	Tentative  bool   `json:"tentative"`
	Busy       bool   `json:"busy"`
	Attendees  bool   `json:"has_attendees"`
}

type calendarBridgeResponse struct {
	Version   int                      `json:"version"`
	RequestID string                   `json:"request_id"`
	Operation string                   `json:"operation"`
	Calendars []calendarBridgeCalendar `json:"calendars,omitempty"`
	Events    []calendarBridgeEvent    `json:"events,omitempty"`
	Event     *calendarBridgeEvent     `json:"event,omitempty"`
	Error     *calendarBridgeError     `json:"error,omitempty"`
}

func (b *eventKitCalendarBackend) Calendars(ctx context.Context) ([]BackendCalendar, error) {
	response, err := b.call(ctx, calendarBridgeRequest{Operation: "calendars"})
	if err != nil {
		return nil, err
	}
	calendars := make([]BackendCalendar, 0, len(response.Calendars))
	for _, calendar := range response.Calendars {
		if calendar.ID == "" {
			return nil, Errorf(CodeBridgeProtocolError, "calendar bridge returned a calendar without an identifier")
		}
		calendars = append(calendars, BackendCalendar{
			Ref:   ResourceRef{Kind: KindCalendar, Adapter: AdapterCalendar, AccountID: calendar.ID, NativeID: calendar.ID},
			Title: calendar.Title, Source: calendar.Source, Writable: calendar.Writable, Shared: calendar.Shared,
		})
	}
	return calendars, nil
}

func (b *eventKitCalendarBackend) Events(ctx context.Context, refs []ResourceRef, window Interval) ([]BackendEvent, error) {
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind != KindCalendar || ref.Adapter != AdapterCalendar || ref.NativeID == "" {
			return nil, Errorf(CodeInternal, "calendar backend received an invalid calendar reference")
		}
		ids = append(ids, ref.NativeID)
	}
	response, err := b.call(ctx, calendarBridgeRequest{
		Operation: "events", CalendarIDs: ids,
		Start: window.Start.UTC().Format(time.RFC3339Nano), End: window.End.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, err
	}
	events := make([]BackendEvent, 0, len(response.Events))
	for _, event := range response.Events {
		decoded, err := decodeCalendarBridgeEvent(event)
		if err != nil {
			return nil, err
		}
		events = append(events, decoded)
	}
	return events, nil
}

func (b *eventKitCalendarBackend) Event(ctx context.Context, ref ResourceRef) (BackendEvent, error) {
	if ref.Kind != KindEvent || ref.Adapter != AdapterCalendar || ref.NativeID == "" || ref.AccountID == "" {
		return BackendEvent{}, Errorf(CodeInternal, "calendar backend received an invalid event reference")
	}
	response, err := b.call(ctx, calendarBridgeRequest{Operation: "event", Event: &calendarBridgeEventRef{
		CalendarID: ref.AccountID, EventID: ref.NativeID, Occurrence: ref.ExternalID,
	}})
	if err != nil {
		return BackendEvent{}, err
	}
	if response.Event == nil {
		return BackendEvent{}, Errorf(CodeBridgeProtocolError, "calendar bridge omitted the selected event")
	}
	return decodeCalendarBridgeEvent(*response.Event)
}

func decodeCalendarBridgeEvent(event calendarBridgeEvent) (BackendEvent, error) {
	if event.ID == "" || event.CalendarID == "" {
		return BackendEvent{}, Errorf(CodeBridgeProtocolError, "calendar bridge returned an event without an identifier")
	}
	start, err := time.Parse(time.RFC3339Nano, event.Start)
	if err != nil {
		return BackendEvent{}, Errorf(CodeBridgeProtocolError, "calendar bridge returned an invalid event start")
	}
	end, err := time.Parse(time.RFC3339Nano, event.End)
	if err != nil || !start.Before(end) {
		return BackendEvent{}, Errorf(CodeBridgeProtocolError, "calendar bridge returned an invalid event interval")
	}
	return BackendEvent{
		Ref:         ResourceRef{Kind: KindEvent, Adapter: AdapterCalendar, AccountID: event.CalendarID, NativeID: event.ID, ExternalID: event.ItemID},
		CalendarRef: ResourceRef{Kind: KindCalendar, Adapter: AdapterCalendar, AccountID: event.CalendarID, NativeID: event.CalendarID},
		Title:       event.Title, Location: event.Location, Notes: event.Notes,
		Interval: Interval{Start: start, End: end}, AllDay: event.AllDay, Timezone: event.Timezone,
		Recurring: event.Recurring, Detached: event.Detached, Canceled: event.Canceled,
		Tentative: event.Tentative, Busy: event.Busy, HasAttendees: event.Attendees,
	}, nil
}

func (b *eventKitCalendarBackend) call(ctx context.Context, request calendarBridgeRequest) (calendarBridgeResponse, error) {
	request.Version = calendarBridgeVersion
	request.RequestID = newCalendarBridgeRequestID()
	payload, err := json.Marshal(request)
	if err != nil {
		return calendarBridgeResponse{}, fmt.Errorf("marshal calendar bridge request: %w", err)
	}
	raw, err := b.runner.run(ctx, payload)
	if err != nil {
		return calendarBridgeResponse{}, translateCalendarRunnerError(err)
	}
	var response calendarBridgeResponse
	if err := strictUnmarshal(raw, &response); err != nil {
		return calendarBridgeResponse{}, Errorf(CodeBridgeProtocolError, "calendar bridge returned invalid JSON")
	}
	if response.Version != calendarBridgeVersion || response.RequestID != request.RequestID || response.Operation != request.Operation {
		return calendarBridgeResponse{}, Errorf(CodeBridgeProtocolError, "calendar bridge response did not match its request")
	}
	if response.Error != nil {
		return calendarBridgeResponse{}, calendarBridgeErrorToDomain(*response.Error)
	}
	return response, nil
}

func newCalendarBridgeRequestID() string {
	return fmt.Sprintf("calendar-%d", time.Now().UnixNano())
}

func calendarBridgeErrorToDomain(err calendarBridgeError) error {
	message := clip(err.Message, 256)
	switch err.Code {
	case "permission_denied":
		return Errorf(CodePermissionDenied, "calendar permission was denied: %s", message)
	case "not_found":
		return Errorf(CodeStaleReference, "calendar event is no longer available: %s", message)
	case "unsupported":
		return Errorf(CodeUnsupportedOperation, "calendar operation is unsupported: %s", message)
	case "app_unavailable":
		return Errorf(CodeAppUnavailable, "calendar store is unavailable: %s", message)
	default:
		return Errorf(CodeBridgeProtocolError, "calendar helper error: %s", message)
	}
}

func translateCalendarRunnerError(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return wrap(&Error{Code: CodeAppUnavailable, Message: "Calendar did not respond in time"}, err)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, errCalendarBridgeOutputTooLarge) {
		return Errorf(CodeBridgeProtocolError, "calendar helper response exceeded the size limit")
	}
	return Errorf(CodeAppUnavailable, "calendar helper failed: %s", clip(err.Error(), 256))
}

func frameCalendarBridgePayload(payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > maxCalendarBridgeFrameBytes {
		return nil, Errorf(CodeBridgeProtocolError, "calendar bridge request exceeds the frame limit")
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}

func unframeCalendarBridgePayload(frame []byte) ([]byte, error) {
	if len(frame) < 4 {
		return nil, Errorf(CodeBridgeProtocolError, "calendar helper returned a partial frame")
	}
	n := binary.BigEndian.Uint32(frame[:4])
	if n == 0 || n > maxCalendarBridgeFrameBytes || len(frame) != 4+int(n) {
		return nil, Errorf(CodeBridgeProtocolError, "calendar helper returned an invalid frame")
	}
	return append([]byte(nil), frame[4:]...), nil
}
