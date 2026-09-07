package personalapps

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The fakes below stand in for the native adapters. Unit tests never touch a
// real application, never prompt for a permission and never sleep.

type fakeMail struct {
	accounts  []BackendAccount
	mailboxes []BackendMailbox
	page      BackendMailPage
	messages  []BackendMessage
	err       error
	calls     int
}

func (f *fakeMail) Accounts(context.Context) ([]BackendAccount, error) {
	f.calls++
	return f.accounts, f.err
}

func (f *fakeMail) Mailboxes(context.Context, ResourceRef, ResourceRef) ([]BackendMailbox, error) {
	f.calls++
	return f.mailboxes, f.err
}

func (f *fakeMail) Search(_ context.Context, _ MailQuery) (BackendMailPage, error) {
	f.calls++
	return f.page, f.err
}

func (f *fakeMail) Messages(context.Context, []ResourceRef, int) ([]BackendMessage, error) {
	f.calls++
	return f.messages, f.err
}

type fakeCalendar struct {
	calendars []BackendCalendar
	events    []BackendEvent
	err       error
}

func (f *fakeCalendar) Calendars(context.Context) ([]BackendCalendar, error) {
	return f.calendars, f.err
}

func (f *fakeCalendar) Events(context.Context, []ResourceRef, Interval) ([]BackendEvent, error) {
	return f.events, f.err
}

func (f *fakeCalendar) Event(_ context.Context, ref ResourceRef) (BackendEvent, error) {
	for _, e := range f.events {
		if e.Ref.NativeID == ref.NativeID {
			return e, f.err
		}
	}
	return BackendEvent{}, ErrUnknownHandle
}

type fakeMutator struct {
	calls    int
	outcomes [][]ItemOutcome
	err      error
}

func (f *fakeMutator) Apply(context.Context, ResolvedChange) ([]ItemOutcome, error) {
	defer func() { f.calls++ }()
	if f.err != nil {
		return nil, f.err
	}
	if f.calls < len(f.outcomes) {
		return f.outcomes[f.calls], nil
	}
	return []ItemOutcome{{Outcome: OutcomeApplied}}, nil
}

type fakeJournal struct {
	intents   []string
	outcomes  []string
	order     []string
	intentEr  error
	outcomeEr error
}

func (f *fakeJournal) RecordIntent(_ context.Context, plan Plan) error {
	f.intents = append(f.intents, plan.ID)
	f.order = append(f.order, "intent")
	return f.intentEr
}

func (f *fakeJournal) RecordOutcome(_ context.Context, plan Plan, _ []ItemOutcome) error {
	f.outcomes = append(f.outcomes, plan.ID)
	f.order = append(f.order, "outcome")
	return f.outcomeEr
}

type fakeApprovals struct {
	planID string
	digest string
}

func (f *fakeApprovals) ApprovedPlan(planID, digest string) bool {
	return planID == f.planID && digest == f.digest
}

type fakeNavigator struct {
	opened []string
	err    error
}

func (f *fakeNavigator) Open(_ context.Context, ref ResourceRef) error {
	if f.err != nil {
		return f.err
	}
	f.opened = append(f.opened, ref.NativeID)
	return nil
}

// fixture wires a service with in-scope mail and calendar fakes.
type fixture struct {
	svc     *Service
	mail    *fakeMail
	cal     *fakeCalendar
	mutator *fakeMutator
	journal *fakeJournal
	appr    *fakeApprovals
	nav     *fakeNavigator
	clock   *fakeClock
	gate    int
	gateErr error
}

const (
	testAccount  = "acct-native-1"
	testCalendar = "cal-native-1"
)

func accountRef() ResourceRef {
	return ResourceRef{Kind: KindAccount, Adapter: AdapterMail, AccountID: testAccount}
}

func mailboxRef(path ...string) ResourceRef {
	return ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: testAccount, ContainerPath: path}
}

func messageRef(native string, path ...string) ResourceRef {
	return ResourceRef{Kind: KindMessage, Adapter: AdapterMail, AccountID: testAccount, ContainerPath: path, NativeID: native}
}

func calendarRef() ResourceRef {
	return ResourceRef{Kind: KindCalendar, Adapter: AdapterCalendar, AccountID: testCalendar, NativeID: testCalendar}
}

func eventRef(native string) ResourceRef {
	return ResourceRef{Kind: KindEvent, Adapter: AdapterCalendar, AccountID: testCalendar, NativeID: native}
}

func newFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	f := &fixture{
		clock:   newFakeClock(),
		mutator: &fakeMutator{},
		journal: &fakeJournal{},
		appr:    &fakeApprovals{},
		nav:     &fakeNavigator{},
	}
	f.mail = &fakeMail{
		accounts: []BackendAccount{{Ref: accountRef(), DisplayName: "Work"}},
		mailboxes: []BackendMailbox{
			{Ref: mailboxRef("INBOX"), DisplayName: "INBOX", UnreadCount: 2},
			{Ref: mailboxRef("Archive"), DisplayName: "Archive"},
		},
		page: BackendMailPage{
			Messages: []BackendMessage{
				{
					Ref:        messageRef("m1", "INBOX"),
					MailboxRef: mailboxRef("INBOX"),
					Subject:    "Status update",
					From:       "colleague@example.com",
					Received:   time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC),
					Unread:     true,
				},
			},
			Scanned:  1,
			Complete: true,
		},
		messages: []BackendMessage{{
			Ref:           messageRef("m1", "INBOX"),
			MailboxRef:    mailboxRef("INBOX"),
			Subject:       "Status update",
			From:          "colleague@example.com",
			Received:      time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC),
			Unread:        true,
			Body:          "the quarterly numbers are attached",
			BodyAvailable: true,
		}},
	}
	f.cal = &fakeCalendar{
		calendars: []BackendCalendar{{Ref: calendarRef(), Title: "Personal", Writable: true}},
	}

	opts := Options{
		Scope: Scope{
			MailEnabled:      true,
			CalendarEnabled:  true,
			AllowedAccounts:  []string{testAccount},
			AllowedCalendars: []string{testCalendar},
		},
		Mail:      f.mail,
		Calendar:  f.cal,
		Navigator: f.nav,
		Journal:   f.journal,
		Approvals: f.appr,
		Now:       f.clock.Now,
		PrivacyGate: func(context.Context) error {
			f.gate++
			return f.gateErr
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	svc, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f.svc = svc
	if opts.Scope.MailEnabled {
		if err := svc.Connect(AdapterMail); err != nil {
			t.Fatalf("Connect(mail): %v", err)
		}
	}
	if opts.Scope.CalendarEnabled {
		if err := svc.Connect(AdapterCalendar); err != nil {
			t.Fatalf("Connect(calendar): %v", err)
		}
	}
	return f
}

func (f *fixture) run(t *testing.T, raw string) Result {
	t.Helper()
	req, err := ParseRequest([]byte(raw), Limits{})
	if err != nil {
		t.Fatalf("ParseRequest(%s): %v", clip(raw, 120), err)
	}
	return f.svc.Execute(context.Background(), req)
}

func (f *fixture) mustRun(t *testing.T, raw string) Result {
	t.Helper()
	res := f.run(t, raw)
	if res.Error != nil {
		t.Fatalf("%s failed: %s: %s", res.Operation, res.Error.Code, res.Error.Message)
	}
	return res
}

func data(t *testing.T, res Result, key string) any {
	t.Helper()
	m, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data is %T, want a map", res.Data)
	}
	return m[key]
}

// accountHandle runs mail_accounts and returns the handle it issued.
func (f *fixture) accountHandle(t *testing.T) Handle {
	t.Helper()
	res := f.mustRun(t, `{"operation":"mail_accounts"}`)
	accounts := data(t, res, "accounts").([]AccountView)
	if len(accounts) != 1 {
		t.Fatalf("mail_accounts returned %d accounts, want 1", len(accounts))
	}
	return accounts[0].ID
}

func (f *fixture) mailboxHandles(t *testing.T) map[string]Handle {
	t.Helper()
	account := f.accountHandle(t)
	res := f.mustRun(t, `{"operation":"mail_mailboxes","arguments":{"account_id":"`+string(account)+`"}}`)
	out := map[string]Handle{}
	for _, box := range data(t, res, "mailboxes").([]MailboxView) {
		out[box.Name] = box.ID
	}
	return out
}

func (f *fixture) calendarHandle(t *testing.T) Handle {
	t.Helper()
	res := f.mustRun(t, `{"operation":"calendar_list"}`)
	calendars := data(t, res, "calendars").([]CalendarView)
	if len(calendars) != 1 {
		t.Fatalf("calendar_list returned %d calendars, want 1", len(calendars))
	}
	return calendars[0].ID
}

func TestNewRequiresAPrivacyGateWithAnyBackend(t *testing.T) {
	_, err := New(Options{Mail: &fakeMail{}})
	if err == nil {
		t.Fatal("New wired a mail backend with no privacy gate")
	}
	if _, err := New(Options{}); err != nil {
		t.Fatalf("New with no backend: %v", err)
	}
}

func TestNewRejectsInvalidLimits(t *testing.T) {
	if _, err := New(Options{Limits: Limits{PageSize: 500, MaxPageSize: 10}}); err == nil {
		t.Fatal("New accepted inconsistent limits")
	}
}

func TestStatusIsMetadataOnly(t *testing.T) {
	f := newFixture(t, nil)
	res := f.mustRun(t, `{"operation":"status"}`)

	if f.gate != 0 {
		t.Fatal("status entered the private session")
	}
	if f.mail.calls != 0 {
		t.Fatal("status contacted the mail adapter")
	}
	view, ok := res.Data.(StatusView)
	if !ok {
		t.Fatalf("Data is %T, want StatusView", res.Data)
	}
	if !view.MailConnected || !view.CalendarConnected {
		t.Fatalf("status = %+v, want both adapters connected", view)
	}
	if view.MutationsEnabled {
		t.Fatal("mutations are enabled by default")
	}
	if len(view.ChangeTypes) != 0 {
		t.Fatalf("ChangeTypes = %v with mutations off", view.ChangeTypes)
	}
	for _, op := range view.Operations {
		if op == OpChangePrepare || op == OpChangeApply {
			t.Fatalf("status offers %q while mutations are disabled", op)
		}
	}
	if !strings.Contains(strings.Join(view.Notes, " "), "mutations are disabled") {
		t.Fatalf("Notes = %v, want the disabled mutations explained", view.Notes)
	}
}

func TestStatusReflectsDisconnection(t *testing.T) {
	f := newFixture(t, nil)
	f.svc.Disconnect(AdapterMail)

	view := f.svc.Status()
	if view.MailConnected {
		t.Fatal("mail still reports as connected after Disconnect")
	}
	for _, op := range view.Operations {
		if op.Adapter() == AdapterMail {
			t.Fatalf("a disconnected adapter still offers %q", op)
		}
	}
}

func TestDisabledAndDisconnectedAdaptersRefuse(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		f := newFixture(t, func(o *Options) { o.Scope.MailEnabled = false })
		res := f.run(t, `{"operation":"mail_accounts"}`)
		if res.Error == nil || res.Error.Code != CodeUnsupportedOperation {
			t.Fatalf("result = %+v, want the disabled adapter refused", res)
		}
		if f.mail.calls != 0 {
			t.Fatal("a disabled adapter was contacted")
		}
	})
	t.Run("disconnected", func(t *testing.T) {
		f := newFixture(t, nil)
		f.svc.Disconnect(AdapterMail)
		res := f.run(t, `{"operation":"mail_accounts"}`)
		if res.Error == nil || res.Error.Code != CodeAppUnavailable {
			t.Fatalf("result = %+v, want the disconnected adapter refused", res)
		}
		if f.gate != 0 {
			t.Fatal("a refused operation still entered the private session")
		}
	})
}

func TestPrivacyGateRunsBeforeAnyRead(t *testing.T) {
	f := newFixture(t, nil)
	f.accountHandle(t)
	if f.gate == 0 {
		t.Fatal("a personal read did not go through the privacy gate")
	}
	if !f.svc.Connection().PrivateSession {
		t.Fatal("the session was not marked private after a personal read")
	}

	// Disconnecting does not un-mark the session: content already read
	// stays in the conversation.
	f.svc.Disconnect(AdapterMail)
	if !f.svc.Connection().PrivateSession {
		t.Fatal("Disconnect cleared the private-session marking")
	}
}

func TestPrivacyGateFailureRefusesTheRead(t *testing.T) {
	f := newFixture(t, nil)
	f.gateErr = errors.New("history could not be locked down")

	res := f.run(t, `{"operation":"mail_accounts"}`)
	if res.Error == nil {
		t.Fatal("a read proceeded although the privacy gate failed")
	}
	if f.mail.calls != 0 {
		t.Fatal("the adapter was contacted although the privacy gate failed")
	}
}

func TestScopeDeniesOutOfScopeResources(t *testing.T) {
	f := newFixture(t, nil)
	handles := f.mailboxHandles(t)
	inbox := handles["INBOX"]

	// Narrowing the scope invalidates outstanding handles outright.
	f.svc.SetScope(Scope{MailEnabled: true, CalendarEnabled: true, AllowedAccounts: nil})
	res := f.run(t, `{"operation":"mail_search","arguments":{"mailbox_ids":["`+string(inbox)+`"]}}`)
	if res.Error == nil {
		t.Fatalf("a search ran after the scope was emptied: %+v", res)
	}
}

func TestEmptyAllowlistAuthorizesNothing(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Scope.AllowedAccounts = nil })
	res := f.mustRun(t, `{"operation":"mail_accounts"}`)
	accounts := data(t, res, "accounts").([]AccountView)
	if len(accounts) != 0 {
		t.Fatalf("an empty allowlist exposed %d accounts", len(accounts))
	}
	if res.Coverage == nil || res.Coverage.Scanned != 1 || res.Coverage.Returned != 0 {
		t.Fatalf("coverage = %+v, want the filtering reported honestly", res.Coverage)
	}
}

func TestMailboxAllowlistNarrowsWithinAnAccount(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Scope.AllowedMailboxes = []MailboxScope{{AccountID: testAccount, Path: []string{"INBOX"}}}
	})
	boxes := f.mailboxHandles(t)
	if _, ok := boxes["INBOX"]; !ok {
		t.Fatal("the allowed mailbox was filtered out")
	}
	if _, ok := boxes["Archive"]; ok {
		t.Fatal("a mailbox outside the allowlist was returned")
	}
}

// An adapter must not be able to widen a request by returning something the
// scope does not cover.
func TestOutOfScopeAdapterResultsAreDropped(t *testing.T) {
	f := newFixture(t, nil)
	f.mail.page.Messages = append(f.mail.page.Messages, BackendMessage{
		Ref:        ResourceRef{Kind: KindMessage, Adapter: AdapterMail, AccountID: "other-account", ContainerPath: []string{"INBOX"}, NativeID: "x"},
		MailboxRef: ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "other-account", ContainerPath: []string{"INBOX"}},
		Subject:    "from an account nobody approved",
	})
	f.mail.page.Scanned = 2

	account := f.accountHandle(t)
	res := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	messages := data(t, res, "messages").([]MessageSummaryView)
	if len(messages) != 1 {
		t.Fatalf("mail_search returned %d messages, want the out-of-scope one dropped", len(messages))
	}
	if len(res.Warnings) == 0 {
		t.Fatal("dropping an out-of-scope result was not reported")
	}
}

func TestMailSearchReportsPartialCoverage(t *testing.T) {
	f := newFixture(t, nil)
	f.mail.page.Scanned = 1000
	f.mail.page.Complete = false
	f.mail.page.Reason = "scan_limit"
	f.mail.page.NextCursor = "opaque-token"

	account := f.accountHandle(t)
	res := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	if res.Status != StatusPartial {
		t.Fatalf("Status = %q, want %q for an incomplete scan", res.Status, StatusPartial)
	}
	if res.Coverage == nil || res.Coverage.Complete || res.Coverage.Reason != "scan_limit" {
		t.Fatalf("coverage = %+v, want an explained incomplete scan", res.Coverage)
	}
	if res.NextCursor != "opaque-token" {
		t.Fatalf("NextCursor = %q, want the adapter's continuation token", res.NextCursor)
	}
	if res.Coverage.BodyScope != "not_requested" {
		t.Fatalf("BodyScope = %q, want it to say no body was fetched", res.Coverage.BodyScope)
	}
}

func TestMailReadReportsUnavailableBodies(t *testing.T) {
	f := newFixture(t, nil)
	f.mail.messages[0].Body = ""
	f.mail.messages[0].BodyAvailable = false
	f.mail.messages[0].Unavailable = "not synchronized"

	account := f.accountHandle(t)
	search := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	id := data(t, search, "messages").([]MessageSummaryView)[0].ID

	res := f.mustRun(t, `{"operation":"mail_read","arguments":{"message_ids":["`+string(id)+`"]}}`)
	view := data(t, res, "messages").([]MessageView)[0]
	if view.BodyAvailable {
		t.Fatal("an unavailable body was reported as available")
	}
	if view.Unavailable == "" {
		t.Fatal("an unavailable body carries no explanation")
	}
	if res.Status != StatusPartial {
		t.Fatalf("Status = %q, want %q when content was unavailable", res.Status, StatusPartial)
	}
}

func TestMailReadTruncatesBodies(t *testing.T) {
	f := newFixture(t, nil)
	f.mail.messages[0].Body = strings.Repeat("é", 200) // two bytes per rune

	account := f.accountHandle(t)
	search := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	id := data(t, search, "messages").([]MessageSummaryView)[0].ID

	res := f.mustRun(t, `{"operation":"mail_read","arguments":{"message_ids":["`+string(id)+`"],"max_body_bytes":101}}`)
	view := data(t, res, "messages").([]MessageView)[0]
	if !view.BodyTruncated {
		t.Fatal("a truncated body was not marked truncated")
	}
	if len(view.Body) > 101 {
		t.Fatalf("body is %d bytes, want at most 101", len(view.Body))
	}
	if !strings.HasSuffix(view.Body, "é") {
		t.Fatalf("truncation split a rune: %q", view.Body[len(view.Body)-3:])
	}
}

// A message's version must change when a field a mutation depends on
// changes, so a stale handle cannot be used to move the wrong message.
func TestMessageVersionTracksObservedState(t *testing.T) {
	f := newFixture(t, nil)
	account := f.accountHandle(t)
	first := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	before := data(t, first, "messages").([]MessageSummaryView)[0]

	f.mail.page.Messages[0].Unread = false
	second := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	after := data(t, second, "messages").([]MessageSummaryView)[0]

	if before.Version == after.Version {
		t.Fatal("the version did not change when the read flag did")
	}
	if before.ID == after.ID {
		t.Fatal("a changed observation reused the same handle")
	}
}

func TestCalendarFreeSlotsExcludesBusyTime(t *testing.T) {
	f := newFixture(t, nil)
	prague := mustZone(t, "Europe/Prague")
	busy := Interval{
		Start: time.Date(2026, 9, 7, 9, 0, 0, 0, prague),
		End:   time.Date(2026, 9, 7, 12, 0, 0, 0, prague),
	}
	f.cal.events = []BackendEvent{
		{Ref: eventRef("e1"), CalendarRef: calendarRef(), Title: "Workshop", Interval: busy, Busy: true},
		{Ref: eventRef("e2"), CalendarRef: calendarRef(), Title: "Canceled", Busy: true, Canceled: true,
			Interval: Interval{Start: busy.End, End: busy.End.Add(2 * time.Hour)}},
		{Ref: eventRef("e3"), CalendarRef: calendarRef(), Title: "Free time marker", Busy: false,
			Interval: Interval{Start: busy.End, End: busy.End.Add(time.Hour)}},
	}

	cal := f.calendarHandle(t)
	res := f.mustRun(t, `{"operation":"calendar_free_slots","arguments":{"calendar_ids":["`+string(cal)+`"],`+
		`"start":"2026-09-07T00:00:00+02:00","end":"2026-09-08T00:00:00+02:00","timezone":"Europe/Prague",`+
		`"duration_minutes":60,"working_hours":{"start":"09:00","end":"17:00"}}}`)

	view, ok := res.Data.(FreeSlotsView)
	if !ok {
		t.Fatalf("Data is %T, want FreeSlotsView", res.Data)
	}
	if len(view.Slots) != 1 {
		t.Fatalf("free slots = %+v, want one gap after the workshop", view.Slots)
	}
	if !view.Slots[0].Start.Equal(busy.End) || view.Slots[0].Minutes != 300 {
		t.Fatalf("slot = %+v, want 12:00-17:00", view.Slots[0])
	}
	if len(view.Limitations) == 0 {
		t.Fatal("free slots were reported without stating their limits")
	}
}

func TestCalendarEventsFiltersOutOfScopeCalendars(t *testing.T) {
	f := newFixture(t, nil)
	cal := f.calendarHandle(t)
	f.cal.events = []BackendEvent{
		{Ref: eventRef("mine"), CalendarRef: calendarRef(), Title: "Mine",
			Interval: Interval{Start: time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)}},
		{Ref: eventRef("theirs"), CalendarRef: ResourceRef{Kind: KindCalendar, Adapter: AdapterCalendar, AccountID: "other", NativeID: "other"},
			Title:    "Not approved",
			Interval: Interval{Start: time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}},
	}

	res := f.mustRun(t, `{"operation":"calendar_events","arguments":{"calendar_ids":["`+string(cal)+`"],`+
		`"start":"2026-09-07T00:00:00Z","end":"2026-09-08T00:00:00Z","timezone":"UTC"}}`)
	events := data(t, res, "events").([]EventView)
	if len(events) != 1 || events[0].Title != "Mine" {
		t.Fatalf("calendar_events returned %+v, want only the approved calendar", events)
	}
}

func TestOpenItemRequiresANavigatorAndScope(t *testing.T) {
	f := newFixture(t, nil)
	account := f.accountHandle(t)
	search := f.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(account)+`"]}}`)
	id := data(t, search, "messages").([]MessageSummaryView)[0].ID

	res := f.mustRun(t, `{"operation":"open_item","arguments":{"item_id":"`+string(id)+`"}}`)
	if view := res.Data.(OpenView); !view.Opened {
		t.Fatal("open_item reported that nothing was opened")
	}
	if len(f.nav.opened) != 1 || f.nav.opened[0] != "m1" {
		t.Fatalf("navigator opened %v, want the resolved message", f.nav.opened)
	}

	without := newFixture(t, func(o *Options) { o.Navigator = nil })
	other := without.accountHandle(t)
	otherSearch := without.mustRun(t, `{"operation":"mail_search","arguments":{"account_ids":["`+string(other)+`"]}}`)
	otherID := data(t, otherSearch, "messages").([]MessageSummaryView)[0].ID
	res = without.run(t, `{"operation":"open_item","arguments":{"item_id":"`+string(otherID)+`"}}`)
	if res.Error == nil || res.Error.Code != CodeUnsupportedOperation {
		t.Fatalf("result = %+v, want unsupported with no navigator", res)
	}
}

func TestUnsupportedPlatformRefusesEverythingButStatus(t *testing.T) {
	svc, err := New(Options{Scope: Scope{MailEnabled: true}, Now: newFakeClock().Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if svc.PlatformSupported() != platformSupported() {
		t.Fatalf("PlatformSupported() = %v with no backend wired", svc.PlatformSupported())
	}
	if platformSupported() {
		t.Skip("this platform can reach the personal apps")
	}
	req, err := ParseRequest([]byte(`{"operation":"mail_accounts"}`), Limits{})
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	res := svc.Execute(context.Background(), req)
	if res.Status != StatusUnsupported {
		t.Fatalf("Status = %q, want %q on an unsupported platform", res.Status, StatusUnsupported)
	}
	status := svc.Status()
	if len(status.Operations) != 1 || status.Operations[0] != OpStatus {
		t.Fatalf("Operations = %v, want status only", status.Operations)
	}
}
