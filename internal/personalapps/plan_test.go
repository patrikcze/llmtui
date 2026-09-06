package personalapps

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func parseChanges(t *testing.T, changesJSON string) []Change {
	t.Helper()
	req, err := ParseRequest([]byte(`{"operation":"change_prepare","arguments":{"changes":`+changesJSON+`}}`), Limits{})
	if err != nil {
		t.Fatalf("ParseRequest(%s): %v", clip(changesJSON, 160), err)
	}
	return req.Arguments().(*ChangePrepareArgs).Changes
}

func moveJSON(h handles, version string) string {
	return `[{"type":"mail_move","messages":[{"message_id":"` + string(h.message) + `","expected_version":"` + version + `"}],` +
		`"destination_mailbox_id":"` + string(h.mailbox) + `"}]`
}

func TestChangePrepareDecodesVariants(t *testing.T) {
	h := newHandles(t)
	tests := map[string]struct {
		json    string
		kind    ChangeType
		adapter Adapter
	}{
		"move": {moveJSON(h, "v1"), ChangeMailMove, AdapterMail},
		"read": {`[{"type":"mail_set_read","messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"}],"read":true}]`, ChangeMailSetRead, AdapterMail},
		"flag": {`[{"type":"mail_set_flag","messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"}],"flagged":false}]`, ChangeMailSetFlag, AdapterMail},
		"draft": {`[{"type":"mail_save_draft","sender_account_id":"` + string(h.account) + `","to":["a@example.com"],` +
			`"subject":"Hello","body":"Hi there"}]`, ChangeMailSaveDraft, AdapterMail},
		"create event": {`[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"Focus",` +
			`"timezone":"Europe/Prague","start":"2026-09-07T09:00:00+02:00","end":"2026-09-07T10:00:00+02:00"}]`, ChangeCalendarCreateEvent, AdapterCalendar},
		"update event": {`[{"type":"calendar_update_event","event_id":"` + string(h.event) + `","expected_version":"v7","title":"Renamed"}]`, ChangeCalendarUpdateEvent, AdapterCalendar},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			changes := parseChanges(t, tt.json)
			if len(changes) != 1 {
				t.Fatalf("decoded %d changes, want 1", len(changes))
			}
			if changes[0].Type != tt.kind {
				t.Fatalf("Type = %q, want %q", changes[0].Type, tt.kind)
			}
			if got := changes[0].Adapter(); got != tt.adapter {
				t.Fatalf("Adapter() = %q, want %q", got, tt.adapter)
			}
			if len(changes[0].Handles()) == 0 {
				t.Fatal("Handles() returned nothing for a change that names resources")
			}
		})
	}
	if len(tests) != len(ChangeTypes()) {
		t.Fatalf("the variant table covers %d types, the vocabulary has %d", len(tests), len(ChangeTypes()))
	}
}

// A field belonging to another variant must be rejected, not ignored.
func TestChangeRejectsForeignAndUnsupportedVariants(t *testing.T) {
	h := newHandles(t)
	bad := map[string]string{
		"foreign field":     `[{"type":"mail_set_read","messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"}],"read":true,"destination_mailbox_id":"` + string(h.mailbox) + `"}]`,
		"attendees":         `[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"x","timezone":"UTC","start":"2026-09-07T09:00:00Z","end":"2026-09-07T10:00:00Z","attendees":["a@example.com"]}]`,
		"recurrence":        `[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"x","timezone":"UTC","start":"2026-09-07T09:00:00Z","end":"2026-09-07T10:00:00Z","recurrence":"FREQ=WEEKLY"}]`,
		"send":              `[{"type":"mail_send","message_id":"` + string(h.message) + `"}]`,
		"delete":            `[{"type":"mail_delete","messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"}]}]`,
		"trash":             `[{"type":"calendar_delete_event","event_id":"` + string(h.event) + `"}]`,
		"missing type":      `[{"messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"}]}]`,
		"not an object":     `["mail_move"]`,
		"empty change list": `[]`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			_, err := ParseRequest([]byte(`{"operation":"change_prepare","arguments":{"changes":`+raw+`}}`), Limits{})
			if err == nil {
				t.Fatalf("change_prepare accepted %s", name)
			}
		})
	}
}

func TestChangeValidationRules(t *testing.T) {
	h := newHandles(t)
	tests := map[string]string{
		"move without expected version": `[{"type":"mail_move","messages":[{"message_id":"` + string(h.message) + `"}],"destination_mailbox_id":"` + string(h.mailbox) + `"}]`,
		"move to a message handle":      `[{"type":"mail_move","messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"}],"destination_mailbox_id":"` + string(h.message) + `"}]`,
		"move with no messages":         `[{"type":"mail_move","messages":[],"destination_mailbox_id":"` + string(h.mailbox) + `"}]`,
		"draft without body":            `[{"type":"mail_save_draft","sender_account_id":"` + string(h.account) + `","to":["a@example.com"],"subject":"x","body":"   "}]`,
		"draft without recipients":      `[{"type":"mail_save_draft","sender_account_id":"` + string(h.account) + `","subject":"x","body":"hi"}]`,
		"draft without subject":         `[{"type":"mail_save_draft","sender_account_id":"` + string(h.account) + `","to":["a@example.com"],"body":"hi"}]`,
		"draft with a bad address":      `[{"type":"mail_save_draft","sender_account_id":"` + string(h.account) + `","to":["not an address"],"subject":"x","body":"hi"}]`,
		"reply that sets a subject":     `[{"type":"mail_save_draft","sender_account_id":"` + string(h.account) + `","in_reply_to_message_id":"` + string(h.message) + `","subject":"Re: x","body":"hi"}]`,
		"quote without a source":        `[{"type":"mail_save_draft","sender_account_id":"` + string(h.account) + `","to":["a@example.com"],"subject":"x","body":"hi","include_quote":true}]`,
		"event without a title":         `[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"  ","timezone":"UTC","start":"2026-09-07T09:00:00Z","end":"2026-09-07T10:00:00Z"}]`,
		"event without an end":          `[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"x","timezone":"UTC","start":"2026-09-07T09:00:00Z"}]`,
		"event timed and all-day":       `[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"x","timezone":"UTC","start":"2026-09-07T09:00:00Z","end":"2026-09-07T10:00:00Z","all_day_start":"2026-09-07","all_day_end":"2026-09-08"}]`,
		"event with no timing":          `[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"x","timezone":"UTC"}]`,
		"event end before start":        `[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"x","timezone":"UTC","start":"2026-09-07T10:00:00Z","end":"2026-09-07T09:00:00Z"}]`,
		"event offset disagrees":        `[{"type":"calendar_create_event","calendar_id":"` + string(h.calendar) + `","title":"x","timezone":"Europe/Prague","start":"2026-09-07T09:00:00Z","end":"2026-09-07T10:00:00Z"}]`,
		"update without a version":      `[{"type":"calendar_update_event","event_id":"` + string(h.event) + `","title":"x"}]`,
		"update with nothing to change": `[{"type":"calendar_update_event","event_id":"` + string(h.event) + `","expected_version":"v1"}]`,
		"update blanking the title":     `[{"type":"calendar_update_event","event_id":"` + string(h.event) + `","expected_version":"v1","title":"  "}]`,
		"duplicate message target":      `[{"type":"mail_set_read","messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"},{"message_id":"` + string(h.message) + `","expected_version":"v2"}],"read":true}]`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseRequest([]byte(`{"operation":"change_prepare","arguments":{"changes":`+raw+`}}`), Limits{})
			if err == nil {
				t.Fatalf("change_prepare accepted %s", name)
			}
			if CodeOf(err) != CodeInvalidRequest && CodeOf(err) != CodeInvalidTimezone {
				t.Fatalf("code = %q for %s (err %v)", CodeOf(err), name, err)
			}
		})
	}
}

func TestChangePrepareRespectsTheChangeBudget(t *testing.T) {
	h := newHandles(t)
	one := `{"type":"mail_set_flag","messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"}],"flagged":true}`
	many := "[" + strings.TrimSuffix(strings.Repeat(one+",", 30), ",") + "]"
	if _, err := ParseRequest([]byte(`{"operation":"change_prepare","arguments":{"changes":`+many+`}}`), Limits{}); err == nil {
		t.Fatal("change_prepare accepted more changes than the plan budget allows")
	}
}

func TestChangeItemCount(t *testing.T) {
	h := newHandles(t)
	raw := `[{"type":"mail_move","messages":[{"message_id":"` + string(h.message) + `","expected_version":"v1"},` +
		`{"message_id":"` + string(h.message2) + `","expected_version":"v2"}],"destination_mailbox_id":"` + string(h.mailbox) + `"}]`
	changes := parseChanges(t, raw)
	if got := changes[0].ItemCount(); got != 2 {
		t.Fatalf("ItemCount() = %d, want 2", got)
	}
}

func TestPlanDigestIsStableAcrossEquivalentOrderings(t *testing.T) {
	h := newHandles(t)
	first := parseChanges(t, `[{"type":"mail_set_read","messages":[{"message_id":"`+string(h.message)+`","expected_version":"v1"},{"message_id":"`+string(h.message2)+`","expected_version":"v2"}],"read":true}]`)
	second := parseChanges(t, `[{"type":"mail_set_read","messages":[{"message_id":"`+string(h.message2)+`","expected_version":"v2"},{"message_id":"`+string(h.message)+`","expected_version":"v1"}],"read":true}]`)

	a, err := PlanDigest(first)
	if err != nil {
		t.Fatalf("PlanDigest: %v", err)
	}
	b, err := PlanDigest(second)
	if err != nil {
		t.Fatalf("PlanDigest: %v", err)
	}
	if a != b {
		t.Fatalf("equivalent change sets digested differently:\n%s\n%s", a, b)
	}
}

func TestPlanDigestChangesWithMaterialEdits(t *testing.T) {
	h := newHandles(t)
	base := parseChanges(t, moveJSON(h, "v1"))
	baseDigest, err := PlanDigest(base)
	if err != nil {
		t.Fatalf("PlanDigest: %v", err)
	}

	edits := map[string][]Change{
		"different destination": parseChanges(t, `[{"type":"mail_move","messages":[{"message_id":"`+string(h.message)+`","expected_version":"v1"}],"destination_mailbox_id":"`+string(h.mailbox2)+`"}]`),
		"different expectation": parseChanges(t, moveJSON(h, "v2")),
		"different message":     parseChanges(t, `[{"type":"mail_move","messages":[{"message_id":"`+string(h.message2)+`","expected_version":"v1"}],"destination_mailbox_id":"`+string(h.mailbox)+`"}]`),
		"extra target":          parseChanges(t, `[{"type":"mail_move","messages":[{"message_id":"`+string(h.message)+`","expected_version":"v1"},{"message_id":"`+string(h.message2)+`","expected_version":"v2"}],"destination_mailbox_id":"`+string(h.mailbox)+`"}]`),
	}
	for name, changes := range edits {
		t.Run(name, func(t *testing.T) {
			digest, err := PlanDigest(changes)
			if err != nil {
				t.Fatalf("PlanDigest: %v", err)
			}
			if digest == baseDigest {
				t.Fatalf("%s produced the same digest, so an approval would carry over", name)
			}
		})
	}
}

// A draft's body and recipients are part of the digest: editing either after
// the human reviewed the preview must invalidate the approval.
func TestPlanDigestCoversDraftContent(t *testing.T) {
	h := newHandles(t)
	draft := func(to, body string) []Change {
		return parseChanges(t, `[{"type":"mail_save_draft","sender_account_id":"`+string(h.account)+`","to":["`+to+`"],"subject":"x","body":"`+body+`"}]`)
	}
	base, _ := PlanDigest(draft("a@example.com", "hello"))
	for name, changes := range map[string][]Change{
		"edited body":      draft("a@example.com", "hello, and wire the money"),
		"edited recipient": draft("attacker@example.com", "hello"),
	} {
		digest, _ := PlanDigest(changes)
		if digest == base {
			t.Fatalf("%s did not change the plan digest", name)
		}
	}
}

func TestPlanStoreLifecycle(t *testing.T) {
	h := newHandles(t)
	clock := newFakeClock()
	store := NewPlanStore(PlanStoreOptions{TTL: 5 * time.Minute, Now: clock.Now})
	changes := parseChanges(t, moveJSON(h, "v1"))

	plan, err := store.Prepare(changes)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !validPlanID(plan.ID) {
		t.Fatalf("plan id %q is not host-issued shape", plan.ID)
	}
	if plan.ItemCount != 1 || len(plan.Adapters) != 1 || plan.Adapters[0] != AdapterMail {
		t.Fatalf("plan summary = %+v, want one mail item", plan)
	}
	if !strings.HasSuffix(plan.ApprovalKey(), plan.Digest) {
		t.Fatalf("ApprovalKey() = %q, want it bound to the digest", plan.ApprovalKey())
	}
	if !strings.Contains(plan.ApprovalKey(), string(OpChangeApply)) {
		t.Fatalf("ApprovalKey() = %q, want it scoped to change_apply", plan.ApprovalKey())
	}

	if _, err := store.Get(plan.ID); err != nil {
		t.Fatalf("Get: %v", err)
	}
	// Get does not consume: a preview can be rendered more than once.
	if got := store.Len(); got != 1 {
		t.Fatalf("Len() after Get = %d, want 1", got)
	}

	consumed, err := store.Consume(plan.ID)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if consumed.Digest != plan.Digest {
		t.Fatalf("Consume returned digest %q, want %q", consumed.Digest, plan.Digest)
	}
	if _, err := store.Consume(plan.ID); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("a plan was applied twice: %v", err)
	}
}

func TestPlanStoreExpiry(t *testing.T) {
	h := newHandles(t)
	clock := newFakeClock()
	store := NewPlanStore(PlanStoreOptions{TTL: 5 * time.Minute, Now: clock.Now})
	plan, err := store.Prepare(parseChanges(t, moveJSON(h, "v1")))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	clock.advance(4 * time.Minute)
	if _, err := store.Get(plan.ID); err != nil {
		t.Fatalf("Get before expiry: %v", err)
	}
	clock.advance(2 * time.Minute)
	if !plan.Expired(clock.Now()) {
		t.Fatal("Expired() reported false past the plan TTL")
	}
	if _, err := store.Consume(plan.ID); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("an expired plan was applied: %v", err)
	}
}

func TestPlanStoreBoundsActivePlans(t *testing.T) {
	h := newHandles(t)
	store := NewPlanStore(PlanStoreOptions{Max: 2, Now: newFakeClock().Now})
	for i, version := range []string{"v1", "v2"} {
		if _, err := store.Prepare(parseChanges(t, moveJSON(h, version))); err != nil {
			t.Fatalf("Prepare %d: %v", i, err)
		}
	}
	_, err := store.Prepare(parseChanges(t, moveJSON(h, "v3")))
	if CodeOf(err) != CodeRateLimited {
		t.Fatalf("code = %q, want %q when the plan budget is full", CodeOf(err), CodeRateLimited)
	}
}

func TestPlanChangesAreImmutable(t *testing.T) {
	h := newHandles(t)
	store := NewPlanStore(PlanStoreOptions{Now: newFakeClock().Now})
	changes := parseChanges(t, moveJSON(h, "v1"))

	plan, err := store.Prepare(changes)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// Editing the caller's slice must not reach the stored plan.
	changes[0].MailMove.DestinationMailboxID = h.mailbox2
	stored, err := store.Get(plan.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := stored.Changes()[0].MailMove.DestinationMailboxID; got == h.mailbox2 {
		t.Fatal("the prepared plan followed an edit made after preparation")
	}

	// The returned slice header is a copy too.
	view := stored.Changes()
	view[0] = Change{}
	if stored.Changes()[0].Type != ChangeMailMove {
		t.Fatal("Changes() handed out the plan's own slice")
	}
}

func TestPlanStoreResetAndDiscard(t *testing.T) {
	h := newHandles(t)
	store := NewPlanStore(PlanStoreOptions{Now: newFakeClock().Now})
	first, _ := store.Prepare(parseChanges(t, moveJSON(h, "v1")))
	second, _ := store.Prepare(parseChanges(t, moveJSON(h, "v2")))

	store.Discard(first.ID)
	if _, err := store.Get(first.ID); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("a denied plan survived Discard: %v", err)
	}
	store.Reset()
	if _, err := store.Get(second.ID); !errors.Is(err, ErrPlanNotFound) {
		t.Fatalf("a plan survived Reset: %v", err)
	}
	if got := store.Len(); got != 0 {
		t.Fatalf("Len() after Reset = %d, want 0", got)
	}
}

func TestPlanStoreRejectsAnEmptyPlan(t *testing.T) {
	store := NewPlanStore(PlanStoreOptions{Now: newFakeClock().Now})
	if _, err := store.Prepare(nil); err == nil {
		t.Fatal("Prepare accepted an empty change set")
	}
}

func TestValidPlanID(t *testing.T) {
	store := NewPlanStore(PlanStoreOptions{Now: newFakeClock().Now})
	h := newHandles(t)
	plan, err := store.Prepare(parseChanges(t, moveJSON(h, "v1")))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !validPlanID(plan.ID) {
		t.Fatalf("validPlanID(%q) = false for a freshly issued plan", plan.ID)
	}
	for _, bad := range []string{"", "plan_", "plan_00", strings.Repeat("f", 32), "plan_" + strings.Repeat("g", 32)} {
		if validPlanID(bad) {
			t.Errorf("validPlanID(%q) = true", bad)
		}
	}
}
