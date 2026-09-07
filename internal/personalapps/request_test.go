package personalapps

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// handles mints a handle of each kind so tests use real host-issued values
// rather than strings a model could have invented.
type handles struct {
	account  Handle
	mailbox  Handle
	mailbox2 Handle
	message  Handle
	message2 Handle
	calendar Handle
	event    Handle
}

func newHandles(t *testing.T) handles {
	t.Helper()
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	mint := func(kind HandleKind, native string) Handle {
		t.Helper()
		h, err := reg.Mint(ResourceRef{Kind: kind, Adapter: AdapterMail, AccountID: "a", NativeID: native})
		if err != nil {
			t.Fatalf("Mint(%s): %v", kind, err)
		}
		return h
	}
	return handles{
		account:  mint(KindAccount, "acct"),
		mailbox:  mint(KindMailbox, "box"),
		mailbox2: mint(KindMailbox, "box2"),
		message:  mint(KindMessage, "m1"),
		message2: mint(KindMessage, "m2"),
		calendar: mint(KindCalendar, "cal"),
		event:    mint(KindEvent, "evt"),
	}
}

func parse(t *testing.T, raw string) (Request, error) {
	t.Helper()
	return ParseRequest([]byte(raw), Limits{})
}

func mustParse(t *testing.T, raw string) Request {
	t.Helper()
	req, err := parse(t, raw)
	if err != nil {
		t.Fatalf("ParseRequest(%s): %v", clip(raw, 120), err)
	}
	return req
}

func TestParseRequestStatus(t *testing.T) {
	req := mustParse(t, `{"operation":"status"}`)
	if req.Operation() != OpStatus {
		t.Fatalf("Operation() = %q, want %q", req.Operation(), OpStatus)
	}
	if _, ok := req.Arguments().(*StatusArgs); !ok {
		t.Fatalf("Arguments() = %T, want *StatusArgs", req.Arguments())
	}
}

func TestParseRequestMailSearch(t *testing.T) {
	h := newHandles(t)
	raw := `{"operation":"mail_search","arguments":{"account_ids":["` + string(h.account) + `"],` +
		`"mailbox_ids":["` + string(h.mailbox) + `"],` +
		`"received_after":"2026-09-04T00:00:00+02:00","unread":true,"limit":25}}`

	req := mustParse(t, raw)
	args, ok := req.Arguments().(*MailSearchArgs)
	if !ok {
		t.Fatalf("Arguments() = %T, want *MailSearchArgs", req.Arguments())
	}
	if args.Sort != SortReceivedDesc {
		t.Errorf("Sort = %q, want the default %q", args.Sort, SortReceivedDesc)
	}
	if args.Unread == nil || !*args.Unread {
		t.Errorf("Unread = %v, want true", args.Unread)
	}
	if got := args.ReceivedAfter.UTC(); !got.Equal(time.Date(2026, 9, 3, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("ReceivedAfter = %v, want 2026-09-03T22:00:00Z", got)
	}
	if got := args.Size(Limits{}); got != 25 {
		t.Errorf("page size = %d, want 25", got)
	}
}

// The same payload must decode identically whichever protocol carried it:
// the native tool call and the fenced block share one decoder.
func TestParseRequestNativeAndFencedAgree(t *testing.T) {
	h := newHandles(t)
	payload := `{"operation":"mail_read","arguments":{"message_ids":["` + string(h.message) + `"]}}`

	native := mustParse(t, payload)
	fenced := mustParse(t, "\n  "+payload+"\n")

	if native.Operation() != fenced.Operation() {
		t.Fatalf("operations differ: %q vs %q", native.Operation(), fenced.Operation())
	}
	a := native.Arguments().(*MailReadArgs)
	b := fenced.Arguments().(*MailReadArgs)
	if len(a.MessageIDs) != len(b.MessageIDs) || a.MessageIDs[0] != b.MessageIDs[0] {
		t.Fatalf("message ids differ: %v vs %v", a.MessageIDs, b.MessageIDs)
	}
}

func TestParseRequestRejectsMalformedPayloads(t *testing.T) {
	h := newHandles(t)
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ``},
		{"not json", `nonsense`},
		{"truncated", `{"operation":"status"`},
		{"trailing json", `{"operation":"status"} {"operation":"status"}`},
		{"trailing scalar", `{"operation":"status"} 7`},
		{"duplicate top-level key", `{"operation":"status","operation":"mail_accounts"}`},
		{"duplicate nested key", `{"operation":"mail_read","arguments":{"message_ids":["` + string(h.message) + `"],"message_ids":[]}}`},
		{"unknown operation", `{"operation":"mail_send","arguments":{}}`},
		{"missing operation", `{"arguments":{}}`},
		{"unknown envelope field", `{"operation":"status","approved":true}`},
		{"unknown argument field", `{"operation":"status","arguments":{"bypass":true}}`},
		{"foreign argument field", `{"operation":"mail_accounts","arguments":{"message_ids":[]}}`},
		{"wrong type", `{"operation":"mail_read","arguments":{"message_ids":"all"}}`},
		{"operation not a string", `{"operation":7}`},
		{"array payload", `["status"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := parse(t, tt.raw); err == nil {
				t.Fatalf("ParseRequest accepted %s", tt.raw)
			} else if CodeOf(err) != CodeInvalidRequest {
				t.Fatalf("code = %q, want %q (err %v)", CodeOf(err), CodeInvalidRequest, err)
			}
		})
	}
}

// There is no argument through which a model can grant itself permission.
func TestParseRequestHasNoSelfApprovalArguments(t *testing.T) {
	for _, field := range []string{"approved", "bypass", "auto", "script", "shell", "app_path"} {
		raw := `{"operation":"change_apply","arguments":{"plan_id":"plan_000102030405060708090a0b0c0d0e0f","` + field + `":true}}`
		if _, err := parse(t, raw); err == nil {
			t.Errorf("ParseRequest accepted a %q argument", field)
		}
	}
}

func TestParseRequestRejectsOversizedAndDeepPayloads(t *testing.T) {
	big := `{"operation":"mail_search","arguments":{"subject":"` + strings.Repeat("a", 400) + `"}}`
	if _, err := ParseRequest([]byte(big), Limits{MaxRequestBytes: 100}); err == nil {
		t.Fatal("ParseRequest accepted an oversized payload")
	}

	deep := `{"operation":"status","arguments":` + strings.Repeat(`[`, 40) + strings.Repeat(`]`, 40) + `}`
	if _, err := parse(t, deep); err == nil {
		t.Fatal("ParseRequest accepted an excessively nested payload")
	}
}

func TestParseRequestRejectsInvalidUTF8(t *testing.T) {
	raw := []byte(`{"operation":"mail_search","arguments":{"subject":"` + "\xff\xfe" + `"}}`)
	if _, err := ParseRequest(raw, Limits{}); err == nil {
		t.Fatal("ParseRequest accepted invalid UTF-8")
	}
}

func TestMailSearchRequiresScope(t *testing.T) {
	_, err := parse(t, `{"operation":"mail_search","arguments":{"unread":true}}`)
	if err == nil {
		t.Fatal("mail_search accepted an unscoped search")
	}
	if !strings.Contains(err.Error(), "unscoped") {
		t.Fatalf("error = %v, want it to name the missing scope", err)
	}
}

func TestMailSearchRejectsForeignHandleKinds(t *testing.T) {
	h := newHandles(t)
	raw := `{"operation":"mail_search","arguments":{"account_ids":["` + string(h.mailbox) + `"]}}`
	if _, err := parse(t, raw); err == nil {
		t.Fatal("mail_search accepted a mailbox handle as an account")
	}
}

// TestMalformedHandleErrorEchoesTheReceivedValue guards a diagnosability
// fix: the previous error text ("account_ids contains an entry that is not
// a host-issued acct handle") named the problem's shape but never the
// actual string received, so there was no way to tell — from the
// error alone — whether a model retyped a handle with a typo, echoed a
// stale value, or sent something structurally unrelated. The value is safe
// to echo: a handle carries no account name, mailbox path or content, only
// a kind prefix and random bytes.
func TestMalformedHandleErrorEchoesTheReceivedValue(t *testing.T) {
	raw := `{"operation":"mail_search","arguments":{"account_ids":["acct_not-actually-hex"]}}`
	_, err := parse(t, raw)
	if err == nil {
		t.Fatal("expected a malformed-handle error")
	}
	if !strings.Contains(err.Error(), "acct_not-actually-hex") {
		t.Fatalf("error %q does not echo the received value", err.Error())
	}
}

func TestMailSearchNormalizesHandleLists(t *testing.T) {
	h := newHandles(t)
	raw := `{"operation":"mail_search","arguments":{"mailbox_ids":["` + string(h.mailbox2) + `","` +
		string(h.mailbox) + `","` + string(h.mailbox2) + `"]}}`
	args := mustParse(t, raw).Arguments().(*MailSearchArgs)
	if len(args.MailboxIDs) != 2 {
		t.Fatalf("MailboxIDs = %v, want the duplicate removed", args.MailboxIDs)
	}
	if args.MailboxIDs[0] > args.MailboxIDs[1] {
		t.Fatalf("MailboxIDs = %v, want a stable order", args.MailboxIDs)
	}
}

func TestMailSearchRejectsInvertedWindowAndBadSort(t *testing.T) {
	h := newHandles(t)
	base := `{"operation":"mail_search","arguments":{"account_ids":["` + string(h.account) + `"],`
	for name, tail := range map[string]string{
		"inverted window": `"received_after":"2026-09-05T00:00:00Z","received_before":"2026-09-04T00:00:00Z"}}`,
		"equal window":    `"received_after":"2026-09-05T00:00:00Z","received_before":"2026-09-05T00:00:00Z"}}`,
		"unknown sort":    `"sort":"relevance"}}`,
		"local time":      `"received_after":"2026-09-05 00:00:00"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(t, base+tail); err == nil {
				t.Fatalf("mail_search accepted %s", name)
			}
		})
	}
}

func TestMailReadBounds(t *testing.T) {
	h := newHandles(t)
	ids := make([]string, 0, 3)
	for _, id := range []Handle{h.message, h.message2} {
		ids = append(ids, `"`+string(id)+`"`)
	}
	raw := `{"operation":"mail_read","arguments":{"message_ids":[` + strings.Join(ids, ",") + `],"max_body_bytes":1024}}`
	args := mustParse(t, raw).Arguments().(*MailReadArgs)
	if got := args.BodyBytes(Limits{}); got != 1024 {
		t.Fatalf("BodyBytes() = %d, want the requested 1024", got)
	}

	// A request cannot raise the configured cap.
	raw = `{"operation":"mail_read","arguments":{"message_ids":["` + string(h.message) + `"],"max_body_bytes":999999999}}`
	if _, err := parse(t, raw); err == nil {
		t.Fatal("mail_read accepted a body cap above the configured maximum")
	}

	if _, err := parse(t, `{"operation":"mail_read","arguments":{"message_ids":[]}}`); err == nil {
		t.Fatal("mail_read accepted an empty selection")
	}
}

func TestOpenItemAcceptsOnlyItemHandles(t *testing.T) {
	h := newHandles(t)
	if _, err := parse(t, `{"operation":"open_item","arguments":{"item_id":"`+string(h.message)+`"}}`); err != nil {
		t.Fatalf("open_item rejected a message handle: %v", err)
	}
	if _, err := parse(t, `{"operation":"open_item","arguments":{"item_id":"`+string(h.event)+`"}}`); err != nil {
		t.Fatalf("open_item rejected an event handle: %v", err)
	}
	for _, bad := range []string{string(h.account), "https://example.com", "/etc/passwd", "com.apple.mail", ""} {
		raw := `{"operation":"open_item","arguments":{"item_id":"` + bad + `"}}`
		if _, err := parse(t, raw); err == nil {
			t.Errorf("open_item accepted %q", bad)
		}
	}
}

func TestChangeApplyTakesOnlyAPlanID(t *testing.T) {
	if _, err := parse(t, `{"operation":"change_apply","arguments":{"plan_id":"plan_000102030405060708090a0b0c0d0e0f"}}`); err != nil {
		t.Fatalf("change_apply rejected a well-formed plan id: %v", err)
	}
	for _, bad := range []string{"", "plan", "plan_zz", "000102030405060708090a0b0c0d0e0f"} {
		raw := `{"operation":"change_apply","arguments":{"plan_id":"` + bad + `"}}`
		if _, err := parse(t, raw); err == nil {
			t.Errorf("change_apply accepted plan_id %q", bad)
		}
	}
}

func TestCalendarEventsValidatesWindow(t *testing.T) {
	h := newHandles(t)
	base := `{"operation":"calendar_events","arguments":{"calendar_ids":["` + string(h.calendar) + `"],`

	if _, err := parse(t, base+`"start":"2026-09-06T00:00:00+02:00","end":"2026-09-07T00:00:00+02:00","timezone":"Europe/Prague"}}`); err != nil {
		t.Fatalf("calendar_events rejected a valid window: %v", err)
	}

	tests := map[string]struct {
		tail string
		code Code
	}{
		"missing timezone":  {`"start":"2026-09-06T00:00:00Z","end":"2026-09-07T00:00:00Z"}}`, CodeInvalidTimezone},
		"unknown timezone":  {`"start":"2026-09-06T00:00:00Z","end":"2026-09-07T00:00:00Z","timezone":"Mars/Olympus"}}`, CodeInvalidTimezone},
		"local timezone":    {`"start":"2026-09-06T00:00:00Z","end":"2026-09-07T00:00:00Z","timezone":"Local"}}`, CodeInvalidTimezone},
		"offset disagrees":  {`"start":"2026-09-06T00:00:00Z","end":"2026-09-07T00:00:00Z","timezone":"Europe/Prague"}}`, CodeInvalidTimezone},
		"end before start":  {`"start":"2026-09-07T00:00:00Z","end":"2026-09-06T00:00:00Z","timezone":"UTC"}}`, CodeInvalidRequest},
		"empty interval":    {`"start":"2026-09-06T00:00:00Z","end":"2026-09-06T00:00:00Z","timezone":"UTC"}}`, CodeInvalidRequest},
		"window is too big": {`"start":"2026-01-01T00:00:00Z","end":"2026-06-01T00:00:00Z","timezone":"UTC"}}`, CodeInvalidRequest},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parse(t, base+tt.tail)
			if err == nil {
				t.Fatalf("calendar_events accepted %s", name)
			}
			if CodeOf(err) != tt.code {
				t.Fatalf("code = %q, want %q (err %v)", CodeOf(err), tt.code, err)
			}
		})
	}
}

func TestCalendarFreeSlotsValidation(t *testing.T) {
	h := newHandles(t)
	base := `{"operation":"calendar_free_slots","arguments":{"calendar_ids":["` + string(h.calendar) + `"],` +
		`"start":"2026-09-07T00:00:00+02:00","end":"2026-09-08T00:00:00+02:00","timezone":"Europe/Prague",`

	req := mustParse(t, base+`"duration_minutes":45,"working_hours":{"start":"09:00","end":"17:00"}}}`)
	args := req.Arguments().(*CalendarFreeSlotsArgs)
	if args.WorkingHours.Start != 9*60 || args.WorkingHours.End != 17*60 {
		t.Fatalf("working hours = %s-%s, want 09:00-17:00", args.WorkingHours.Start, args.WorkingHours.End)
	}
	days := args.WorkingHours.Weekdays()
	if !days[time.Monday] || days[time.Saturday] {
		t.Fatalf("default weekdays = %v, want Monday-Friday", days)
	}

	for name, tail := range map[string]string{
		"zero duration":       `"duration_minutes":0,"working_hours":{"start":"09:00","end":"17:00"}}}`,
		"negative buffer":     `"duration_minutes":30,"buffer_minutes":-5,"working_hours":{"start":"09:00","end":"17:00"}}}`,
		"inverted hours":      `"duration_minutes":30,"working_hours":{"start":"17:00","end":"09:00"}}}`,
		"duration over hours": `"duration_minutes":600,"working_hours":{"start":"09:00","end":"17:00"}}}`,
		"unknown weekday":     `"duration_minutes":30,"working_hours":{"start":"09:00","end":"17:00","days":["someday"]}}}`,
		"bad clock format":    `"duration_minutes":30,"working_hours":{"start":"9am","end":"17:00"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(t, base+tail); err == nil {
				t.Fatalf("calendar_free_slots accepted %s", name)
			}
		})
	}
}

func TestNewRequestRejectsNilAndUnknown(t *testing.T) {
	if _, err := NewRequest(nil, Limits{}); err == nil {
		t.Fatal("NewRequest accepted nil arguments")
	}
	if req := (Request{}); req.Valid() {
		t.Fatal("the zero Request reports itself valid")
	}
}

func TestLimitsValidate(t *testing.T) {
	if err := DefaultLimits().Validate(); err != nil {
		t.Fatalf("DefaultLimits().Validate() = %v", err)
	}
	if err := (Limits{}).Validate(); err != nil {
		t.Fatalf("zero Limits.Validate() = %v, want the defaults to apply", err)
	}
	bad := []Limits{
		{PageSize: 200, MaxPageSize: 100},
		{MaxBodyBytes: 1 << 20, MaxResultBytes: 1024},
		{MaxMessagesPerRead: 500, MaxPageSize: 100},
		{MaxScanCandidates: 10, MaxPageSize: 100},
		{MaxRequestDepth: 1024},
	}
	for i, l := range bad {
		if err := l.Validate(); err == nil {
			t.Errorf("Limits[%d].Validate() accepted an inconsistent configuration", i)
		}
	}
}

func TestLimitsWithDefaultsFillsEveryField(t *testing.T) {
	got := Limits{PageSize: 5}.withDefaults()
	want := DefaultLimits()
	if got.PageSize != 5 {
		t.Errorf("PageSize = %d, want the configured 5", got.PageSize)
	}
	if got.MaxPageSize != want.MaxPageSize || got.PlanTTL != want.PlanTTL || got.MaxScanCandidates != want.MaxScanCandidates {
		t.Errorf("withDefaults() left a field unset: %+v", got)
	}
}

func TestClipBoundsAndSanitizesUntrustedText(t *testing.T) {
	if got := clip("a\x1b[31mb", 40); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("clip kept a control sequence: %q", got)
	}
	if got := clip(strings.Repeat("x", 100), 10); len([]rune(got)) != 11 {
		t.Fatalf("clip returned %d runes, want 10 plus an ellipsis", len([]rune(got)))
	}
}

func TestErrorMessagesStayModelSafe(t *testing.T) {
	// An invalid enum echoes the offending value, but bounded and sanitized.
	h := newHandles(t)
	raw := `{"operation":"mail_search","arguments":{"account_ids":["` + string(h.account) + `"],"sort":"` + strings.Repeat("z", 200) + `"}}`
	_, err := parse(t, raw)
	if err == nil {
		t.Fatal("mail_search accepted an unknown sort")
	}
	if len(err.Error()) > 200 {
		t.Fatalf("error message is %d bytes, want it bounded: %v", len(err.Error()), err)
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a personal-apps error", err)
	}
}
