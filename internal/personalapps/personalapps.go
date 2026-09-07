// Package personalapps defines the pure-Go domain contract for llmtui's
// optional Apple Mail and Apple Calendar integration: the operation
// vocabulary a model may call, the immutable request/result envelopes, the
// opaque resource handles, the capability/scope policy, and the
// prepare-approve-apply plan lifecycle.
//
// The package contains no UI, no provider access, no configuration parsing
// and no process launching. It must never import internal/tui,
// internal/provider, internal/config or internal/tools; the composition root
// maps configuration onto Options and injects backends. Backends are
// consumer-defined interfaces (MailBackend, CalendarBackend) so the domain
// logic is testable without contacting Apple Mail, Apple Calendar or any
// macOS permission prompt. Constructing a Service never launches an app,
// never starts a helper process and never requests an OS permission.
//
// Everything reachable through this package is untrusted data. Mail and
// calendar text is user content that may carry terminal control sequences or
// prompt-injection attempts; callers render it through
// internal/terminaltext.Sanitize and frame it with internal/untrusted.Frame
// before it reaches a prompt. This package deliberately does not do that
// framing itself: it does not know which of its results are display-bound.
//
// Scope of the current implementation: native transports (JXA for Mail and
// an explicitly configured EventKit companion for Calendar), request/plan
// contracts, validation, normalization, identity, temporal logic, policy,
// durable mutation recovery, and approved mutations with readback. Calendar
// companion configuration can be inspected passively, but constructing a
// Service or diagnostic must never prompt for macOS access; the person must
// explicitly connect an adapter before a real operation can launch it.
package personalapps

import "time"

// Version is the schema version stamped on every Result envelope. Bump it
// only for a change that an already-shipped consumer could misread.
const Version = 1

// Clock supplies the current time. Every time-dependent component in this
// package takes one so expiry, ambiguity and freshness are testable without
// sleeping. A nil Clock means time.Now.
type Clock func() time.Time

func (c Clock) now() time.Time {
	if c == nil {
		return time.Now()
	}
	return c()
}

// Operation is the closed set of actions a model may request. Anything
// outside this set is rejected before a backend is consulted.
type Operation string

const (
	// OpStatus reports already-known capability and connection state. It
	// never prompts for a permission and never launches an app.
	OpStatus Operation = "status"
	// OpMailAccounts lists in-scope mail accounts as identity and label only.
	OpMailAccounts Operation = "mail_accounts"
	// OpMailMailboxes lists mailbox metadata within one in-scope account.
	OpMailMailboxes Operation = "mail_mailboxes"
	// OpMailSearch runs a bounded structured metadata search. It carries no
	// free-form predicate and no script text.
	OpMailSearch Operation = "mail_search"
	// OpMailRead reads bounded content for explicitly selected messages.
	OpMailRead Operation = "mail_read"
	// OpCalendarList lists in-scope calendars with writability.
	OpCalendarList Operation = "calendar_list"
	// OpCalendarEvents lists occurrences overlapping a half-open interval.
	OpCalendarEvents Operation = "calendar_events"
	// OpCalendarEvent reads one selected event with its observed version.
	OpCalendarEvent Operation = "calendar_event"
	// OpCalendarFreeSlots computes deterministic local gaps from observed
	// busy intervals in the selected calendars only.
	OpCalendarFreeSlots Operation = "calendar_free_slots"
	// OpChangePrepare validates a bounded change set and returns an
	// immutable preview. It performs no external write of any kind.
	OpChangePrepare Operation = "change_prepare"
	// OpChangeApply executes one host-issued plan that a human approved.
	OpChangeApply Operation = "change_apply"
	// OpOpenItem navigates the owning app to an existing item. It is not a
	// general URL or path opener.
	OpOpenItem Operation = "open_item"
)

// operations lists every valid Operation in a stable order.
var operations = []Operation{
	OpStatus,
	OpMailAccounts,
	OpMailMailboxes,
	OpMailSearch,
	OpMailRead,
	OpCalendarList,
	OpCalendarEvents,
	OpCalendarEvent,
	OpCalendarFreeSlots,
	OpChangePrepare,
	OpChangeApply,
	OpOpenItem,
}

// Operations returns every valid Operation in a stable order.
func Operations() []Operation {
	out := make([]Operation, len(operations))
	copy(out, operations)
	return out
}

// Valid reports whether op is part of the closed operation vocabulary.
func (o Operation) Valid() bool {
	for _, known := range operations {
		if o == known {
			return true
		}
	}
	return false
}

func (o Operation) String() string { return string(o) }

// Adapter names the personal application an operation talks to. It is the
// unit of enablement, connection and scope.
type Adapter string

const (
	AdapterMail     Adapter = "mail"
	AdapterCalendar Adapter = "calendar"
	// AdapterNone marks host-only operations such as status and
	// change_apply, whose adapter is decided by the plan they carry.
	AdapterNone Adapter = ""
)

// Adapter reports which application the operation reads or mutates.
// change_apply returns AdapterNone because its adapters come from the
// approved plan rather than from the model's arguments.
func (o Operation) Adapter() Adapter {
	switch o {
	case OpMailAccounts, OpMailMailboxes, OpMailSearch, OpMailRead:
		return AdapterMail
	case OpCalendarList, OpCalendarEvents, OpCalendarEvent, OpCalendarFreeSlots:
		return AdapterCalendar
	default:
		return AdapterNone
	}
}

// Effect classifies what an operation can do to the outside world. It is a
// conservative static classification; per-plan policy remains authoritative
// for what change_apply is actually allowed to execute.
type Effect string

const (
	// EffectMetadata reads host-side or app-side metadata only.
	EffectMetadata Effect = "metadata"
	// EffectRead reads personal content.
	EffectRead Effect = "read"
	// EffectNavigate causes a visible, user-triggered UI effect only.
	EffectNavigate Effect = "navigate"
	// EffectMutate can change state in a personal application.
	EffectMutate Effect = "mutate"
)

// Effect returns the operation's conservative static effect.
func (o Operation) Effect() Effect {
	switch o {
	case OpStatus:
		return EffectMetadata
	case OpMailAccounts, OpMailMailboxes, OpMailSearch, OpCalendarList:
		return EffectMetadata
	case OpMailRead, OpCalendarEvents, OpCalendarEvent, OpCalendarFreeSlots, OpChangePrepare:
		return EffectRead
	case OpOpenItem:
		return EffectNavigate
	case OpChangeApply:
		return EffectMutate
	default:
		return EffectMutate
	}
}

// Status is the outcome category of a completed operation. It is reported
// even when data is returned, so a caller can never read a partial or
// uncertain result as a complete success.
type Status string

const (
	// StatusOK means the operation completed within its requested bounds.
	StatusOK Status = "ok"
	// StatusPartial means the result is incomplete but honest about it.
	StatusPartial Status = "partial"
	// StatusDenied means an OS permission or a configured scope refused it.
	StatusDenied Status = "denied"
	// StatusUnsupported means the platform, app or adapter cannot do it.
	StatusUnsupported Status = "unsupported"
	// StatusStale means a referenced handle, version or plan no longer
	// matches observed state.
	StatusStale Status = "stale"
	// StatusTimeout means the deadline expired with no observed effect.
	StatusTimeout Status = "timeout"
	// StatusOutcomeUnknown means a mutation may or may not have happened.
	// It never authorizes an automatic retry.
	StatusOutcomeUnknown Status = "outcome_unknown"
	// StatusError is any other reported failure.
	StatusError Status = "error"
)

// Coverage states exactly how much of the requested space was examined. It
// is mandatory on every result that scans: a caller must be able to tell a
// complete answer from a bounded sample.
type Coverage struct {
	// Scanned is how many candidate records the adapter examined.
	Scanned int `json:"scanned"`
	// Returned is how many records this page carries.
	Returned int `json:"returned"`
	// Complete is true only when the whole requested space was examined.
	Complete bool `json:"complete"`
	// Reason explains an incomplete scan, e.g. "scan_limit".
	Reason string `json:"reason,omitempty"`
	// BodyScope records whether message bodies were fetched, e.g.
	// "not_requested", "selected", "unavailable".
	BodyScope string `json:"body_scope,omitempty"`
}

// Result is the envelope every operation returns to the controller. Data is
// the operation-specific payload; it is always a value defined by this
// package, never a raw backend DTO.
type Result struct {
	Version    int       `json:"version"`
	Operation  Operation `json:"operation"`
	Status     Status    `json:"status"`
	ObservedAt time.Time `json:"observed_at"`
	Data       any       `json:"data,omitempty"`
	Coverage   *Coverage `json:"coverage,omitempty"`
	NextCursor string    `json:"next_cursor,omitempty"`
	Warnings   []string  `json:"warnings,omitempty"`
	// Error carries the stable failure classification when Status is not
	// StatusOK or StatusPartial. Its message never contains message bodies,
	// script payloads, credentials or private file paths.
	Error *ResultError `json:"error,omitempty"`
}

// ResultError is the model-facing form of a failure.
type ResultError struct {
	Code    Code   `json:"code"`
	Message string `json:"message"`
}
