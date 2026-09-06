package personalapps

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// ChangeType is the closed set of mutations the first release can prepare.
// Sending mail, forwarding, adding attendees, editing recurrence and any
// destructive operation are deliberately absent: an operation that does not
// exist in this enum cannot be requested, approved or executed.
type ChangeType string

const (
	ChangeMailMove            ChangeType = "mail_move"
	ChangeMailSetRead         ChangeType = "mail_set_read"
	ChangeMailSetFlag         ChangeType = "mail_set_flag"
	ChangeMailSaveDraft       ChangeType = "mail_save_draft"
	ChangeCalendarCreateEvent ChangeType = "calendar_create_event"
	ChangeCalendarUpdateEvent ChangeType = "calendar_update_event"
)

var changeTypes = []ChangeType{
	ChangeMailMove,
	ChangeMailSetRead,
	ChangeMailSetFlag,
	ChangeMailSaveDraft,
	ChangeCalendarCreateEvent,
	ChangeCalendarUpdateEvent,
}

// ChangeTypes returns every supported change variant in a stable order.
func ChangeTypes() []ChangeType {
	out := make([]ChangeType, len(changeTypes))
	copy(out, changeTypes)
	return out
}

// changeKind carries the discriminator every variant shares.
type changeKind struct {
	Type ChangeType `json:"type"`
}

// MessageTarget names one message and the state the caller believes it is
// in. ExpectedVersion is the fingerprint observed when the handle was
// issued; apply refuses when a fresh read disagrees, so a message that moved
// or was read in the meantime is never silently included.
type MessageTarget struct {
	MessageID       Handle `json:"message_id"`
	ExpectedVersion string `json:"expected_version"`
}

// MailMoveChange moves messages into one mailbox in the same account.
// Cross-account moves are copy-then-delete on the app side and are not
// supported here.
type MailMoveChange struct {
	changeKind
	Messages             []MessageTarget `json:"messages"`
	DestinationMailboxID Handle          `json:"destination_mailbox_id"`
}

// MailSetReadChange sets an explicit desired read state. There is no toggle:
// a toggle applied twice is not idempotent.
type MailSetReadChange struct {
	changeKind
	Messages []MessageTarget `json:"messages"`
	Read     bool            `json:"read"`
}

// MailSetFlagChange sets an explicit desired flag state.
type MailSetFlagChange struct {
	changeKind
	Messages []MessageTarget `json:"messages"`
	Flagged  bool            `json:"flagged"`
}

// MailSaveDraftChange saves a draft for the human to review in Mail. It
// never sends. When InReplyToMessageID is set, threading comes from Mail's
// own reply command rather than from a guessed "Re:" subject.
type MailSaveDraftChange struct {
	changeKind
	SenderAccountID    Handle   `json:"sender_account_id"`
	InReplyToMessageID Handle   `json:"in_reply_to_message_id,omitempty"`
	To                 []string `json:"to,omitempty"`
	Cc                 []string `json:"cc,omitempty"`
	Bcc                []string `json:"bcc,omitempty"`
	Subject            string   `json:"subject,omitempty"`
	Body               string   `json:"body"`
	// IncludeQuote asks Mail to quote the message being replied to.
	IncludeQuote bool `json:"include_quote,omitempty"`
}

// CalendarCreateEventChange creates one ordinary personal event. It has no
// attendee and no recurrence field: those are separate later gates, and an
// argument that does not exist cannot be half-supported.
type CalendarCreateEventChange struct {
	changeKind
	CalendarID Handle `json:"calendar_id"`
	Title      string `json:"title"`
	Notes      string `json:"notes,omitempty"`
	Location   string `json:"location,omitempty"`
	Timezone   string `json:"timezone"`
	// Start and End are a timed event; AllDayStart and AllDayEnd are an
	// all-day event with an exclusive end date. Exactly one pair is set.
	Start       *Timestamp `json:"start,omitempty"`
	End         *Timestamp `json:"end,omitempty"`
	AllDayStart *DateOnly  `json:"all_day_start,omitempty"`
	AllDayEnd   *DateOnly  `json:"all_day_end,omitempty"`
}

// CalendarUpdateEventChange patches explicitly named fields of one event.
// Fields left nil are preserved: this is never a whole-event replacement.
type CalendarUpdateEventChange struct {
	changeKind
	EventID         Handle `json:"event_id"`
	ExpectedVersion string `json:"expected_version"`
	Timezone        string `json:"timezone,omitempty"`

	Title       *string    `json:"title,omitempty"`
	Notes       *string    `json:"notes,omitempty"`
	Location    *string    `json:"location,omitempty"`
	Start       *Timestamp `json:"start,omitempty"`
	End         *Timestamp `json:"end,omitempty"`
	AllDayStart *DateOnly  `json:"all_day_start,omitempty"`
	AllDayEnd   *DateOnly  `json:"all_day_end,omitempty"`
}

// Change is one entry of a prepared change set: a tagged union whose variant
// decides which fields are even allowed to appear.
type Change struct {
	Type                ChangeType
	MailMove            *MailMoveChange
	MailSetRead         *MailSetReadChange
	MailSetFlag         *MailSetFlagChange
	MailSaveDraft       *MailSaveDraftChange
	CalendarCreateEvent *CalendarCreateEventChange
	CalendarUpdateEvent *CalendarUpdateEventChange
}

// UnmarshalJSON reads the discriminator, then decodes the variant strictly
// so a field belonging to another variant is rejected rather than ignored.
func (c *Change) UnmarshalJSON(b []byte) error {
	var kind changeKind
	if err := json.Unmarshal(b, &kind); err != nil {
		return Errorf(CodeInvalidRequest, "a change must be an object with a \"type\"")
	}
	*c = Change{Type: kind.Type}
	switch kind.Type {
	case ChangeMailMove:
		c.MailMove = &MailMoveChange{}
		return strictUnmarshal(b, c.MailMove)
	case ChangeMailSetRead:
		c.MailSetRead = &MailSetReadChange{}
		return strictUnmarshal(b, c.MailSetRead)
	case ChangeMailSetFlag:
		c.MailSetFlag = &MailSetFlagChange{}
		return strictUnmarshal(b, c.MailSetFlag)
	case ChangeMailSaveDraft:
		c.MailSaveDraft = &MailSaveDraftChange{}
		return strictUnmarshal(b, c.MailSaveDraft)
	case ChangeCalendarCreateEvent:
		c.CalendarCreateEvent = &CalendarCreateEventChange{}
		return strictUnmarshal(b, c.CalendarCreateEvent)
	case ChangeCalendarUpdateEvent:
		c.CalendarUpdateEvent = &CalendarUpdateEventChange{}
		return strictUnmarshal(b, c.CalendarUpdateEvent)
	default:
		return Errorf(CodeInvalidRequest, "unsupported change type %q", clip(string(kind.Type), 40))
	}
}

// MarshalJSON writes the active variant back, so a canonical digest and an
// approval preview see exactly the fields that were accepted.
func (c Change) MarshalJSON() ([]byte, error) {
	v := c.variant()
	if v == nil {
		return nil, Errorf(CodeInternal, "change has no variant")
	}
	return json.Marshal(v)
}

func (c Change) variant() any {
	switch {
	case c.MailMove != nil:
		return c.MailMove
	case c.MailSetRead != nil:
		return c.MailSetRead
	case c.MailSetFlag != nil:
		return c.MailSetFlag
	case c.MailSaveDraft != nil:
		return c.MailSaveDraft
	case c.CalendarCreateEvent != nil:
		return c.CalendarCreateEvent
	case c.CalendarUpdateEvent != nil:
		return c.CalendarUpdateEvent
	default:
		return nil
	}
}

// Adapter reports which application the change mutates.
func (c Change) Adapter() Adapter {
	switch c.Type {
	case ChangeMailMove, ChangeMailSetRead, ChangeMailSetFlag, ChangeMailSaveDraft:
		return AdapterMail
	case ChangeCalendarCreateEvent, ChangeCalendarUpdateEvent:
		return AdapterCalendar
	default:
		return AdapterNone
	}
}

// Handles lists every handle the change refers to, so scope enforcement and
// preview rendering never have to know the variant layout.
func (c Change) Handles() []Handle {
	var out []Handle
	switch {
	case c.MailMove != nil:
		for _, m := range c.MailMove.Messages {
			out = append(out, m.MessageID)
		}
		out = append(out, c.MailMove.DestinationMailboxID)
	case c.MailSetRead != nil:
		for _, m := range c.MailSetRead.Messages {
			out = append(out, m.MessageID)
		}
	case c.MailSetFlag != nil:
		for _, m := range c.MailSetFlag.Messages {
			out = append(out, m.MessageID)
		}
	case c.MailSaveDraft != nil:
		out = append(out, c.MailSaveDraft.SenderAccountID)
		if c.MailSaveDraft.InReplyToMessageID != "" {
			out = append(out, c.MailSaveDraft.InReplyToMessageID)
		}
	case c.CalendarCreateEvent != nil:
		out = append(out, c.CalendarCreateEvent.CalendarID)
	case c.CalendarUpdateEvent != nil:
		out = append(out, c.CalendarUpdateEvent.EventID)
	}
	return out
}

// ItemCount reports how many individual items the change affects. It feeds
// the per-plan item budget, which is separate from the tool-call budget.
func (c Change) ItemCount() int {
	switch {
	case c.MailMove != nil:
		return len(c.MailMove.Messages)
	case c.MailSetRead != nil:
		return len(c.MailSetRead.Messages)
	case c.MailSetFlag != nil:
		return len(c.MailSetFlag.Messages)
	default:
		return 1
	}
}

func (c *Change) normalize() {
	switch {
	case c.MailMove != nil:
		c.MailMove.Messages = normalizeTargets(c.MailMove.Messages)
	case c.MailSetRead != nil:
		c.MailSetRead.Messages = normalizeTargets(c.MailSetRead.Messages)
	case c.MailSetFlag != nil:
		c.MailSetFlag.Messages = normalizeTargets(c.MailSetFlag.Messages)
	case c.MailSaveDraft != nil:
		d := c.MailSaveDraft
		d.To = normalizeAddresses(d.To)
		d.Cc = normalizeAddresses(d.Cc)
		d.Bcc = normalizeAddresses(d.Bcc)
		d.Subject = strings.TrimSpace(d.Subject)
	case c.CalendarCreateEvent != nil:
		e := c.CalendarCreateEvent
		e.Title = strings.TrimSpace(e.Title)
		e.Location = strings.TrimSpace(e.Location)
		e.Timezone = strings.TrimSpace(e.Timezone)
	case c.CalendarUpdateEvent != nil:
		c.CalendarUpdateEvent.Timezone = strings.TrimSpace(c.CalendarUpdateEvent.Timezone)
	}
}

func (c Change) validate(l Limits) error {
	if c.variant() == nil {
		return Errorf(CodeInvalidRequest, "unsupported change type %q", clip(string(c.Type), 40))
	}
	switch {
	case c.MailMove != nil:
		return c.MailMove.validate(l)
	case c.MailSetRead != nil:
		return validateTargets(c.MailSetRead.Messages, l)
	case c.MailSetFlag != nil:
		return validateTargets(c.MailSetFlag.Messages, l)
	case c.MailSaveDraft != nil:
		return c.MailSaveDraft.validate(l)
	case c.CalendarCreateEvent != nil:
		return c.CalendarCreateEvent.validate(l)
	default:
		return c.CalendarUpdateEvent.validate(l)
	}
}

func (m MailMoveChange) validate(l Limits) error {
	if err := validateTargets(m.Messages, l); err != nil {
		return err
	}
	return requireHandle("destination_mailbox_id", m.DestinationMailboxID, KindMailbox)
}

func (d MailSaveDraftChange) validate(l Limits) error {
	if err := requireHandle("sender_account_id", d.SenderAccountID, KindAccount); err != nil {
		return err
	}
	if d.InReplyToMessageID != "" {
		if err := requireHandle("in_reply_to_message_id", d.InReplyToMessageID, KindMessage); err != nil {
			return err
		}
		if d.Subject != "" {
			return Errorf(CodeInvalidRequest, "a reply takes its subject and threading from the original message; omit subject")
		}
	} else {
		if d.Subject == "" {
			return Errorf(CodeInvalidRequest, "subject is required for a draft that is not a reply")
		}
		if len(d.To) == 0 {
			return Errorf(CodeInvalidRequest, "to must contain at least one recipient")
		}
	}
	if d.IncludeQuote && d.InReplyToMessageID == "" {
		return Errorf(CodeInvalidRequest, "include_quote requires in_reply_to_message_id")
	}
	if err := checkString("subject", d.Subject, l); err != nil {
		return err
	}
	if strings.TrimSpace(d.Body) == "" {
		return Errorf(CodeInvalidRequest, "body is required")
	}
	if len(d.Body) > l.MaxBodyBytes {
		return Errorf(CodeInvalidRequest, "body exceeds the %d byte limit", l.MaxBodyBytes)
	}
	for field, list := range map[string][]string{"to": d.To, "cc": d.Cc, "bcc": d.Bcc} {
		if len(list) > maxRecipients {
			return Errorf(CodeInvalidRequest, "%s carries %d recipients, the maximum is %d", field, len(list), maxRecipients)
		}
		for _, addr := range list {
			if err := checkAddress(field, addr, l); err != nil {
				return err
			}
		}
	}
	return nil
}

// maxRecipients bounds one recipient field. A draft is reviewed by a human,
// and a list nobody can read is not reviewed.
const maxRecipients = 50

func (e CalendarCreateEventChange) validate(l Limits) error {
	if err := requireHandle("calendar_id", e.CalendarID, KindCalendar); err != nil {
		return err
	}
	if strings.TrimSpace(e.Title) == "" {
		return Errorf(CodeInvalidRequest, "title is required")
	}
	for field, value := range map[string]string{"title": e.Title, "notes": e.Notes, "location": e.Location} {
		if err := checkString(field, value, l); err != nil {
			return err
		}
	}
	_, err := eventInterval(e.Timezone, e.Start, e.End, e.AllDayStart, e.AllDayEnd, true, l)
	return err
}

func (e CalendarUpdateEventChange) validate(l Limits) error {
	if err := requireHandle("event_id", e.EventID, KindEvent); err != nil {
		return err
	}
	if strings.TrimSpace(e.ExpectedVersion) == "" {
		return Errorf(CodeInvalidRequest, "expected_version is required so a changed event is not overwritten blindly")
	}
	if !printableASCII(e.ExpectedVersion) || len(e.ExpectedVersion) > 128 {
		return Errorf(CodeInvalidRequest, "expected_version is not a host-issued version")
	}
	patches := 0
	for _, s := range []*string{e.Title, e.Notes, e.Location} {
		if s != nil {
			patches++
			if err := checkString("patch field", *s, l); err != nil {
				return err
			}
		}
	}
	if e.Title != nil && strings.TrimSpace(*e.Title) == "" {
		return Errorf(CodeInvalidRequest, "title must not be blanked")
	}
	timed := e.Start != nil || e.End != nil
	allDay := e.AllDayStart != nil || e.AllDayEnd != nil
	if timed || allDay {
		patches++
		if _, err := eventInterval(e.Timezone, e.Start, e.End, e.AllDayStart, e.AllDayEnd, true, l); err != nil {
			return err
		}
	}
	if patches == 0 {
		return Errorf(CodeInvalidRequest, "the update names no field to change")
	}
	return nil
}

// eventInterval validates the timing half of a create or update and returns
// the resulting half-open interval. Exactly one of the timed pair and the
// all-day pair may be present, and both endpoints of a pair are required:
// a half-specified event is where silent duration guesses come from.
func eventInterval(timezone string, start, end *Timestamp, allDayStart, allDayEnd *DateOnly, required bool, l Limits) (Interval, error) {
	full := l.withDefaults()
	timed := start != nil || end != nil
	allDay := allDayStart != nil || allDayEnd != nil
	switch {
	case timed && allDay:
		return Interval{}, Errorf(CodeInvalidRequest, "an event is either timed or all-day, not both")
	case !timed && !allDay:
		if required {
			return Interval{}, Errorf(CodeInvalidRequest, "the event needs either start and end, or all_day_start and all_day_end")
		}
		return Interval{}, nil
	}

	loc, err := LoadZone(timezone)
	if err != nil {
		return Interval{}, err
	}
	if timed {
		if start == nil || end == nil {
			return Interval{}, Errorf(CodeInvalidRequest, "a timed event needs both start and end")
		}
		if err := ValidateOffsetAgreement("start", start.Time, loc); err != nil {
			return Interval{}, err
		}
		if err := ValidateOffsetAgreement("end", end.Time, loc); err != nil {
			return Interval{}, err
		}
		iv := Interval{Start: start.Time, End: end.Time}
		if !iv.Valid() {
			return Interval{}, Errorf(CodeInvalidRequest, "end must be after start")
		}
		if max := time.Duration(full.MaxCalendarDays) * 24 * time.Hour; iv.Duration() > max {
			return Interval{}, Errorf(CodeInvalidRequest, "the event spans more than the maximum %d days", full.MaxCalendarDays)
		}
		return iv, nil
	}
	if allDayStart == nil || allDayEnd == nil {
		return Interval{}, Errorf(CodeInvalidRequest, "an all-day event needs both all_day_start and all_day_end")
	}
	iv, err := AllDayInterval(*allDayStart, *allDayEnd, loc)
	if err != nil {
		return Interval{}, err
	}
	if max := time.Duration(full.MaxCalendarDays) * 24 * time.Hour; iv.Duration() > max {
		return Interval{}, Errorf(CodeInvalidRequest, "the event spans more than the maximum %d days", full.MaxCalendarDays)
	}
	return iv, nil
}

// normalizeTargets sorts targets by handle and drops exact duplicates so two
// equivalent change sets canonicalize identically. A repeated handle with a
// different expected version is kept: validation rejects it as a conflict
// rather than picking one.
func normalizeTargets(in []MessageTarget) []MessageTarget {
	if len(in) == 0 {
		return nil
	}
	out := make([]MessageTarget, 0, len(in))
	for _, t := range in {
		t.MessageID = Handle(strings.TrimSpace(string(t.MessageID)))
		t.ExpectedVersion = strings.TrimSpace(t.ExpectedVersion)
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MessageID == out[j].MessageID {
			return out[i].ExpectedVersion < out[j].ExpectedVersion
		}
		return out[i].MessageID < out[j].MessageID
	})
	deduped := out[:0]
	for i, t := range out {
		if i == 0 || t != out[i-1] {
			deduped = append(deduped, t)
		}
	}
	return deduped
}

func normalizeAddresses(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, a := range in {
		if trimmed := strings.TrimSpace(a); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return dedupeStrings(out)
}

func validateTargets(targets []MessageTarget, l Limits) error {
	if len(targets) == 0 {
		return Errorf(CodeInvalidRequest, "messages must name at least one message")
	}
	if len(targets) > l.MaxChangesPerPlan {
		return Errorf(CodeInvalidRequest, "messages names %d items, the maximum per change is %d", len(targets), l.MaxChangesPerPlan)
	}
	seen := make(map[Handle]bool, len(targets))
	for _, t := range targets {
		if err := requireHandle("message_id", t.MessageID, KindMessage); err != nil {
			return err
		}
		if strings.TrimSpace(t.ExpectedVersion) == "" {
			return Errorf(CodeInvalidRequest, "expected_version is required for every message")
		}
		if !printableASCII(t.ExpectedVersion) || len(t.ExpectedVersion) > 128 {
			return Errorf(CodeInvalidRequest, "expected_version is not a host-issued version")
		}
		if seen[t.MessageID] {
			return Errorf(CodeInvalidRequest, "messages names the same message twice with different expectations")
		}
		seen[t.MessageID] = true
	}
	return nil
}

// clone deep-copies a change so a prepared plan cannot be edited through a
// pointer the caller still holds. A shallow slice copy is not enough: each
// variant lives behind a pointer, and a plan whose contents can change after
// the human reviewed them is not a plan.
func (c Change) clone() Change {
	out := Change{Type: c.Type}
	switch {
	case c.MailMove != nil:
		v := *c.MailMove
		v.Messages = append([]MessageTarget(nil), c.MailMove.Messages...)
		out.MailMove = &v
	case c.MailSetRead != nil:
		v := *c.MailSetRead
		v.Messages = append([]MessageTarget(nil), c.MailSetRead.Messages...)
		out.MailSetRead = &v
	case c.MailSetFlag != nil:
		v := *c.MailSetFlag
		v.Messages = append([]MessageTarget(nil), c.MailSetFlag.Messages...)
		out.MailSetFlag = &v
	case c.MailSaveDraft != nil:
		v := *c.MailSaveDraft
		v.To = append([]string(nil), c.MailSaveDraft.To...)
		v.Cc = append([]string(nil), c.MailSaveDraft.Cc...)
		v.Bcc = append([]string(nil), c.MailSaveDraft.Bcc...)
		out.MailSaveDraft = &v
	case c.CalendarCreateEvent != nil:
		v := *c.CalendarCreateEvent
		v.Start = cloneTimestamp(c.CalendarCreateEvent.Start)
		v.End = cloneTimestamp(c.CalendarCreateEvent.End)
		v.AllDayStart = cloneDate(c.CalendarCreateEvent.AllDayStart)
		v.AllDayEnd = cloneDate(c.CalendarCreateEvent.AllDayEnd)
		out.CalendarCreateEvent = &v
	case c.CalendarUpdateEvent != nil:
		v := *c.CalendarUpdateEvent
		v.Title = cloneString(c.CalendarUpdateEvent.Title)
		v.Notes = cloneString(c.CalendarUpdateEvent.Notes)
		v.Location = cloneString(c.CalendarUpdateEvent.Location)
		v.Start = cloneTimestamp(c.CalendarUpdateEvent.Start)
		v.End = cloneTimestamp(c.CalendarUpdateEvent.End)
		v.AllDayStart = cloneDate(c.CalendarUpdateEvent.AllDayStart)
		v.AllDayEnd = cloneDate(c.CalendarUpdateEvent.AllDayEnd)
		out.CalendarUpdateEvent = &v
	}
	return out
}

func cloneString(s *string) *string {
	if s == nil {
		return nil
	}
	v := *s
	return &v
}

func cloneTimestamp(ts *Timestamp) *Timestamp {
	if ts == nil {
		return nil
	}
	v := *ts
	return &v
}

func cloneDate(d *DateOnly) *DateOnly {
	if d == nil {
		return nil
	}
	v := *d
	return &v
}

// CloneChanges deep-copies a change set. Callers that hand a change set to
// another component use it so neither side can edit the other's copy.
func CloneChanges(in []Change) []Change {
	if len(in) == 0 {
		return nil
	}
	out := make([]Change, len(in))
	for i := range in {
		out[i] = in[i].clone()
	}
	return out
}
