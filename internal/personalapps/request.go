package personalapps

import (
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Arguments is the typed payload of one operation. Every implementation is a
// value type: a Request hands out copies so a validated request cannot be
// edited between approval and execution.
type Arguments interface {
	// Operation reports which operation the payload belongs to.
	Operation() Operation
	// normalize canonicalizes the payload in place.
	normalize()
	// validate rejects anything outside the contract. It never contacts an
	// adapter, so validation cannot prompt or launch an app.
	validate(l Limits) error
}

// Request is one validated, normalized personal_apps call. Build it with
// NewRequest or ParseRequest; the zero value is not usable.
type Request struct {
	op   Operation
	args Arguments
}

// NewRequest validates args against l and returns an immutable Request.
func NewRequest(args Arguments, l Limits) (Request, error) {
	if args == nil {
		return Request{}, Errorf(CodeInvalidRequest, "missing arguments")
	}
	op := args.Operation()
	if !op.Valid() {
		return Request{}, Errorf(CodeInvalidRequest, "unknown operation %q", op)
	}
	full := l.withDefaults()
	args.normalize()
	if err := args.validate(full); err != nil {
		return Request{}, err
	}
	return Request{op: op, args: args}, nil
}

// Operation reports which operation was requested.
func (r Request) Operation() Operation { return r.op }

// Arguments returns the typed payload. Callers type-assert to the concrete
// argument type for the operation.
func (r Request) Arguments() Arguments { return r.args }

// Valid reports whether the Request was produced by successful validation.
func (r Request) Valid() bool { return r.op.Valid() && r.args != nil }

// Timestamp is an RFC3339 instant with an explicit offset. A bare local time
// is rejected: a personal-app write must never guess a zone.
type Timestamp struct {
	time.Time
}

// UnmarshalJSON parses a strict RFC3339 string.
func (ts *Timestamp) UnmarshalJSON(b []byte) error {
	s, err := jsonString(b)
	if err != nil {
		return Errorf(CodeInvalidRequest, "timestamp must be an RFC3339 string")
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return Errorf(CodeInvalidRequest, "timestamp %q is not RFC3339 with an explicit offset", clip(s, 40))
	}
	ts.Time = parsed
	return nil
}

// MarshalJSON writes the instant back in RFC3339.
func (ts Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(`"` + ts.Time.Format(time.RFC3339) + `"`), nil
}

// DateOnly is a calendar date with no instant attached, used for all-day
// events. Its end is exclusive and it is never routed through UTC midnight.
type DateOnly struct {
	Year  int
	Month time.Month
	Day   int
}

// UnmarshalJSON parses a strict YYYY-MM-DD string.
func (d *DateOnly) UnmarshalJSON(b []byte) error {
	s, err := jsonString(b)
	if err != nil {
		return Errorf(CodeInvalidRequest, "date must be a YYYY-MM-DD string")
	}
	parsed, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return Errorf(CodeInvalidRequest, "date %q is not YYYY-MM-DD", clip(s, 20))
	}
	d.Year, d.Month, d.Day = parsed.Date()
	return nil
}

// MarshalJSON writes the date back as YYYY-MM-DD.
func (d DateOnly) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.String() + `"`), nil
}

func (d DateOnly) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day)
}

// IsZero reports whether the date was never set.
func (d DateOnly) IsZero() bool { return d.Year == 0 && d.Month == 0 && d.Day == 0 }

// In returns midnight at the start of the date in loc.
func (d DateOnly) In(loc *time.Location) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
}

// ClockTime is a wall-clock time of day in minutes since midnight. It is a
// working-hours boundary, not an instant, so it survives DST by definition.
type ClockTime int

// UnmarshalJSON parses a strict "HH:MM" string.
func (c *ClockTime) UnmarshalJSON(b []byte) error {
	s, err := jsonString(b)
	if err != nil {
		return Errorf(CodeInvalidRequest, "time of day must be an \"HH:MM\" string")
	}
	parsed, err := time.Parse("15:04", s)
	if err != nil {
		return Errorf(CodeInvalidRequest, "time of day %q is not \"HH:MM\"", clip(s, 10))
	}
	*c = ClockTime(parsed.Hour()*60 + parsed.Minute())
	return nil
}

// MarshalJSON writes the time of day back as "HH:MM".
func (c ClockTime) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", c.String())), nil
}

func (c ClockTime) String() string { return fmt.Sprintf("%02d:%02d", int(c)/60, int(c)%60) }

// Page carries the shared bounded pagination arguments. A cursor is an
// opaque host token bound to the original query; it is never a model-chosen
// offset into a mailbox that keeps changing.
type Page struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

const maxCursorBytes = 512

func (p *Page) normalize() { p.Cursor = strings.TrimSpace(p.Cursor) }

func (p Page) validate(l Limits) error {
	if p.Limit < 0 {
		return Errorf(CodeInvalidRequest, "limit must not be negative")
	}
	if p.Limit > l.MaxPageSize {
		return Errorf(CodeInvalidRequest, "limit %d exceeds the maximum page size %d", p.Limit, l.MaxPageSize)
	}
	if len(p.Cursor) > maxCursorBytes {
		return Errorf(CodeInvalidRequest, "cursor is too long")
	}
	if p.Cursor != "" && !printableASCII(p.Cursor) {
		return Errorf(CodeInvalidRequest, "cursor is not a host-issued token")
	}
	return nil
}

// Size returns the page size to use, applying the configured default.
func (p Page) Size(l Limits) int {
	full := l.withDefaults()
	if p.Limit <= 0 {
		return full.PageSize
	}
	return p.Limit
}

// StatusArgs has no fields: status reports only what the host already knows.
type StatusArgs struct{}

func (StatusArgs) Operation() Operation  { return OpStatus }
func (*StatusArgs) normalize()           {}
func (StatusArgs) validate(Limits) error { return nil }

// MailAccountsArgs lists in-scope mail accounts.
type MailAccountsArgs struct {
	Page
}

func (MailAccountsArgs) Operation() Operation { return OpMailAccounts }
func (a *MailAccountsArgs) normalize()        { a.Page.normalize() }
func (a MailAccountsArgs) validate(l Limits) error {
	return a.Page.validate(l)
}

// MailMailboxesArgs lists mailbox metadata inside one account.
type MailMailboxesArgs struct {
	AccountID Handle `json:"account_id"`
	ParentID  Handle `json:"parent_id,omitempty"`
	Page
}

func (MailMailboxesArgs) Operation() Operation { return OpMailMailboxes }
func (a *MailMailboxesArgs) normalize()        { a.Page.normalize() }
func (a MailMailboxesArgs) validate(l Limits) error {
	if err := requireHandle("account_id", a.AccountID, KindAccount); err != nil {
		return err
	}
	if a.ParentID != "" {
		if err := requireHandle("parent_id", a.ParentID, KindMailbox); err != nil {
			return err
		}
	}
	return a.Page.validate(l)
}

// MailSort is the ordering of a mail search. It is a closed enum: there is
// no free-form sort expression.
type MailSort string

const (
	SortReceivedDesc MailSort = "received_desc"
	SortReceivedAsc  MailSort = "received_asc"
)

// MailSearchArgs is a bounded structured metadata search. It carries no
// predicate text and no script text of any kind.
type MailSearchArgs struct {
	AccountIDs    []Handle   `json:"account_ids,omitempty"`
	MailboxIDs    []Handle   `json:"mailbox_ids,omitempty"`
	ReceivedAfter *Timestamp `json:"received_after,omitempty"`
	// ReceivedBefore is exclusive, so two adjacent windows never overlap.
	ReceivedBefore *Timestamp `json:"received_before,omitempty"`
	From           string     `json:"from,omitempty"`
	Subject        string     `json:"subject,omitempty"`
	Unread         *bool      `json:"unread,omitempty"`
	Flagged        *bool      `json:"flagged,omitempty"`
	Sort           MailSort   `json:"sort,omitempty"`
	Page
}

func (MailSearchArgs) Operation() Operation { return OpMailSearch }

func (a *MailSearchArgs) normalize() {
	a.AccountIDs = normalizeHandles(a.AccountIDs)
	a.MailboxIDs = normalizeHandles(a.MailboxIDs)
	a.From = strings.TrimSpace(a.From)
	a.Subject = strings.TrimSpace(a.Subject)
	if a.Sort == "" {
		a.Sort = SortReceivedDesc
	}
	a.Page.normalize()
}

func (a MailSearchArgs) validate(l Limits) error {
	if len(a.AccountIDs) == 0 && len(a.MailboxIDs) == 0 {
		return Errorf(CodeInvalidRequest, "mail_search requires account_ids or mailbox_ids; there is no unscoped search")
	}
	if err := requireHandles("account_ids", a.AccountIDs, KindAccount, l.MaxPageSize); err != nil {
		return err
	}
	if err := requireHandles("mailbox_ids", a.MailboxIDs, KindMailbox, l.MaxPageSize); err != nil {
		return err
	}
	if a.ReceivedAfter != nil && a.ReceivedBefore != nil && !a.ReceivedAfter.Before(a.ReceivedBefore.Time) {
		return Errorf(CodeInvalidRequest, "received_after must be earlier than received_before")
	}
	if err := checkString("from", a.From, l); err != nil {
		return err
	}
	if err := checkString("subject", a.Subject, l); err != nil {
		return err
	}
	switch a.Sort {
	case SortReceivedDesc, SortReceivedAsc:
	default:
		return Errorf(CodeInvalidRequest, "sort %q is not a supported ordering", clip(string(a.Sort), 32))
	}
	return a.Page.validate(l)
}

// MailReadArgs reads bounded content for explicitly selected messages.
type MailReadArgs struct {
	MessageIDs []Handle `json:"message_ids"`
	// MaxBodyBytes optionally lowers the configured body cap. It can never
	// raise it.
	MaxBodyBytes int `json:"max_body_bytes,omitempty"`
}

func (MailReadArgs) Operation() Operation { return OpMailRead }
func (a *MailReadArgs) normalize()        { a.MessageIDs = normalizeHandles(a.MessageIDs) }

func (a MailReadArgs) validate(l Limits) error {
	if len(a.MessageIDs) == 0 {
		return Errorf(CodeInvalidRequest, "message_ids must select at least one message")
	}
	if len(a.MessageIDs) > l.MaxMessagesPerRead {
		return Errorf(CodeInvalidRequest, "message_ids selects %d messages, the maximum is %d", len(a.MessageIDs), l.MaxMessagesPerRead)
	}
	if err := requireHandles("message_ids", a.MessageIDs, KindMessage, l.MaxMessagesPerRead); err != nil {
		return err
	}
	if a.MaxBodyBytes < 0 {
		return Errorf(CodeInvalidRequest, "max_body_bytes must not be negative")
	}
	if a.MaxBodyBytes > l.MaxBodyBytes {
		return Errorf(CodeInvalidRequest, "max_body_bytes %d exceeds the configured maximum %d", a.MaxBodyBytes, l.MaxBodyBytes)
	}
	return nil
}

// BodyBytes returns the effective body cap for the request.
func (a MailReadArgs) BodyBytes(l Limits) int {
	full := l.withDefaults()
	if a.MaxBodyBytes <= 0 || a.MaxBodyBytes > full.MaxBodyBytes {
		return full.MaxBodyBytes
	}
	return a.MaxBodyBytes
}

// CalendarListArgs lists in-scope calendars.
type CalendarListArgs struct {
	Page
}

func (CalendarListArgs) Operation() Operation { return OpCalendarList }
func (a *CalendarListArgs) normalize()        { a.Page.normalize() }
func (a CalendarListArgs) validate(l Limits) error {
	return a.Page.validate(l)
}

// CalendarEventsArgs lists occurrences overlapping the half-open interval
// [Start, End). Timezone is the IANA zone the caller reasons in; it must
// agree with the offsets carried by Start and End.
type CalendarEventsArgs struct {
	CalendarIDs []Handle  `json:"calendar_ids"`
	Start       Timestamp `json:"start"`
	End         Timestamp `json:"end"`
	Timezone    string    `json:"timezone"`
	Page
}

func (CalendarEventsArgs) Operation() Operation { return OpCalendarEvents }

func (a *CalendarEventsArgs) normalize() {
	a.CalendarIDs = normalizeHandles(a.CalendarIDs)
	a.Timezone = strings.TrimSpace(a.Timezone)
	a.Page.normalize()
}

func (a CalendarEventsArgs) validate(l Limits) error {
	if err := requireHandles("calendar_ids", a.CalendarIDs, KindCalendar, l.MaxPageSize); err != nil {
		return err
	}
	if len(a.CalendarIDs) == 0 {
		return Errorf(CodeInvalidRequest, "calendar_ids must select at least one calendar")
	}
	if _, err := ValidateInterval(a.Start.Time, a.End.Time, a.Timezone, l); err != nil {
		return err
	}
	return a.Page.validate(l)
}

// CalendarEventArgs reads one selected event.
type CalendarEventArgs struct {
	EventID Handle `json:"event_id"`
}

func (CalendarEventArgs) Operation() Operation { return OpCalendarEvent }
func (a *CalendarEventArgs) normalize()        {}
func (a CalendarEventArgs) validate(Limits) error {
	return requireHandle("event_id", a.EventID, KindEvent)
}

// WorkingHours clips free-slot results to a wall-clock window on selected
// weekdays. It is expressed in the request's timezone, never in UTC.
type WorkingHours struct {
	Start ClockTime `json:"start"`
	End   ClockTime `json:"end"`
	// Days lists the weekdays to consider, as lowercase English names.
	// Empty means Monday through Friday.
	Days []string `json:"days,omitempty"`
}

var weekdayNames = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

// Weekdays returns the selected weekdays, defaulting to Monday-Friday.
func (w WorkingHours) Weekdays() map[time.Weekday]bool {
	out := make(map[time.Weekday]bool, 7)
	if len(w.Days) == 0 {
		for d := time.Monday; d <= time.Friday; d++ {
			out[d] = true
		}
		return out
	}
	for _, name := range w.Days {
		if day, ok := weekdayNames[name]; ok {
			out[day] = true
		}
	}
	return out
}

func (w *WorkingHours) normalize() {
	for i, d := range w.Days {
		w.Days[i] = strings.ToLower(strings.TrimSpace(d))
	}
	sort.Strings(w.Days)
	w.Days = dedupeStrings(w.Days)
}

func (w WorkingHours) validate() error {
	if w.Start < 0 || w.End > 24*60 {
		return Errorf(CodeInvalidRequest, "working hours must fall inside one day")
	}
	if w.Start >= w.End {
		return Errorf(CodeInvalidRequest, "working hours start %s is not before end %s", w.Start, w.End)
	}
	for _, d := range w.Days {
		if _, ok := weekdayNames[d]; !ok {
			return Errorf(CodeInvalidRequest, "unknown weekday %q", clip(d, 16))
		}
	}
	return nil
}

// CalendarFreeSlotsArgs computes free intervals from the observed busy
// intervals of the selected calendars only. It is never other people's
// availability and never a server free/busy lookup.
type CalendarFreeSlotsArgs struct {
	CalendarIDs     []Handle     `json:"calendar_ids"`
	Start           Timestamp    `json:"start"`
	End             Timestamp    `json:"end"`
	Timezone        string       `json:"timezone"`
	DurationMinutes int          `json:"duration_minutes"`
	WorkingHours    WorkingHours `json:"working_hours"`
	// BufferMinutes is kept free on both sides of every busy interval.
	BufferMinutes int `json:"buffer_minutes,omitempty"`
	// IncludeAllDayAsBusy makes an all-day event block the whole day.
	IncludeAllDayAsBusy bool `json:"include_all_day_as_busy,omitempty"`
}

func (CalendarFreeSlotsArgs) Operation() Operation { return OpCalendarFreeSlots }

func (a *CalendarFreeSlotsArgs) normalize() {
	a.CalendarIDs = normalizeHandles(a.CalendarIDs)
	a.Timezone = strings.TrimSpace(a.Timezone)
	a.WorkingHours.normalize()
}

func (a CalendarFreeSlotsArgs) validate(l Limits) error {
	if len(a.CalendarIDs) == 0 {
		return Errorf(CodeInvalidRequest, "calendar_ids must select at least one calendar")
	}
	if err := requireHandles("calendar_ids", a.CalendarIDs, KindCalendar, l.MaxPageSize); err != nil {
		return err
	}
	if _, err := ValidateInterval(a.Start.Time, a.End.Time, a.Timezone, l); err != nil {
		return err
	}
	if a.DurationMinutes <= 0 {
		return Errorf(CodeInvalidRequest, "duration_minutes must be positive")
	}
	if a.DurationMinutes > 24*60 {
		return Errorf(CodeInvalidRequest, "duration_minutes must not exceed one day")
	}
	if a.BufferMinutes < 0 || a.BufferMinutes > 12*60 {
		return Errorf(CodeInvalidRequest, "buffer_minutes must be between 0 and 720")
	}
	if err := a.WorkingHours.validate(); err != nil {
		return err
	}
	if window := int(a.WorkingHours.End - a.WorkingHours.Start); a.DurationMinutes > window {
		return Errorf(CodeInvalidRequest, "duration_minutes %d does not fit in the %d-minute working window", a.DurationMinutes, window)
	}
	return nil
}

// ChangePrepareArgs carries the bounded change set to preview. Preparing
// performs no external write, not even saving a draft.
type ChangePrepareArgs struct {
	Changes []Change `json:"changes"`
}

func (ChangePrepareArgs) Operation() Operation { return OpChangePrepare }

func (a *ChangePrepareArgs) normalize() {
	for i := range a.Changes {
		a.Changes[i].normalize()
	}
}

func (a ChangePrepareArgs) validate(l Limits) error {
	if len(a.Changes) == 0 {
		return Errorf(CodeInvalidRequest, "changes must contain at least one change")
	}
	if len(a.Changes) > l.MaxChangesPerPlan {
		return Errorf(CodeInvalidRequest, "changes carries %d entries, the maximum per plan is %d", len(a.Changes), l.MaxChangesPerPlan)
	}
	for i := range a.Changes {
		if err := a.Changes[i].validate(l); err != nil {
			return Errorf(CodeInvalidRequest, "changes[%d]: %s", i, messageOf(err))
		}
	}
	return nil
}

// ChangeApplyArgs executes one prepared plan. The only argument is the
// host-issued plan ID: possessing it is not authorization, and there is no
// argument through which a model could claim approval.
type ChangeApplyArgs struct {
	PlanID string `json:"plan_id"`
}

func (ChangeApplyArgs) Operation() Operation { return OpChangeApply }
func (a *ChangeApplyArgs) normalize()        { a.PlanID = strings.TrimSpace(a.PlanID) }

func (a ChangeApplyArgs) validate(Limits) error {
	if a.PlanID == "" {
		return Errorf(CodeInvalidRequest, "plan_id is required")
	}
	if !validPlanID(a.PlanID) {
		return Errorf(CodeInvalidRequest, "plan_id is not a host-issued plan identifier")
	}
	return nil
}

// OpenItemArgs navigates the owning app to an existing item. It accepts only
// a handle this session issued, never a URL, path or bundle identifier.
type OpenItemArgs struct {
	ItemID Handle `json:"item_id"`
}

func (OpenItemArgs) Operation() Operation { return OpOpenItem }
func (a *OpenItemArgs) normalize()        {}

func (a OpenItemArgs) validate(Limits) error {
	switch a.ItemID.Kind() {
	case KindMessage, KindEvent:
		if !a.ItemID.WellFormed() {
			return Errorf(CodeInvalidRequest, "item_id is not a host-issued handle")
		}
		return nil
	default:
		return Errorf(CodeInvalidRequest, "item_id must be a message or event handle")
	}
}

// newArguments returns an empty payload for op, ready to decode into.
func newArguments(op Operation) (Arguments, error) {
	switch op {
	case OpStatus:
		return &StatusArgs{}, nil
	case OpMailAccounts:
		return &MailAccountsArgs{}, nil
	case OpMailMailboxes:
		return &MailMailboxesArgs{}, nil
	case OpMailSearch:
		return &MailSearchArgs{}, nil
	case OpMailRead:
		return &MailReadArgs{}, nil
	case OpCalendarList:
		return &CalendarListArgs{}, nil
	case OpCalendarEvents:
		return &CalendarEventsArgs{}, nil
	case OpCalendarEvent:
		return &CalendarEventArgs{}, nil
	case OpCalendarFreeSlots:
		return &CalendarFreeSlotsArgs{}, nil
	case OpChangePrepare:
		return &ChangePrepareArgs{}, nil
	case OpChangeApply:
		return &ChangeApplyArgs{}, nil
	case OpOpenItem:
		return &OpenItemArgs{}, nil
	default:
		return nil, Errorf(CodeInvalidRequest, "unknown operation %q", clip(string(op), 40))
	}
}

// Shared validation helpers.

func requireHandle(field string, h Handle, kind HandleKind) error {
	if h == "" {
		return Errorf(CodeInvalidRequest, "%s is required", field)
	}
	if h.Kind() != kind || !h.WellFormed() {
		return Errorf(CodeInvalidRequest, "%s is not a host-issued %s handle", field, kind)
	}
	return nil
}

func requireHandles(field string, hs []Handle, kind HandleKind, max int) error {
	if len(hs) > max {
		return Errorf(CodeInvalidRequest, "%s carries %d entries, the maximum is %d", field, len(hs), max)
	}
	for _, h := range hs {
		if h.Kind() != kind || !h.WellFormed() {
			return Errorf(CodeInvalidRequest, "%s contains an entry that is not a host-issued %s handle", field, kind)
		}
	}
	return nil
}

// normalizeHandles sorts and deduplicates a handle list so two equivalent
// requests canonicalize identically.
func normalizeHandles(hs []Handle) []Handle {
	if len(hs) == 0 {
		return nil
	}
	out := make([]Handle, 0, len(hs))
	for _, h := range hs {
		if trimmed := Handle(strings.TrimSpace(string(h))); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	deduped := out[:0]
	for i, h := range out {
		if i == 0 || h != out[i-1] {
			deduped = append(deduped, h)
		}
	}
	if len(deduped) == 0 {
		return nil
	}
	return deduped
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := in[:0]
	for i, s := range in {
		if s == "" {
			continue
		}
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func checkString(field, value string, l Limits) error {
	if len(value) > l.MaxStringBytes {
		return Errorf(CodeInvalidRequest, "%s exceeds the %d byte limit", field, l.MaxStringBytes)
	}
	if !utf8.ValidString(value) {
		return Errorf(CodeInvalidRequest, "%s is not valid UTF-8", field)
	}
	return nil
}

func checkAddress(field, value string, l Limits) error {
	if err := checkString(field, value, l); err != nil {
		return err
	}
	if strings.TrimSpace(value) == "" {
		return Errorf(CodeInvalidRequest, "%s contains an empty address", field)
	}
	if _, err := mail.ParseAddress(value); err != nil {
		return Errorf(CodeInvalidRequest, "%s contains an address that is not valid", field)
	}
	return nil
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// clip bounds untrusted text echoed back in an error message.
func clip(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

// messageOf returns the model-safe message of a personal-apps error.
func messageOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "invalid value"
}
