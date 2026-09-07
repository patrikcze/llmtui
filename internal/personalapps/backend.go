package personalapps

import (
	"context"
	"time"
)

// The interfaces below are consumer-defined: they describe exactly what the
// service needs from an adapter, and nothing more. No method takes script
// text, a command line, a bundle identifier or a file path, so no caller can
// ask an adapter to run something arbitrary. Every method takes a context
// and must honor its cancellation.
//
// Backend DTOs are deliberately separate from the model-facing views in
// results.go. Keeping them apart is what stops a native account property, an
// internal resource location or an unbounded body from reaching a prompt
// because someone added a field to a struct.

// MailBackend reads Apple Mail. Its implementations live behind the darwin
// bridge; tests inject fakes.
type MailBackend interface {
	// Accounts lists the configured mail accounts.
	Accounts(ctx context.Context) ([]BackendAccount, error)
	// Mailboxes lists the mailboxes of one account. A zero parent means the
	// account root.
	Mailboxes(ctx context.Context, account ResourceRef, parent ResourceRef) ([]BackendMailbox, error)
	// Search runs a bounded metadata query and reports its own coverage: an
	// adapter that stopped at a scan limit must say so rather than let the
	// result read as complete.
	Search(ctx context.Context, query MailQuery) (BackendMailPage, error)
	// Messages reads bounded content for explicitly selected messages.
	// Content the app cannot supply is returned unavailable, never as an
	// empty body that reads like success.
	Messages(ctx context.Context, refs []ResourceRef, maxBodyBytes int) ([]BackendMessage, error)
}

// CalendarBackend reads the macOS event store.
type CalendarBackend interface {
	// Calendars lists the calendars the store exposes.
	Calendars(ctx context.Context) ([]BackendCalendar, error)
	// Events returns the occurrences overlapping the half-open interval,
	// including occurrences of a series that started before it.
	Events(ctx context.Context, refs []ResourceRef, window Interval) ([]BackendEvent, error)
	// Event returns one event by reference.
	Event(ctx context.Context, ref ResourceRef) (BackendEvent, error)
}

// Mutator applies one approved change and reports what it observed
// afterwards. It is a separate interface from the read backends so a
// deployment can wire reads without ever wiring the ability to write.
type Mutator interface {
	// Apply executes one resolved change. It returns per-item outcomes
	// based on a readback, not on the absence of an error. An operation
	// whose effect could not be established is reported as
	// OutcomeUnknown, which must never be retried automatically.
	Apply(ctx context.Context, change ResolvedChange) ([]ItemOutcome, error)
}

// Journal records durable intent before an external side effect and the
// observed outcome after it. The service refuses to mutate anything when no
// journal is configured or when a write to it fails: an unrecorded mutation
// is one that cannot be recognized after a crash, which is how a duplicate
// send happens.
type Journal interface {
	// RecordIntent stores the intent to execute a plan and returns an error
	// if it could not be persisted durably.
	RecordIntent(ctx context.Context, plan Plan) error
	// RecordOutcome stores the observed result of a plan.
	RecordOutcome(ctx context.Context, plan Plan, outcomes []ItemOutcome) error
}

// BackendAccount is one mail account as the adapter sees it.
type BackendAccount struct {
	// Ref identifies the account for later operations.
	Ref ResourceRef
	// DisplayName is the user-visible account label. It is untrusted text.
	DisplayName string
	// LocalStore marks the account-less local mailbox store, which needs an
	// explicit scope rather than a fabricated remote account.
	LocalStore bool
}

// BackendMailbox is one mailbox as the adapter sees it.
type BackendMailbox struct {
	Ref ResourceRef
	// DisplayName is untrusted text and is not a unique identifier: two
	// mailboxes in different accounts can share a name.
	DisplayName string
	HasChildren bool
	UnreadCount int
	TotalCount  int
}

// MailQuery is the normalized, scope-checked form of a mail search. The
// service builds it from a validated request and already-resolved handles,
// so an adapter never sees a model-supplied identifier.
type MailQuery struct {
	Accounts       []ResourceRef
	Mailboxes      []ResourceRef
	ReceivedAfter  time.Time
	ReceivedBefore time.Time
	From           string
	Subject        string
	Unread         *bool
	Flagged        *bool
	// Newest orders results newest first when true.
	Newest bool
	// Limit is the number of records to return.
	Limit int
	// MaxCandidates bounds how many records the adapter may examine.
	MaxCandidates int
	// Cursor continues a previous scan. It is opaque to the model and is
	// bound to the original query by the adapter that issued it.
	Cursor string
}

// BackendMessage is one message as the adapter sees it. Body is present only
// when content was requested and available.
type BackendMessage struct {
	Ref            ResourceRef
	MailboxRef     ResourceRef
	Subject        string
	From           string
	To             []string
	Cc             []string
	Received       time.Time
	Unread         bool
	Flagged        bool
	HasAttachments bool
	AttachmentInfo []BackendAttachment

	// Body is plain text. HTML, MIME and attachments are not rendered here.
	Body string
	// BodyTruncated reports that Body was cut at the requested cap.
	BodyTruncated bool
	// BodyAvailable is false when the app could not supply content, for
	// example while offline or for an encrypted message.
	BodyAvailable bool
	// Unavailable explains a missing body in stable, content-free terms.
	Unavailable string
}

// BackendAttachment is attachment metadata only. Exporting attachment
// content is separately scoped later work.
type BackendAttachment struct {
	// Name is untrusted text and is never used as a filesystem path here.
	Name string
	Size int64
	Type string
}

// BackendMailPage is one page of search results plus honest coverage.
type BackendMailPage struct {
	Messages []BackendMessage
	// Scanned is how many candidates the adapter examined.
	Scanned int
	// Complete is true only when the whole requested space was examined.
	Complete bool
	// Reason explains an incomplete scan in stable terms.
	Reason string
	// NextCursor continues the scan, when the adapter can offer a stable
	// continuation.
	NextCursor string
}

// BackendCalendar is one calendar as the adapter sees it.
type BackendCalendar struct {
	Ref ResourceRef
	// Title is untrusted text.
	Title string
	// Source is the account or container the calendar belongs to.
	Source string
	// Writable reports whether the store accepts writes to it. A writable
	// calendar may still be shared, which the preview must disclose.
	Writable bool
	// Shared reports that other people can see events on this calendar.
	Shared bool
}

// BackendEvent is one event occurrence as the adapter sees it.
type BackendEvent struct {
	Ref         ResourceRef
	CalendarRef ResourceRef
	Title       string
	Location    string
	Notes       string
	// Interval is the occurrence's half-open span.
	Interval Interval
	// AllDay marks a date-based event whose end date is exclusive.
	AllDay bool
	// Timezone is the event's own IANA zone when it has one.
	Timezone string
	// Recurring marks an occurrence of a series; Detached marks an
	// occurrence that was edited away from the series.
	Recurring bool
	Detached  bool
	// Canceled and Tentative feed the busy/free rule: anything not clearly
	// free counts as busy.
	Canceled  bool
	Tentative bool
	// Busy reports whether the event blocks time. Unknown availability is
	// busy by default.
	Busy bool
	// HasAttendees marks an event this release refuses to write to.
	HasAttendees bool
}

// ResolvedChange pairs a validated change with the resources its handles
// resolve to, so a mutator never resolves a model-supplied identifier itself.
type ResolvedChange struct {
	// Change is a deep copy of the approved change.
	Change Change
	// Refs maps every handle in the change to its resolved resource.
	Refs map[Handle]ResourceRef
	// PlanID and PlanDigest identify the approved plan this change came
	// from, for the operation ledger.
	PlanID     string
	PlanDigest string
}

// Outcome is what was observed about one attempted item.
type Outcome string

const (
	// OutcomeApplied means a readback confirmed the intended state.
	OutcomeApplied Outcome = "applied"
	// OutcomeNotApplied means the item was deliberately not attempted or a
	// readback confirmed no change.
	OutcomeNotApplied Outcome = "not_applied"
	// OutcomeStale means observed state no longer matched the expectation.
	OutcomeStale Outcome = "stale"
	// OutcomeFailed means the attempt failed with a known cause.
	OutcomeFailed Outcome = "failed"
	// OutcomeUnknown means the effect could not be established. It blocks
	// automatic retries; a repeat needs a human and fresh preconditions.
	OutcomeUnknown Outcome = "outcome_unknown"
)

// ItemOutcome is the per-item evidence a mutation returns.
type ItemOutcome struct {
	// Target is the handle the outcome belongs to, when the item had one.
	Target Handle `json:"target,omitempty"`
	// Outcome is what was observed.
	Outcome Outcome `json:"outcome"`
	// Code classifies a failure in stable terms.
	Code Code `json:"code,omitempty"`
	// Detail is a short, content-free explanation.
	Detail string `json:"detail,omitempty"`
	// Replacement is a new handle issued when the old one stopped being
	// valid, for example after a move changed a message's identity.
	Replacement Handle `json:"replacement,omitempty"`
	// ObservedVersion is the fingerprint read back after the change.
	ObservedVersion string `json:"observed_version,omitempty"`
}

// Terminal reports whether the outcome leaves no further work for this item.
func (o Outcome) Terminal() bool {
	return o == OutcomeApplied || o == OutcomeNotApplied
}

// Uncertain reports whether the outcome forbids an automatic retry.
func (o Outcome) Uncertain() bool { return o == OutcomeUnknown }
