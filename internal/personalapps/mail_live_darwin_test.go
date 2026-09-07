package personalapps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveMailBridgeSmoke exercises the real NewMailBackend adapter against
// whatever Mail accounts are configured on the machine running the test. It
// is read-only: nothing it calls can create, move, flag, delete or send
// anything. It is skipped by default — set LLMTUI_TEST_LIVE_MAIL=1 to run
// it, on a machine with Mail.app configured and Automation access already
// granted to this process (the test does not attempt to walk through a
// first-run consent dialog). It intentionally never logs message subjects,
// senders or body text: only counts, booleans and timestamps, so a run's
// output is safe to paste into an issue or CI log.
//
// This mirrors the existing opt-in pattern for
// internal/provider/embedded/llamart's native integration tests: `go test
// ./...` does not exercise this by default, and that is expected.
func TestLiveMailBridgeSmoke(t *testing.T) {
	if os.Getenv("LLMTUI_TEST_LIVE_MAIL") != "1" {
		t.Skip("set LLMTUI_TEST_LIVE_MAIL=1 to run against a real, already-authorized Mail.app")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	backend := NewMailBackend(MailBackendOptions{Timeout: 20 * time.Second})

	accounts, err := backend.Accounts(ctx)
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) == 0 {
		t.Fatal("expected at least one Mail account on this machine")
	}
	t.Logf("found %d account(s)", len(accounts))

	acct := accounts[0]
	boxes, err := backend.Mailboxes(ctx, acct.Ref, ResourceRef{})
	if err != nil {
		t.Fatalf("Mailboxes: %v", err)
	}
	if len(boxes) == 0 {
		t.Fatal("expected at least one mailbox in the first account")
	}
	t.Logf("first account has %d top-level mailbox(es)", len(boxes))

	var inbox *BackendMailbox
	for i := range boxes {
		if strings.EqualFold(boxes[i].DisplayName, "inbox") {
			inbox = &boxes[i]
			break
		}
	}
	if inbox == nil {
		t.Skip("first account has no top-level Inbox; skipping search/read checks")
	}

	page, err := backend.Search(ctx, MailQuery{
		Mailboxes:     []ResourceRef{inbox.Ref},
		Limit:         3,
		MaxCandidates: 200,
		Newest:        true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	t.Logf("search scanned=%d complete=%t reason=%q returned=%d", page.Scanned, page.Complete, page.Reason, len(page.Messages))
	for i := 1; i < len(page.Messages); i++ {
		if page.Messages[i].Received.After(page.Messages[i-1].Received) {
			t.Fatalf("results are not sorted newest-first at index %d", i)
		}
	}
	if len(page.Messages) == 0 {
		t.Skip("inbox returned zero messages; nothing further to verify")
	}

	refs := make([]ResourceRef, 0, len(page.Messages))
	for _, m := range page.Messages {
		refs = append(refs, m.Ref)
	}
	full, err := backend.Messages(ctx, refs, 500)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(full) != len(refs) {
		t.Fatalf("expected %d messages back, got %d", len(refs), len(full))
	}
	const requestedCap = 500
	for _, m := range full {
		t.Logf("message: body_available=%t body_truncated=%t body_len=%d attachments=%d",
			m.BodyAvailable, m.BodyTruncated, len(m.Body), len(m.AttachmentInfo))
		// The adapter truncates by character count, not byte count, so
		// multi-byte UTF-8 content can overshoot the requested cap in
		// bytes; Service.mailRead's clipBody is what enforces an exact
		// byte cap for what a model actually sees (see its doc comment).
		// This only guards against the adapter returning something wildly
		// larger than asked, which would be a real bug.
		if m.BodyAvailable && len(m.Body) > requestedCap*4 {
			t.Fatalf("body far exceeded the requested cap: %d bytes for a %d-byte request", len(m.Body), requestedCap)
		}
	}
}

// TestLiveMailBridgeThroughService drives the same real adapter through the
// actual Service the tool loop uses — scope enforcement, handle minting,
// JSON request decoding and the byte-exact clipBody cap included — rather
// than calling the backend directly. Same opt-in gate and same
// no-content-logged discipline as TestLiveMailBridgeSmoke.
func TestLiveMailBridgeThroughService(t *testing.T) {
	if os.Getenv("LLMTUI_TEST_LIVE_MAIL") != "1" {
		t.Skip("set LLMTUI_TEST_LIVE_MAIL=1 to run against a real, already-authorized Mail.app")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	backend := NewMailBackend(MailBackendOptions{Timeout: 20 * time.Second})
	discovery, err := backend.Accounts(ctx)
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(discovery) == 0 {
		t.Fatal("expected at least one Mail account on this machine")
	}

	gated := false
	svc, err := New(Options{
		Scope: Scope{
			MailEnabled:     true,
			AllowedAccounts: []string{discovery[0].Ref.AccountID},
		},
		Limits: Limits{MaxBodyBytes: 300},
		Mail:   backend,
		PrivacyGate: func(context.Context) error {
			gated = true
			return nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Connect(AdapterMail); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	accountsRes := svc.ExecuteRaw(ctx, []byte(`{"operation":"mail_accounts"}`))
	if !gated {
		t.Fatal("expected the privacy gate to have been called before any content-bearing read")
	}
	if accountsRes.Status != StatusOK {
		t.Fatalf("mail_accounts status: %v (error: %+v)", accountsRes.Status, accountsRes.Error)
	}
	var accountsOut struct {
		Accounts []AccountView `json:"accounts"`
	}
	decodeInto(t, accountsRes.Data, &accountsOut)
	if len(accountsOut.Accounts) != 1 {
		t.Fatalf("expected exactly the one allowlisted account, got %d", len(accountsOut.Accounts))
	}
	accountHandle := accountsOut.Accounts[0].ID
	if strings.Contains(string(accountHandle), discovery[0].Ref.AccountID) {
		t.Fatal("model-facing account id must be an opaque handle, not the native account id")
	}

	mailboxesReq := fmt.Sprintf(`{"operation":"mail_mailboxes","arguments":{"account_id":%q}}`, accountHandle)
	mailboxesRes := svc.ExecuteRaw(ctx, []byte(mailboxesReq))
	if mailboxesRes.Status != StatusOK {
		t.Fatalf("mail_mailboxes status: %v (error: %+v)", mailboxesRes.Status, mailboxesRes.Error)
	}
	var mailboxesOut struct {
		Mailboxes []MailboxView `json:"mailboxes"`
	}
	decodeInto(t, mailboxesRes.Data, &mailboxesOut)
	var inboxHandle Handle
	for _, mb := range mailboxesOut.Mailboxes {
		if strings.EqualFold(mb.Name, "inbox") {
			inboxHandle = mb.ID
			break
		}
	}
	if inboxHandle == "" {
		t.Skip("allowlisted account has no top-level Inbox; skipping search/read checks")
	}

	searchReq := fmt.Sprintf(`{"operation":"mail_search","arguments":{"mailbox_ids":[%q],"limit":2}}`, inboxHandle)
	searchRes := svc.ExecuteRaw(ctx, []byte(searchReq))
	if searchRes.Status != StatusOK && searchRes.Status != StatusPartial {
		t.Fatalf("mail_search status: %v (error: %+v)", searchRes.Status, searchRes.Error)
	}
	var searchOut struct {
		Messages []MessageSummaryView `json:"messages"`
	}
	decodeInto(t, searchRes.Data, &searchOut)
	t.Logf("mail_search status=%s coverage=%+v returned=%d", searchRes.Status, searchRes.Coverage, len(searchOut.Messages))
	if len(searchOut.Messages) == 0 {
		t.Skip("inbox returned zero messages; nothing further to verify")
	}

	readReq := fmt.Sprintf(`{"operation":"mail_read","arguments":{"message_ids":[%q]}}`, searchOut.Messages[0].ID)
	readRes := svc.ExecuteRaw(ctx, []byte(readReq))
	if readRes.Status != StatusOK && readRes.Status != StatusPartial {
		t.Fatalf("mail_read status: %v (error: %+v)", readRes.Status, readRes.Error)
	}
	var readOut struct {
		Messages []MessageView `json:"messages"`
	}
	decodeInto(t, readRes.Data, &readOut)
	if len(readOut.Messages) != 1 {
		t.Fatalf("expected exactly 1 message back, got %d", len(readOut.Messages))
	}
	m := readOut.Messages[0]
	t.Logf("mail_read: body_available=%t body_truncated=%t body_len=%d", m.BodyAvailable, m.BodyTruncated, len(m.Body))
	if m.BodyAvailable && len(m.Body) > 300 {
		t.Fatalf("Service must enforce the configured byte-exact body cap, got %d bytes", len(m.Body))
	}
}

// TestLiveMailBridgeSearchScansFromTheCorrectEnd guards a real regression
// found via live use: opSearch's bounded scan assumed mailbox.messages()
// always places the newest message at the high index and walked from there
// downward. For at least one real Gmail/IMAP account that assumption was
// backwards — index 0 held the newest message — so a limit-bounded
// "newest first" search silently returned the *oldest* messages it found
// near the wrong end instead of an error, discovered only because the
// dates displayed were months old. detectNewestAtIndexZero (in the script)
// now checks per mailbox instead of assuming a fixed direction; this proves
// it end-to-end against live data rather than a fake, since the bug lived
// entirely in scan-direction logic a fake response can't exercise.
//
// The check: a small "newest first" page and a small "oldest first" page
// from the same mailbox must not overlap in time — every message in the
// newest page must be at least as recent as every message in the oldest
// page. A mailbox scan starting from the wrong end would violate this
// immediately for any mailbox with more history than the two page sizes
// combined.
func TestLiveMailBridgeSearchScansFromTheCorrectEnd(t *testing.T) {
	if os.Getenv("LLMTUI_TEST_LIVE_MAIL") != "1" {
		t.Skip("set LLMTUI_TEST_LIVE_MAIL=1 to run against a real, already-authorized Mail.app")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	backend := NewMailBackend(MailBackendOptions{Timeout: 20 * time.Second})
	accounts, err := backend.Accounts(ctx)
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	var inbox *ResourceRef
	for _, acct := range accounts {
		boxes, err := backend.Mailboxes(ctx, acct.Ref, ResourceRef{})
		if err != nil {
			continue
		}
		for i := range boxes {
			if strings.EqualFold(boxes[i].DisplayName, "inbox") && boxes[i].TotalCount > 20 {
				inbox = &boxes[i].Ref
				break
			}
		}
		if inbox != nil {
			break
		}
	}
	if inbox == nil {
		t.Skip("no account has an Inbox with enough history to distinguish scan direction")
	}

	newest, err := backend.Search(ctx, MailQuery{Mailboxes: []ResourceRef{*inbox}, Limit: 3, MaxCandidates: 200, Newest: true})
	if err != nil {
		t.Fatalf("Search (newest): %v", err)
	}
	oldest, err := backend.Search(ctx, MailQuery{Mailboxes: []ResourceRef{*inbox}, Limit: 3, MaxCandidates: 200, Newest: false})
	if err != nil {
		t.Fatalf("Search (oldest): %v", err)
	}
	if len(newest.Messages) == 0 || len(oldest.Messages) == 0 {
		t.Skip("inbox returned no messages for one direction; nothing to compare")
	}

	oldestOfNewest := newest.Messages[0].Received
	for _, m := range newest.Messages {
		if m.Received.Before(oldestOfNewest) {
			oldestOfNewest = m.Received
		}
	}
	newestOfOldest := oldest.Messages[0].Received
	for _, m := range oldest.Messages {
		if m.Received.After(newestOfOldest) {
			newestOfOldest = m.Received
		}
	}
	t.Logf("newest-first page: %d messages, oldest of them %s", len(newest.Messages), oldestOfNewest)
	t.Logf("oldest-first page: %d messages, newest of them %s", len(oldest.Messages), newestOfOldest)
	if oldestOfNewest.Before(newestOfOldest) {
		t.Fatalf("newest-first search returned messages older than the oldest-first search — the scan is reading from the wrong end of the mailbox (oldest-of-newest=%s, newest-of-oldest=%s)", oldestOfNewest, newestOfOldest)
	}
}

// decodeInto round-trips Result.Data through JSON exactly like the real
// personal_apps tool boundary does, instead of type-asserting the in-process
// any value directly.
func decodeInto(t *testing.T, data any, out any) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal result data: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
}
