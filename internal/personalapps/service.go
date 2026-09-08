package personalapps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Navigator opens an existing item in its owning application. It is a
// separate, tiny interface because navigation is a visible UI effect the
// user asked for, not a general "open this URL or path" capability.
type Navigator interface {
	Open(ctx context.Context, ref ResourceRef) error
}

// ApprovalChecker reports whether a human approved one exact plan. The host
// owns approval; this package never decides it. With no checker wired,
// applying is refused, so a build that forgot to connect the approval UI
// cannot mutate anything.
type ApprovalChecker interface {
	ApprovedPlan(planID, digest string) bool
}

// Options configures a Service. The composition root maps configuration and
// wiring onto it; nothing here is reachable from a model request.
type Options struct {
	// Scope is the user's configured authorization.
	Scope Scope
	// Limits bounds every request. Zero fields take package defaults.
	Limits Limits
	// Mail, Calendar, Mutator, Navigator and Journal are injected adapters.
	// A nil adapter makes its operations report unsupported rather than
	// failing at call time.
	Mail      MailBackend
	Calendar  CalendarBackend
	Mutator   Mutator
	Navigator Navigator
	Journal   Journal
	// Approvals reports human approval of a specific plan.
	Approvals ApprovalChecker
	// PrivacyGate is called before any operation that can surface personal
	// content. The host uses it to enter the private session mode that
	// disables transcript saving, response caching, memory capture and
	// derived indexing. Returning an error refuses the read.
	//
	// It is required whenever a backend is wired: personal content must not
	// be readable before the persistence and egress restrictions are on.
	PrivacyGate func(context.Context) error
	// Now supplies the current time.
	Now Clock
}

// Service is the personal-apps domain entry point. It validates, enforces
// scope, resolves handles, calls injected adapters and shapes bounded
// results. It launches nothing, prompts for nothing and holds no native
// resource; adapters own all of that.
//
// A Service is safe for concurrent use, but it deliberately serializes
// adapter work: at most one external operation runs at a time, because the
// apps behind it are not transactional and concurrent automation of the same
// mailbox is how two changes race each other.
type Service struct {
	opts   Options
	limits Limits

	// adapter serializes external work.
	adapter sync.Mutex

	mu    sync.RWMutex
	conn  ConnectionState
	scope Scope

	registry *Registry
	plans    *PlanStore
}

// New validates the options and returns a Service. It performs no I/O, does
// not launch an application and does not request any permission, so
// constructing one from configuration is always safe.
func New(opts Options) (*Service, error) {
	limits := opts.Limits.withDefaults()
	if err := opts.Limits.Validate(); err != nil {
		return nil, err
	}
	if (opts.Mail != nil || opts.Calendar != nil) && opts.PrivacyGate == nil {
		return nil, Errorf(CodeInternal, "a privacy gate is required before any personal data adapter is wired")
	}
	return &Service{
		opts:   opts,
		limits: limits,
		scope:  opts.Scope.Normalize(),
		registry: NewRegistry(RegistryOptions{
			TTL: limits.PlanTTL * 6,
			Now: opts.Now,
		}),
		plans: NewPlanStore(PlanStoreOptions{TTL: limits.PlanTTL, Max: limits.MaxActivePlans, Now: opts.Now}),
	}, nil
}

// PlatformSupported reports whether this build can reach the personal apps.
// A wired adapter counts as support so tests and alternative hosts do not
// depend on the build platform.
func (s *Service) PlatformSupported() bool {
	return platformSupported() || s.opts.Mail != nil || s.opts.Calendar != nil
}

// Scope returns the current authorization.
func (s *Service) Scope() Scope {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scope
}

// SetScope replaces the authorization, for example after the human edited it
// through the scope command. Narrowing it invalidates outstanding handles and
// prepared plans, so a request in flight cannot use a resource the user just
// removed.
func (s *Service) SetScope(scope Scope) {
	s.mu.Lock()
	s.scope = scope.Normalize()
	s.mu.Unlock()
	s.registry.Reset()
	s.plans.Reset()
}

// Connection returns the current connection state.
func (s *Service) Connection() ConnectionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.conn
}

// Connect records that the human explicitly connected an adapter. The
// permission flow itself belongs to the adapter and the host UI; this only
// records the decision.
func (s *Service) Connect(a Adapter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a {
	case AdapterMail:
		if !s.scope.MailEnabled {
			return ErrDisabled
		}
		s.conn.MailConnected = true
	case AdapterCalendar:
		if !s.scope.CalendarEnabled {
			return ErrDisabled
		}
		s.conn.CalendarConnected = true
	default:
		return Errorf(CodeInvalidRequest, "unknown adapter %q", clip(string(a), 32))
	}
	return nil
}

// Disconnect drops an adapter's connection and invalidates every handle and
// prepared plan. The private-session marking deliberately survives: text
// already in the conversation does not become safe to persist because an app
// was disconnected afterwards.
func (s *Service) Disconnect(a Adapter) {
	s.mu.Lock()
	switch a {
	case AdapterMail:
		s.conn.MailConnected = false
	case AdapterCalendar:
		s.conn.CalendarConnected = false
	}
	s.mu.Unlock()
	s.registry.Reset()
	s.plans.Reset()
}

// Status returns the metadata-only capability report.
func (s *Service) Status() StatusView {
	s.mu.RLock()
	scope, conn := s.scope, s.conn
	s.mu.RUnlock()

	supported := s.PlatformSupported()
	view := StatusView{
		PlatformSupported: supported,
		MailEnabled:       scope.MailEnabled,
		CalendarEnabled:   scope.CalendarEnabled,
		MailConnected:     conn.MailConnected,
		CalendarConnected: conn.CalendarConnected,
		MutationsEnabled:  scope.MutationsEnabled,
		PrivateSession:    conn.PrivateSession,
		Operations:        scope.AllowedOperations(conn, supported),
		ChangeTypes:       scope.AllowedChangeTypes(conn),
	}
	if !supported {
		view.Notes = append(view.Notes, "this build cannot reach Apple Mail or Apple Calendar")
		return view
	}
	if scope.MailEnabled && !conn.MailConnected {
		view.Notes = append(view.Notes, "mail is enabled but not connected; connect it explicitly to grant access")
	}
	if scope.CalendarEnabled && !conn.CalendarConnected {
		view.Notes = append(view.Notes, "calendar is enabled but not connected; connect it explicitly to grant access")
	}
	if scope.MailEnabled && len(scope.AllowedAccounts) == 0 {
		view.Notes = append(view.Notes, "no mail account is in scope, so no mail can be read")
	}
	if scope.CalendarEnabled && len(scope.AllowedCalendars) == 0 {
		view.Notes = append(view.Notes, "no calendar is in scope, so no event can be read")
	}
	if !scope.MutationsEnabled {
		view.Notes = append(view.Notes, "mutations are disabled; changes cannot be prepared or applied")
	}
	return view
}

// Execute runs one validated request and always returns a Result. It never
// returns a bare Go error: every failure is a Result carrying a Status and a
// stable Code, so a caller cannot accidentally render a failure as success.
func (s *Service) Execute(ctx context.Context, req Request) Result {
	if !req.Valid() {
		return s.fail(req.Operation(), Errorf(CodeInvalidRequest, "the request was not validated"))
	}
	op := req.Operation()

	s.mu.RLock()
	scope, conn := s.scope, s.conn
	s.mu.RUnlock()

	if !s.PlatformSupported() && op != OpStatus {
		return s.fail(op, ErrUnsupportedPlatform)
	}
	if err := scope.CheckOperation(op, conn); err != nil {
		return s.fail(op, err)
	}
	if op == OpStatus {
		return s.ok(op, s.Status(), nil, "", nil)
	}
	// Everything past this point can surface personal content, so the
	// privacy gate runs before any adapter is touched.
	if err := s.enterPrivate(ctx); err != nil {
		return s.fail(op, err)
	}

	s.adapter.Lock()
	defer s.adapter.Unlock()

	switch args := req.Arguments().(type) {
	case *MailAccountsArgs:
		return s.mailAccounts(ctx, *args, scope)
	case *MailMailboxesArgs:
		return s.mailMailboxes(ctx, *args, scope)
	case *MailSearchArgs:
		return s.mailSearch(ctx, *args, scope)
	case *MailReadArgs:
		return s.mailRead(ctx, *args, scope)
	case *CalendarListArgs:
		return s.calendarList(ctx, *args, scope)
	case *CalendarEventsArgs:
		return s.calendarEvents(ctx, *args, scope)
	case *CalendarEventArgs:
		return s.calendarEvent(ctx, *args, scope)
	case *CalendarFreeSlotsArgs:
		return s.calendarFreeSlots(ctx, *args, scope)
	case *ChangePrepareArgs:
		return s.changePrepare(*args, scope, conn)
	case *ChangeApplyArgs:
		return s.changeApply(ctx, *args, scope, conn)
	case *OpenItemArgs:
		return s.openItem(ctx, *args, scope)
	default:
		return s.fail(op, Errorf(CodeInternal, "operation %q has no executor", op))
	}
}

// ExecuteRaw parses raw against this Service's own configured limits and
// executes it. It is the entry point for a caller (the tools/native
// protocol layers) that only has the wire bytes: those layers must not
// reimplement or duplicate this package's decoding rules, so they hand raw
// bytes here rather than calling ParseRequest themselves.
//
// Like Execute, it always returns a Result and never a bare Go error — a
// request too malformed to name a valid operation still gets a Result with
// Operation "" and a stable CodeInvalidRequest.
func (s *Service) ExecuteRaw(ctx context.Context, raw []byte) Result {
	req, err := ParseRequest(raw, s.limits)
	if err != nil {
		return s.fail("", err)
	}
	return s.Execute(ctx, req)
}

// PreparedPlan returns the bounded preview for an already-prepared plan,
// without consuming it. It exists so a host UI can render the same summary
// change_prepare returned to the model when it asks the human to approve
// that plan — the human's approval decision must see the actual plan, not
// take the model's word for what it prepared.
func (s *Service) PreparedPlan(id string) (PlanView, error) {
	plan, err := s.plans.Get(id)
	if err != nil {
		return PlanView{}, err
	}
	return PlanView{
		PlanID:           plan.ID,
		Digest:           plan.Digest,
		Adapters:         plan.Adapters,
		ItemCount:        plan.ItemCount,
		ExpiresAt:        plan.ExpiresAt,
		Summary:          summarize(plan.Changes()),
		RequiresApproval: true,
	}, nil
}

// enterPrivate marks the session private and lets the host apply its
// persistence and egress restrictions before any content is read.
func (s *Service) enterPrivate(ctx context.Context) error {
	if s.opts.PrivacyGate == nil {
		return Errorf(CodeInternal, "the privacy gate is not wired; refusing to read personal data")
	}
	if err := s.opts.PrivacyGate(ctx); err != nil {
		var typed *Error
		if errors.As(err, &typed) {
			return typed
		}
		return wrap(&Error{Code: CodePermissionDenied, Message: "the private session could not be established"}, err)
	}
	s.mu.Lock()
	s.conn.PrivateSession = true
	s.mu.Unlock()
	return nil
}

func (s *Service) mailAccounts(ctx context.Context, args MailAccountsArgs, scope Scope) Result {
	if s.opts.Mail == nil {
		return s.fail(OpMailAccounts, ErrUnsupportedPlatform)
	}
	accounts, err := s.opts.Mail.Accounts(ctx)
	if err != nil {
		return s.fail(OpMailAccounts, err)
	}
	size := args.Size(s.limits)
	views := make([]AccountView, 0, len(accounts))
	for _, a := range accounts {
		if !scope.AllowsRef(a.Ref) {
			continue
		}
		if len(views) >= size {
			break
		}
		h, err := s.registry.Mint(a.Ref)
		if err != nil {
			return s.fail(OpMailAccounts, err)
		}
		views = append(views, AccountView{ID: h, Name: a.DisplayName, LocalStore: a.LocalStore})
	}
	return s.ok(OpMailAccounts, map[string]any{"accounts": views}, pageCoverage(len(accounts), len(views)), "", nil)
}

func (s *Service) mailMailboxes(ctx context.Context, args MailMailboxesArgs, scope Scope) Result {
	if s.opts.Mail == nil {
		return s.fail(OpMailMailboxes, ErrUnsupportedPlatform)
	}
	account, err := s.resolve(args.AccountID, KindAccount, scope)
	if err != nil {
		return s.fail(OpMailMailboxes, err)
	}
	var parent ResourceRef
	if args.ParentID != "" {
		parent, err = s.resolve(args.ParentID, KindMailbox, scope)
		if err != nil {
			return s.fail(OpMailMailboxes, err)
		}
	}
	boxes, err := s.opts.Mail.Mailboxes(ctx, account, parent)
	if err != nil {
		return s.fail(OpMailMailboxes, err)
	}
	size := args.Size(s.limits)
	views := make([]MailboxView, 0, len(boxes))
	for _, b := range boxes {
		if !scope.AllowsRef(b.Ref) {
			continue
		}
		if len(views) >= size {
			break
		}
		h, err := s.registry.Mint(b.Ref)
		if err != nil {
			return s.fail(OpMailMailboxes, err)
		}
		views = append(views, MailboxView{
			ID:          h,
			Name:        b.DisplayName,
			Path:        append([]string(nil), b.Ref.ContainerPath...),
			HasChildren: b.HasChildren,
			Unread:      b.UnreadCount,
			Total:       b.TotalCount,
		})
	}
	return s.ok(OpMailMailboxes, map[string]any{"mailboxes": views}, pageCoverage(len(boxes), len(views)), "", nil)
}

func (s *Service) mailSearch(ctx context.Context, args MailSearchArgs, scope Scope) Result {
	if s.opts.Mail == nil {
		return s.fail(OpMailSearch, ErrUnsupportedPlatform)
	}
	query := MailQuery{
		From:          args.From,
		Subject:       args.Subject,
		Unread:        args.Unread,
		Flagged:       args.Flagged,
		Newest:        args.Sort != SortReceivedAsc,
		Limit:         args.Size(s.limits),
		MaxCandidates: s.limits.MaxScanCandidates,
		Cursor:        args.Cursor,
	}
	if args.ReceivedAfter != nil {
		query.ReceivedAfter = args.ReceivedAfter.Time
	}
	if args.ReceivedBefore != nil {
		query.ReceivedBefore = args.ReceivedBefore.Time
	}
	for _, h := range args.AccountIDs {
		ref, err := s.resolve(h, KindAccount, scope)
		if err != nil {
			return s.fail(OpMailSearch, err)
		}
		query.Accounts = append(query.Accounts, ref)
	}
	for _, h := range args.MailboxIDs {
		ref, err := s.resolve(h, KindMailbox, scope)
		if err != nil {
			return s.fail(OpMailSearch, err)
		}
		query.Mailboxes = append(query.Mailboxes, ref)
	}

	page, err := s.opts.Mail.Search(ctx, query)
	if err != nil {
		return s.fail(OpMailSearch, err)
	}
	views := make([]MessageSummaryView, 0, len(page.Messages))
	var warnings []string
	for _, m := range page.Messages {
		// Re-check scope on what came back: an adapter must not be able to
		// widen the request by returning something outside it.
		if !scope.AllowsRef(m.Ref) {
			warnings = appendOnce(warnings, "some results were outside the approved scope and were dropped")
			continue
		}
		if len(views) >= query.Limit {
			break
		}
		summary, err := s.messageSummary(m)
		if err != nil {
			return s.fail(OpMailSearch, err)
		}
		views = append(views, summary)
	}
	coverage := &Coverage{
		Scanned:   page.Scanned,
		Returned:  len(views),
		Complete:  page.Complete && len(views) == len(page.Messages),
		Reason:    page.Reason,
		BodyScope: "not_requested",
	}
	if !coverage.Complete && coverage.Reason == "" {
		coverage.Reason = "scan_limit"
	}
	status := StatusOK
	if !coverage.Complete {
		status = StatusPartial
	}
	res := s.ok(OpMailSearch, map[string]any{"messages": views}, coverage, page.NextCursor, warnings)
	res.Status = status
	return res
}

func (s *Service) messageSummary(m BackendMessage) (MessageSummaryView, error) {
	ref := m.Ref
	ref.Fingerprint = messageFingerprint(m)
	h, err := s.registry.Mint(ref)
	if err != nil {
		return MessageSummaryView{}, err
	}
	box, err := s.registry.Mint(m.MailboxRef)
	if err != nil {
		return MessageSummaryView{}, err
	}
	return MessageSummaryView{
		ID:             h,
		Version:        ref.Fingerprint,
		Mailbox:        box,
		Subject:        m.Subject,
		From:           m.From,
		Received:       m.Received,
		Unread:         m.Unread,
		Flagged:        m.Flagged,
		HasAttachments: m.HasAttachments,
	}, nil
}

// messageFingerprint digests the fields a mutation depends on, so a message
// that moved, was read or was flagged since the handle was issued fails its
// precondition instead of being changed on a stale assumption.
func messageFingerprint(m BackendMessage) string {
	return Fingerprint(
		m.Ref.AccountID,
		strings.Join(m.Ref.ContainerPath, "/"),
		m.Ref.NativeID,
		m.Received.UTC().Format(time.RFC3339Nano),
		fmt.Sprintf("unread=%t", m.Unread),
		fmt.Sprintf("flagged=%t", m.Flagged),
	)
}

func (s *Service) mailRead(ctx context.Context, args MailReadArgs, scope Scope) Result {
	if s.opts.Mail == nil {
		return s.fail(OpMailRead, ErrUnsupportedPlatform)
	}
	refs := make([]ResourceRef, 0, len(args.MessageIDs))
	for _, h := range args.MessageIDs {
		ref, err := s.resolve(h, KindMessage, scope)
		if err != nil {
			return s.fail(OpMailRead, err)
		}
		refs = append(refs, ref)
	}
	bodyCap := args.BodyBytes(s.limits)
	messages, err := s.opts.Mail.Messages(ctx, refs, bodyCap)
	if err != nil {
		return s.fail(OpMailRead, err)
	}

	views := make([]MessageView, 0, len(messages))
	var warnings []string
	unavailable := 0
	for _, m := range messages {
		if !scope.AllowsRef(m.Ref) {
			warnings = appendOnce(warnings, "some results were outside the approved scope and were dropped")
			continue
		}
		ref := m.Ref
		ref.Fingerprint = messageFingerprint(m)
		h, err := s.registry.Mint(ref)
		if err != nil {
			return s.fail(OpMailRead, err)
		}
		body, truncated := clipBody(m.Body, bodyCap)
		if m.BodyTruncated {
			truncated = true
		}
		if !m.BodyAvailable {
			unavailable++
			body = ""
		}
		view := MessageView{
			ID:            h,
			Version:       ref.Fingerprint,
			Subject:       m.Subject,
			From:          m.From,
			To:            append([]string(nil), m.To...),
			Cc:            append([]string(nil), m.Cc...),
			Received:      m.Received,
			Unread:        m.Unread,
			Body:          body,
			BodyTruncated: truncated,
			BodyAvailable: m.BodyAvailable,
			Unavailable:   m.Unavailable,
		}
		for _, a := range m.AttachmentInfo {
			// The two shapes coincide today. If either gains a field, this
			// conversion stops compiling, which is the point: what reaches
			// a model must be decided deliberately, not inherited.
			view.Attachments = append(view.Attachments, AttachmentView(a))
		}
		views = append(views, view)
	}

	bodyScope := "selected"
	if unavailable > 0 {
		bodyScope = "partially_unavailable"
		warnings = appendOnce(warnings, "some message bodies were unavailable and are reported as such, not as empty")
	}
	coverage := &Coverage{
		Scanned:   len(args.MessageIDs),
		Returned:  len(views),
		Complete:  len(views) == len(args.MessageIDs) && unavailable == 0,
		BodyScope: bodyScope,
	}
	if !coverage.Complete && coverage.Reason == "" {
		coverage.Reason = "content_unavailable"
	}
	res := s.ok(OpMailRead, map[string]any{"messages": views}, coverage, "", warnings)
	if !coverage.Complete {
		res.Status = StatusPartial
	}
	return res
}

func (s *Service) calendarList(ctx context.Context, args CalendarListArgs, scope Scope) Result {
	if s.opts.Calendar == nil {
		return s.fail(OpCalendarList, ErrUnsupportedPlatform)
	}
	calendars, err := s.opts.Calendar.Calendars(ctx)
	if err != nil {
		return s.fail(OpCalendarList, err)
	}
	size := args.Size(s.limits)
	views := make([]CalendarView, 0, len(calendars))
	for _, c := range calendars {
		if !scope.AllowsRef(c.Ref) {
			continue
		}
		if len(views) >= size {
			break
		}
		h, err := s.registry.Mint(c.Ref)
		if err != nil {
			return s.fail(OpCalendarList, err)
		}
		views = append(views, CalendarView{ID: h, Name: c.Title, Source: c.Source, Writable: c.Writable, Shared: c.Shared})
	}
	if len(calendars) > 0 && len(views) == 0 {
		return s.fail(OpCalendarList, Errorf(CodeScopeDenied, "configured calendar identifiers did not match any calendar available in EventKit"))
	}
	return s.ok(OpCalendarList, map[string]any{"calendars": views}, pageCoverage(len(calendars), len(views)), "", nil)
}

func (s *Service) calendarEvents(ctx context.Context, args CalendarEventsArgs, scope Scope) Result {
	events, _, err := s.readEvents(ctx, args.CalendarIDs, args.Start.Time, args.End.Time, scope)
	if err != nil {
		return s.fail(OpCalendarEvents, err)
	}
	size := args.Size(s.limits)
	views := make([]EventView, 0, len(events))
	for _, e := range events {
		if len(views) >= size {
			break
		}
		view, err := s.eventView(e)
		if err != nil {
			return s.fail(OpCalendarEvents, err)
		}
		views = append(views, view)
	}
	res := s.ok(OpCalendarEvents, map[string]any{"events": views}, pageCoverage(len(events), len(views)), "", nil)
	if len(views) < len(events) {
		res.Status = StatusPartial
	}
	return res
}

func (s *Service) calendarEvent(ctx context.Context, args CalendarEventArgs, scope Scope) Result {
	if s.opts.Calendar == nil {
		return s.fail(OpCalendarEvent, ErrUnsupportedPlatform)
	}
	ref, err := s.resolve(args.EventID, KindEvent, scope)
	if err != nil {
		return s.fail(OpCalendarEvent, err)
	}
	event, err := s.opts.Calendar.Event(ctx, ref)
	if err != nil {
		return s.fail(OpCalendarEvent, err)
	}
	if !scope.AllowsRef(event.Ref) {
		return s.fail(OpCalendarEvent, ErrScopeDenied)
	}
	view, err := s.eventView(event)
	if err != nil {
		return s.fail(OpCalendarEvent, err)
	}
	return s.ok(OpCalendarEvent, map[string]any{"event": view}, nil, "", nil)
}

func (s *Service) calendarFreeSlots(ctx context.Context, args CalendarFreeSlotsArgs, scope Scope) Result {
	loc, err := ValidateInterval(args.Start.Time, args.End.Time, args.Timezone, s.limits)
	if err != nil {
		return s.fail(OpCalendarFreeSlots, err)
	}
	events, window, err := s.readEvents(ctx, args.CalendarIDs, args.Start.Time, args.End.Time, scope)
	if err != nil {
		return s.fail(OpCalendarFreeSlots, err)
	}

	busy := make([]Interval, 0, len(events))
	for _, e := range events {
		if e.Canceled || !e.Busy {
			continue
		}
		if e.AllDay && !args.IncludeAllDayAsBusy {
			continue
		}
		busy = append(busy, e.Interval)
	}
	slots := FreeSlots(window, loc, args.WorkingHours,
		busy, time.Duration(args.DurationMinutes)*time.Minute, time.Duration(args.BufferMinutes)*time.Minute)

	view := FreeSlotsView{Timezone: args.Timezone}
	for _, slot := range slots {
		view.Slots = append(view.Slots, FreeSlotView{
			Start:   slot.Start,
			End:     slot.End,
			Minutes: int(slot.Duration() / time.Minute),
		})
	}
	view.Limitations = append(view.Limitations,
		"free means free in the selected calendars as observed just now",
		"it is not other people's availability and does not survive a later change")
	if !args.IncludeAllDayAsBusy {
		view.Limitations = append(view.Limitations, "all-day events were not treated as busy")
	}
	return s.ok(OpCalendarFreeSlots, view, &Coverage{
		Scanned:  len(events),
		Returned: len(view.Slots),
		Complete: true,
	}, "", nil)
}

// readEvents resolves calendars, checks scope and fetches the occurrences
// overlapping the window.
func (s *Service) readEvents(ctx context.Context, ids []Handle, start, end time.Time, scope Scope) ([]BackendEvent, Interval, error) {
	if s.opts.Calendar == nil {
		return nil, Interval{}, ErrUnsupportedPlatform
	}
	refs := make([]ResourceRef, 0, len(ids))
	for _, h := range ids {
		ref, err := s.resolve(h, KindCalendar, scope)
		if err != nil {
			return nil, Interval{}, err
		}
		refs = append(refs, ref)
	}
	window := Interval{Start: start, End: end}
	events, err := s.opts.Calendar.Events(ctx, refs, window)
	if err != nil {
		return nil, Interval{}, err
	}
	kept := make([]BackendEvent, 0, len(events))
	for _, e := range events {
		if !scope.AllowsRef(e.CalendarRef) {
			continue
		}
		if !e.Interval.Overlaps(window) {
			continue
		}
		kept = append(kept, e)
	}
	return kept, window, nil
}

func (s *Service) eventView(e BackendEvent) (EventView, error) {
	ref := e.Ref
	if ref.Fingerprint == "" {
		ref.Fingerprint = eventFingerprint(e)
	}
	h, err := s.registry.Mint(ref)
	if err != nil {
		return EventView{}, err
	}
	cal, err := s.registry.Mint(e.CalendarRef)
	if err != nil {
		return EventView{}, err
	}
	return EventView{
		ID:           h,
		Version:      ref.Fingerprint,
		Calendar:     cal,
		Title:        e.Title,
		Location:     e.Location,
		Notes:        e.Notes,
		Start:        e.Interval.Start,
		End:          e.Interval.End,
		AllDay:       e.AllDay,
		Timezone:     e.Timezone,
		Recurring:    e.Recurring,
		Detached:     e.Detached,
		Canceled:     e.Canceled,
		Tentative:    e.Tentative,
		Busy:         e.Busy,
		HasAttendees: e.HasAttendees,
	}, nil
}

func eventFingerprint(e BackendEvent) string {
	return Fingerprint(
		e.Ref.NativeID,
		e.Ref.ExternalID,
		e.Interval.Start.UTC().Format(time.RFC3339Nano),
		e.Interval.End.UTC().Format(time.RFC3339Nano),
		e.Title,
		fmt.Sprintf("allday=%t", e.AllDay),
	)
}

func (s *Service) changePrepare(args ChangePrepareArgs, scope Scope, conn ConnectionState) Result {
	// Preparing performs no external write. It resolves handles, checks
	// scope and policy, and freezes the result for review.
	items := 0
	for i, change := range args.Changes {
		if err := scope.CheckChange(change, conn); err != nil {
			return s.fail(OpChangePrepare, err)
		}
		if _, err := s.resolveChange(change, scope); err != nil {
			return s.fail(OpChangePrepare, Errorf(CodeOf(err), "changes[%d]: %s", i, messageOf(err)))
		}
		if err := s.checkChangeShape(change, scope); err != nil {
			return s.fail(OpChangePrepare, Errorf(CodeOf(err), "changes[%d]: %s", i, messageOf(err)))
		}
		items += change.ItemCount()
	}
	if items > s.limits.MaxChangesPerPlan {
		return s.fail(OpChangePrepare, Errorf(CodeInvalidRequest,
			"the plan affects %d items, the maximum is %d", items, s.limits.MaxChangesPerPlan))
	}

	plan, err := s.plans.Prepare(args.Changes)
	if err != nil {
		return s.fail(OpChangePrepare, err)
	}
	view := PlanView{
		PlanID:           plan.ID,
		Digest:           plan.Digest,
		Adapters:         plan.Adapters,
		ItemCount:        plan.ItemCount,
		ExpiresAt:        plan.ExpiresAt,
		Summary:          summarize(plan.Changes()),
		RequiresApproval: true,
	}
	return s.ok(OpChangePrepare, view, nil, "", []string{
		"nothing has changed yet; applying this plan requires the person at the keyboard to approve it",
	})
}

// checkChangeShape enforces the structural rules that need resolved handles:
// a move stays inside one account, and a draft's reply source belongs to the
// sending account's scope.
func (s *Service) checkChangeShape(c Change, scope Scope) error {
	switch {
	case c.MailMove != nil:
		dest, err := s.resolve(c.MailMove.DestinationMailboxID, KindMailbox, scope)
		if err != nil {
			return err
		}
		for _, t := range c.MailMove.Messages {
			src, err := s.resolve(t.MessageID, KindMessage, scope)
			if err != nil {
				return err
			}
			if src.AccountID != dest.AccountID {
				return Errorf(CodeUnsupportedOperation, "moving between accounts is not supported")
			}
			if pathEqual(src.ContainerPath, dest.ContainerPath) {
				return Errorf(CodeInvalidRequest, "a message is already in the destination mailbox")
			}
		}
	case c.MailSaveDraft != nil:
		if c.MailSaveDraft.InReplyToMessageID != "" {
			if _, err := s.resolve(c.MailSaveDraft.InReplyToMessageID, KindMessage, scope); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) changeApply(ctx context.Context, args ChangeApplyArgs, scope Scope, conn ConnectionState) Result {
	plan, err := s.plans.Get(args.PlanID)
	if err != nil {
		return s.fail(OpChangeApply, err)
	}
	if plan.Expired(s.opts.Now.now()) {
		s.plans.Discard(plan.ID)
		return s.fail(OpChangeApply, ErrPlanNotFound)
	}
	if s.opts.Approvals == nil || !s.opts.Approvals.ApprovedPlan(plan.ID, plan.Digest) {
		return s.fail(OpChangeApply, ErrPlanNotApproved)
	}
	if s.opts.Mutator == nil {
		return s.fail(OpChangeApply, ErrUnsupportedPlatform)
	}
	// Re-check policy and scope now, not only at prepare time: an adapter
	// may have been disconnected or the scope narrowed since.
	resolved := make([]ResolvedChange, 0, len(plan.Changes()))
	for _, change := range plan.Changes() {
		if err := scope.CheckChange(change, conn); err != nil {
			return s.fail(OpChangeApply, err)
		}
		rc, err := s.resolveChange(change, scope)
		if err != nil {
			return s.fail(OpChangeApply, err)
		}
		rc.PlanID, rc.PlanDigest = plan.ID, plan.Digest
		resolved = append(resolved, rc)
	}

	// Each resolved effect gets its own durable intent immediately before it
	// runs. Without that record a crash mid-apply leaves nothing to recognize
	// the operation by in another session.
	if s.opts.Journal == nil {
		return s.fail(OpChangeApply, &Error{Code: CodeJournalUnavailable, Message: "no operation ledger is configured; refusing to mutate"})
	}

	// The plan is consumed before execution: one preparation applies at
	// most once, whatever happens next. Intent is still recorded before each
	// side effect below.
	if _, err := s.plans.Consume(plan.ID); err != nil {
		return s.fail(OpChangeApply, err)
	}

	var outcomes []ItemOutcome
	stopped := false
	for _, rc := range resolved {
		if stopped {
			outcomes = append(outcomes, remainingNotApplied(rc)...)
			continue
		}
		decision, err := s.opts.Journal.Begin(ctx, rc)
		if err != nil {
			if len(outcomes) == 0 {
				return s.fail(OpChangeApply, wrap(&Error{Code: CodeJournalUnavailable, Message: "the operation ledger could not record this change"}, err))
			}
			outcomes = append(outcomes, ItemOutcome{
				Outcome: OutcomeUnknown,
				Code:    CodeJournalUnavailable,
				Detail:  "the operation ledger could not record the next change",
			})
			stopped = true
			continue
		}
		if decision.State != MutationNew {
			outcome := OutcomeNotApplied
			code := CodePreconditionFailed
			detail := "a matching approved mutation is already recorded and will not be retried automatically"
			if decision.State == MutationIntentRecorded || decision.State == MutationOutcomeUnknown {
				outcome = OutcomeUnknown
				code = CodeOutcomeUnknown
				detail = "a matching mutation has an uncertain recorded outcome and must not be retried automatically"
			}
			outcomes = append(outcomes, ItemOutcome{Outcome: outcome, Code: code, Detail: detail})
			stopped = true
			continue
		}
		got, err := s.opts.Mutator.Apply(ctx, rc)
		if err != nil {
			// An error is not evidence that nothing happened. Anything
			// short of a readback is recorded as unknown.
			got = []ItemOutcome{{
				Outcome: outcomeForError(err),
				Code:    CodeOf(err),
				Detail:  messageOf(err),
			}}
		}
		outcomes = append(outcomes, got...)
		if err := s.opts.Journal.Complete(ctx, rc, got); err != nil {
			// The change may well have happened; an intent without an outcome
			// remains a cross-session retry barrier.
			outcomes = append(outcomes, ItemOutcome{
				Outcome: OutcomeUnknown,
				Code:    CodeJournalUnavailable,
				Detail:  "the outcome could not be recorded durably",
			})
			stopped = true
			continue
		}
		for _, o := range got {
			if o.Outcome.Uncertain() {
				stopped = true
			}
		}
	}

	view := ApplyView{PlanID: plan.ID, Digest: plan.Digest, Outcomes: outcomes}
	for _, o := range outcomes {
		switch o.Outcome {
		case OutcomeApplied:
			view.Applied++
		case OutcomeUnknown:
			view.Unknown++
		case OutcomeFailed, OutcomeStale:
			view.Failed++
		}
	}
	res := s.ok(OpChangeApply, view, &Coverage{Scanned: len(outcomes), Returned: len(outcomes), Complete: true}, "", nil)
	switch {
	case view.Unknown > 0:
		res.Status = StatusOutcomeUnknown
		res.Warnings = append(res.Warnings,
			"at least one change has an unknown outcome; it must not be retried automatically")
	case view.Failed > 0:
		res.Status = StatusPartial
	}
	return res
}

func (s *Service) openItem(ctx context.Context, args OpenItemArgs, scope Scope) Result {
	if s.opts.Navigator == nil {
		return s.fail(OpOpenItem, ErrUnsupportedPlatform)
	}
	ref, err := s.registry.Resolve(args.ItemID)
	if err != nil {
		return s.fail(OpOpenItem, err)
	}
	if err := scope.CheckRef(ref); err != nil {
		return s.fail(OpOpenItem, err)
	}
	if err := s.opts.Navigator.Open(ctx, ref); err != nil {
		return s.fail(OpOpenItem, err)
	}
	return s.ok(OpOpenItem, OpenView{ID: args.ItemID, Opened: true}, nil, "", nil)
}

// resolve turns a handle into a resource and checks it against the scope.
func (s *Service) resolve(h Handle, kind HandleKind, scope Scope) (ResourceRef, error) {
	ref, err := s.registry.ResolveKind(h, kind)
	if err != nil {
		return ResourceRef{}, err
	}
	if err := scope.CheckRef(ref); err != nil {
		return ResourceRef{}, err
	}
	return ref, nil
}

// resolveChange resolves every handle a change names, so an adapter is
// handed resources rather than model-supplied identifiers.
func (s *Service) resolveChange(c Change, scope Scope) (ResolvedChange, error) {
	refs := make(map[Handle]ResourceRef, 4)
	for _, h := range c.Handles() {
		if h == "" {
			continue
		}
		ref, err := s.registry.Resolve(h)
		if err != nil {
			return ResolvedChange{}, err
		}
		if err := scope.CheckRef(ref); err != nil {
			return ResolvedChange{}, err
		}
		refs[h] = ref
	}
	return ResolvedChange{Change: c.clone(), Refs: refs}, nil
}

func (s *Service) ok(op Operation, data any, coverage *Coverage, cursor string, warnings []string) Result {
	return Result{
		Version:    Version,
		Operation:  op,
		Status:     StatusOK,
		ObservedAt: s.opts.Now.now(),
		Data:       data,
		Coverage:   coverage,
		NextCursor: cursor,
		Warnings:   warnings,
	}
}

func (s *Service) fail(op Operation, err error) Result {
	return Result{
		Version:    Version,
		Operation:  op,
		Status:     StatusOf(err),
		ObservedAt: s.opts.Now.now(),
		Error:      &ResultError{Code: CodeOf(err), Message: messageOf(err)},
	}
}

// outcomeForError maps an adapter failure onto an item outcome. Only a
// failure that proves nothing happened may be reported as not applied;
// everything else is unknown, because a timeout can follow a successful side
// effect.
func outcomeForError(err error) Outcome {
	switch CodeOf(err) {
	case CodeScopeDenied, CodePermissionDenied, CodeUnsupportedOperation, CodeInvalidRequest, CodeReadOnlyCalendar:
		return OutcomeNotApplied
	case CodeStaleReference, CodePreconditionFailed:
		return OutcomeStale
	default:
		return OutcomeUnknown
	}
}

func remainingNotApplied(rc ResolvedChange) []ItemOutcome {
	out := make([]ItemOutcome, 0, rc.Change.ItemCount())
	handles := rc.Change.Handles()
	if len(handles) == 0 {
		return []ItemOutcome{{Outcome: OutcomeNotApplied, Detail: "stopped after an earlier uncertain outcome"}}
	}
	for _, h := range handles {
		out = append(out, ItemOutcome{Target: h, Outcome: OutcomeNotApplied, Detail: "stopped after an earlier uncertain outcome"})
	}
	return out
}

// summarize renders one short line per change for the model-facing preview.
// It names counts and destinations, never bodies or recipients: the full
// detail belongs in the human's review, not in the prompt.
func summarize(changes []Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		switch {
		case c.MailMove != nil:
			out = append(out, fmt.Sprintf("move %d message(s) to one mailbox", len(c.MailMove.Messages)))
		case c.MailSetRead != nil:
			out = append(out, fmt.Sprintf("mark %d message(s) %s", len(c.MailSetRead.Messages), readWord(c.MailSetRead.Read)))
		case c.MailSetFlag != nil:
			out = append(out, fmt.Sprintf("%s %d message(s)", flagWord(c.MailSetFlag.Flagged), len(c.MailSetFlag.Messages)))
		case c.MailSaveDraft != nil:
			kind := "a new draft"
			if c.MailSaveDraft.InReplyToMessageID != "" {
				kind = "a reply draft"
			}
			out = append(out, "save "+kind+" (nothing is sent)")
		case c.CalendarCreateEvent != nil:
			out = append(out, "create one calendar event")
		case c.CalendarUpdateEvent != nil:
			out = append(out, "update one calendar event")
		default:
			out = append(out, "unsupported change")
		}
	}
	return out
}

func readWord(read bool) string {
	if read {
		return "read"
	}
	return "unread"
}

func flagWord(flagged bool) string {
	if flagged {
		return "flag"
	}
	return "unflag"
}

// pageCoverage reports a host-side page honestly: a list cut at the page
// size is incomplete, and this release offers no continuation cursor for
// those lists rather than inventing one that a changing mailbox would
// invalidate.
func pageCoverage(scanned, returned int) *Coverage {
	cov := &Coverage{Scanned: scanned, Returned: returned, Complete: returned >= scanned}
	if !cov.Complete {
		cov.Reason = "page_limit"
	}
	return cov
}

func clipBody(body string, max int) (string, bool) {
	if max <= 0 || len(body) <= max {
		return body, false
	}
	cut := body[:max]
	// Never cut a rune in half; a truncated body must still be valid UTF-8.
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

func appendOnce(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

func pathEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
