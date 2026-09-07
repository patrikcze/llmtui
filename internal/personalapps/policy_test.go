package personalapps

import (
	"errors"
	"testing"
)

func TestScopeFailsClosedOnEmptyAllowlists(t *testing.T) {
	scope := Scope{MailEnabled: true, CalendarEnabled: true}
	if scope.AllowsAccount("acct-1") {
		t.Fatal("an empty account allowlist authorized an account")
	}
	if scope.AllowsCalendar("cal-1") {
		t.Fatal("an empty calendar allowlist authorized a calendar")
	}
	if scope.AllowsMailbox("acct-1", []string{"INBOX"}) {
		t.Fatal("an empty account allowlist authorized a mailbox")
	}
	if !errors.Is(scope.CheckRef(ResourceRef{Kind: KindAccount, AccountID: "acct-1"}), ErrScopeDenied) {
		t.Fatal("CheckRef did not deny an out-of-scope account")
	}
}

func TestScopeMailboxPrefixMatching(t *testing.T) {
	scope := Scope{
		MailEnabled:      true,
		AllowedAccounts:  []string{"acct-1"},
		AllowedMailboxes: []MailboxScope{{AccountID: "acct-1", Path: []string{"INBOX", "Projects"}}},
	}
	tests := map[string]struct {
		account string
		path    []string
		want    bool
	}{
		"exact":                {"acct-1", []string{"INBOX", "Projects"}, true},
		"below":                {"acct-1", []string{"INBOX", "Projects", "2026"}, true},
		"parent is not below":  {"acct-1", []string{"INBOX"}, false},
		"sibling":              {"acct-1", []string{"INBOX", "Personal"}, false},
		"similar name":         {"acct-1", []string{"INBOX", "Projects2"}, false},
		"other account":        {"acct-2", []string{"INBOX", "Projects"}, false},
		"empty path":           {"acct-1", nil, false},
		"case does not differ": {"acct-1", []string{"inbox", "projects"}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := scope.AllowsMailbox(tt.account, tt.path); got != tt.want {
				t.Fatalf("AllowsMailbox(%q, %v) = %v, want %v", tt.account, tt.path, got, tt.want)
			}
		})
	}

	// With no mailbox allowlist, every mailbox of an allowed account is in
	// scope; the account allowlist is still what gates access.
	wide := Scope{MailEnabled: true, AllowedAccounts: []string{"acct-1"}}
	if !wide.AllowsMailbox("acct-1", []string{"INBOX"}) {
		t.Fatal("an allowed account's mailbox was denied with no mailbox allowlist")
	}
	if wide.AllowsMailbox("acct-2", []string{"INBOX"}) {
		t.Fatal("a mailbox of an account outside the allowlist was allowed")
	}
}

func TestScopeCheckOperation(t *testing.T) {
	scope := Scope{MailEnabled: true, AllowedAccounts: []string{"acct-1"}}
	conn := ConnectionState{}

	if err := scope.CheckOperation(OpStatus, conn); err != nil {
		t.Fatalf("status was refused: %v", err)
	}
	if err := scope.CheckOperation(OpMailSearch, conn); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("mail_search error = %v, want ErrNotConnected", err)
	}
	conn.MailConnected = true
	if err := scope.CheckOperation(OpMailSearch, conn); err != nil {
		t.Fatalf("mail_search was refused when connected: %v", err)
	}
	if err := scope.CheckOperation(OpCalendarEvents, conn); !errors.Is(err, ErrDisabled) {
		t.Fatalf("calendar_events error = %v, want ErrDisabled", err)
	}
	for _, op := range []Operation{OpChangePrepare, OpChangeApply} {
		if err := scope.CheckOperation(op, conn); !errors.Is(err, ErrMutationsDisabled) {
			t.Fatalf("%s error = %v, want ErrMutationsDisabled", op, err)
		}
	}
}

func TestScopeAllowedOperations(t *testing.T) {
	scope := Scope{MailEnabled: true, AllowedAccounts: []string{"acct-1"}}
	conn := ConnectionState{MailConnected: true}

	ops := scope.AllowedOperations(conn, true)
	has := func(op Operation) bool {
		for _, candidate := range ops {
			if candidate == op {
				return true
			}
		}
		return false
	}
	if !has(OpStatus) || !has(OpMailSearch) {
		t.Fatalf("Operations = %v, want status and mail_search", ops)
	}
	if has(OpCalendarEvents) || has(OpChangeApply) {
		t.Fatalf("Operations = %v, want the disabled adapter and mutations excluded", ops)
	}

	// An unsupported platform exposes nothing but status.
	if got := scope.AllowedOperations(conn, false); len(got) != 1 || got[0] != OpStatus {
		t.Fatalf("Operations on an unsupported platform = %v, want status only", got)
	}
}

func TestScopeAllowedChangeTypes(t *testing.T) {
	scope := Scope{MailEnabled: true, MutationsEnabled: true, AllowedAccounts: []string{"acct-1"}}
	conn := ConnectionState{MailConnected: true}

	types := scope.AllowedChangeTypes(conn)
	for _, tt := range types {
		if (Change{Type: tt}).Adapter() == AdapterCalendar {
			t.Fatalf("ChangeTypes = %v, want no calendar change while the adapter is off", types)
		}
	}
	if len(types) != 4 {
		t.Fatalf("ChangeTypes = %v, want the four mail variants", types)
	}

	scope.MutationsEnabled = false
	if got := scope.AllowedChangeTypes(conn); got != nil {
		t.Fatalf("ChangeTypes = %v with mutations off, want none", got)
	}
}

func TestScopeNormalizeIsStableAndAddsNothing(t *testing.T) {
	scope := Scope{
		MailEnabled:      true,
		AllowedAccounts:  []string{"b", "a", "a", " "},
		AllowedCalendars: []string{"z", "z"},
		AllowedMailboxes: []MailboxScope{
			{AccountID: "a", Path: []string{"INBOX"}},
			{AccountID: "", Path: []string{"INBOX"}},
			{AccountID: "a", Path: nil},
		},
	}
	got := scope.Normalize()
	if len(got.AllowedAccounts) != 2 || got.AllowedAccounts[0] != "a" {
		t.Fatalf("AllowedAccounts = %v, want a sorted deduplicated list", got.AllowedAccounts)
	}
	if len(got.AllowedCalendars) != 1 {
		t.Fatalf("AllowedCalendars = %v, want the duplicate removed", got.AllowedCalendars)
	}
	if len(got.AllowedMailboxes) != 1 {
		t.Fatalf("AllowedMailboxes = %v, want the incomplete entries dropped", got.AllowedMailboxes)
	}
	// Normalizing must not widen: an account that was never listed stays out.
	if got.AllowsAccount("c") {
		t.Fatal("Normalize added an account")
	}
}

func TestScopeRefKinds(t *testing.T) {
	scope := Scope{
		MailEnabled:      true,
		CalendarEnabled:  true,
		AllowedAccounts:  []string{"acct-1"},
		AllowedCalendars: []string{"cal-1"},
	}
	tests := map[string]struct {
		ref  ResourceRef
		want bool
	}{
		"account":          {ResourceRef{Kind: KindAccount, AccountID: "acct-1"}, true},
		"other account":    {ResourceRef{Kind: KindAccount, AccountID: "acct-2"}, false},
		"message":          {ResourceRef{Kind: KindMessage, AccountID: "acct-1", ContainerPath: []string{"INBOX"}}, true},
		"calendar":         {ResourceRef{Kind: KindCalendar, AccountID: "cal-1", NativeID: "cal-1"}, true},
		"event":            {ResourceRef{Kind: KindEvent, AccountID: "cal-1", NativeID: "e1"}, true},
		"event elsewhere":  {ResourceRef{Kind: KindEvent, AccountID: "cal-9", NativeID: "e1"}, false},
		"unknown resource": {ResourceRef{Kind: "folder"}, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := scope.AllowsRef(tt.ref); got != tt.want {
				t.Fatalf("AllowsRef(%+v) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}
