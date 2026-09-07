package personalapps

import "time"

// The types below are the only shapes that reach a model. They are built
// from backend DTOs by the service, which decides field by field what is
// worth exposing. Every string in them is untrusted app content: the caller
// sanitizes it for display and frames it before it enters a prompt.

// StatusView reports what the integration can currently do. It is metadata
// only: it names no account, mailbox, calendar or message, and producing it
// never launches an app or triggers a permission prompt.
type StatusView struct {
	// PlatformSupported reports whether this build can talk to the apps at
	// all.
	PlatformSupported bool `json:"platform_supported"`
	// MailEnabled and CalendarEnabled report configuration.
	MailEnabled     bool `json:"mail_enabled"`
	CalendarEnabled bool `json:"calendar_enabled"`
	// MailConnected and CalendarConnected report an explicit human connect.
	// Enabling an adapter does not connect it.
	MailConnected     bool `json:"mail_connected"`
	CalendarConnected bool `json:"calendar_connected"`
	// MutationsEnabled reports whether any change can be applied at all.
	MutationsEnabled bool `json:"mutations_enabled"`
	// PrivateSession reports that personal content has entered this session
	// and the persistence and egress restrictions are in force.
	PrivateSession bool `json:"private_session"`
	// Operations lists exactly what may be called right now.
	Operations []Operation `json:"operations"`
	// ChangeTypes lists the change variants that may currently be prepared.
	ChangeTypes []ChangeType `json:"change_types,omitempty"`
	// Notes carries short, content-free guidance, such as which adapter is
	// disconnected and how to connect it.
	Notes []string `json:"notes,omitempty"`
}

// AccountView is one mail account: identity and label, nothing else. Account
// objects carry credentials and server settings, which are never read here.
type AccountView struct {
	ID   Handle `json:"id"`
	Name string `json:"name"`
	// LocalStore marks the account-less local mailbox store.
	LocalStore bool `json:"local_store,omitempty"`
}

// MailboxView is one mailbox. Path is the hierarchy from the account root,
// because a display name alone does not identify a mailbox.
type MailboxView struct {
	ID          Handle   `json:"id"`
	Name        string   `json:"name"`
	Path        []string `json:"path"`
	HasChildren bool     `json:"has_children,omitempty"`
	Unread      int      `json:"unread,omitempty"`
	Total       int      `json:"total,omitempty"`
}

// MessageSummaryView is one search hit: metadata only, never a body.
type MessageSummaryView struct {
	ID      Handle `json:"id"`
	Version string `json:"version"`
	// Mailbox is where the message was observed. A move invalidates it.
	Mailbox        Handle    `json:"mailbox"`
	Subject        string    `json:"subject"`
	From           string    `json:"from"`
	Received       time.Time `json:"received"`
	Unread         bool      `json:"unread"`
	Flagged        bool      `json:"flagged,omitempty"`
	HasAttachments bool      `json:"has_attachments,omitempty"`
}

// MessageView is one message with bounded content.
type MessageView struct {
	ID       Handle    `json:"id"`
	Version  string    `json:"version"`
	Subject  string    `json:"subject"`
	From     string    `json:"from"`
	To       []string  `json:"to,omitempty"`
	Cc       []string  `json:"cc,omitempty"`
	Received time.Time `json:"received"`
	Unread   bool      `json:"unread"`
	// Body is plain text, truncated at the requested cap.
	Body string `json:"body,omitempty"`
	// BodyTruncated and BodyAvailable keep a missing or cut body from
	// reading like a short message.
	BodyTruncated bool `json:"body_truncated,omitempty"`
	BodyAvailable bool `json:"body_available"`
	// Unavailable explains a missing body in stable terms.
	Unavailable string `json:"unavailable,omitempty"`
	// Attachments is metadata only.
	Attachments []AttachmentView `json:"attachments,omitempty"`
}

// AttachmentView is attachment metadata. There is no content and no path.
type AttachmentView struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Type string `json:"type,omitempty"`
}

// CalendarView is one calendar with the facts a write decision needs.
type CalendarView struct {
	ID       Handle `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source,omitempty"`
	Writable bool   `json:"writable"`
	Shared   bool   `json:"shared,omitempty"`
}

// EventView is one occurrence. Recurrence and detachment are reported so a
// caller cannot mistake an occurrence for a standalone event.
type EventView struct {
	ID       Handle `json:"id"`
	Version  string `json:"version"`
	Calendar Handle `json:"calendar"`
	Title    string `json:"title"`
	Location string `json:"location,omitempty"`
	Notes    string `json:"notes,omitempty"`
	// Start and End are the half-open occurrence span.
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// AllDay marks a date-based event; its end date is exclusive.
	AllDay bool `json:"all_day,omitempty"`
	// Timezone is the event's own zone when it differs from the request's.
	Timezone     string `json:"timezone,omitempty"`
	Recurring    bool   `json:"recurring,omitempty"`
	Detached     bool   `json:"detached,omitempty"`
	Canceled     bool   `json:"canceled,omitempty"`
	Tentative    bool   `json:"tentative,omitempty"`
	Busy         bool   `json:"busy"`
	HasAttendees bool   `json:"has_attendees,omitempty"`
}

// FreeSlotView is one computed gap.
type FreeSlotView struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	// Minutes is the slot length, so a caller does not have to subtract
	// timestamps to compare slots.
	Minutes int `json:"minutes"`
}

// FreeSlotsView carries the slots together with the limits of the answer.
type FreeSlotsView struct {
	Timezone string         `json:"timezone"`
	Slots    []FreeSlotView `json:"slots"`
	// Limitations states plainly what the computation did not consider, so
	// a free slot is never reported as a guarantee.
	Limitations []string `json:"limitations,omitempty"`
}

// PlanView is the bounded preview a model sees for a prepared plan. The full
// reviewable detail goes to the human, not here.
type PlanView struct {
	PlanID string `json:"plan_id"`
	// Digest lets a caller notice that a re-prepared plan is the same one.
	Digest    string    `json:"digest"`
	Adapters  []Adapter `json:"adapters"`
	ItemCount int       `json:"item_count"`
	ExpiresAt time.Time `json:"expires_at"`
	// Summary lists one short line per change, in plan order.
	Summary []string `json:"summary"`
	// RequiresApproval is always true: possessing a plan ID is not
	// authorization, and applying one always goes through the human.
	RequiresApproval bool `json:"requires_approval"`
}

// ApplyView is the result of applying a plan: per-item evidence, never a
// bare success flag.
type ApplyView struct {
	PlanID   string        `json:"plan_id"`
	Digest   string        `json:"digest"`
	Outcomes []ItemOutcome `json:"outcomes"`
	// Applied, Failed and Unknown summarize the outcomes.
	Applied int `json:"applied"`
	Failed  int `json:"failed"`
	Unknown int `json:"unknown"`
}

// OpenView confirms a navigation request.
type OpenView struct {
	ID     Handle `json:"id"`
	Opened bool   `json:"opened"`
}
