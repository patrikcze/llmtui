package personalapps

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// fakeBridgeRunner lets the protocol logic in mail_bridge.go be tested
// without osascript or Mail.app. It records the last request it saw and
// returns whatever response/error the test configured.
type fakeBridgeRunner struct {
	lastRequest bridgeRequest
	response    bridgeResponse
	rawOverride []byte // when set, returned verbatim instead of marshaling response
	err         error
}

func (f *fakeBridgeRunner) run(_ context.Context, request []byte) ([]byte, error) {
	_ = json.Unmarshal(request, &f.lastRequest)
	if f.err != nil {
		return nil, f.err
	}
	if f.rawOverride != nil {
		return f.rawOverride, nil
	}
	if f.response.Version == 0 {
		f.response.Version = bridgeVersion
	}
	if f.response.Op == "" {
		f.response.Op = f.lastRequest.Op
	}
	return json.Marshal(f.response)
}

func TestJXAMailBackendAccounts(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{
		Accounts: []bridgeAccount{{ID: "acct-1", Name: "iCloud"}, {ID: "", Name: "bad"}},
	}}
	b := newJXAMailBackend(fake)
	accounts, err := b.Accounts(context.Background())
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected the empty-id account to be dropped, got %d accounts", len(accounts))
	}
	if accounts[0].Ref.AccountID != "acct-1" || accounts[0].DisplayName != "iCloud" {
		t.Fatalf("unexpected account: %+v", accounts[0])
	}
	if accounts[0].Ref.Kind != KindAccount || accounts[0].Ref.Adapter != AdapterMail {
		t.Fatalf("unexpected ref shape: %+v", accounts[0].Ref)
	}
	if fake.lastRequest.Op != "accounts" || fake.lastRequest.Version != bridgeVersion {
		t.Fatalf("unexpected request sent: %+v", fake.lastRequest)
	}
}

func TestJXAMailBackendMailboxesBuildsPathFromParent(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{
		Mailboxes: []bridgeMailbox{
			{Name: "Conflicts", PathSegment: "Conflicts", Unread: 1, Total: 2},
		},
	}}
	b := newJXAMailBackend(fake)
	account := ResourceRef{Kind: KindAccount, Adapter: AdapterMail, AccountID: "acct-1"}
	parent := ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "acct-1", ContainerPath: []string{"Sync Issues"}}

	boxes, err := b.Mailboxes(context.Background(), account, parent)
	if err != nil {
		t.Fatalf("Mailboxes: %v", err)
	}
	if len(boxes) != 1 {
		t.Fatalf("expected 1 mailbox, got %d", len(boxes))
	}
	want := []string{"Sync Issues", "Conflicts"}
	got := boxes[0].Ref.ContainerPath
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected path %v, got %v", want, got)
	}
	if fake.lastRequest.ParentPath == nil || fake.lastRequest.ParentPath[0] != "Sync Issues" {
		t.Fatalf("expected parent_path to be sent, got %+v", fake.lastRequest.ParentPath)
	}

	// Mutating the returned slice must never reach back into a later call's
	// parent argument — ResourceRef.ContainerPath must be an owned copy.
	got[0] = "tampered"
	if parent.ContainerPath[0] != "Sync Issues" {
		t.Fatal("mutating a returned mailbox path leaked into the caller's ResourceRef")
	}
}

func TestJXAMailBackendMailboxesRequiresAccount(t *testing.T) {
	b := newJXAMailBackend(&fakeBridgeRunner{})
	if _, err := b.Mailboxes(context.Background(), ResourceRef{}, ResourceRef{}); err == nil {
		t.Fatal("expected an error for a missing account")
	}
}

func TestJXAMailBackendSearchResolvesAccountToInbox(t *testing.T) {
	inboxCall := 0
	fake := &multiCallRunner{
		steps: []func(bridgeRequest) (bridgeResponse, error){
			func(req bridgeRequest) (bridgeResponse, error) {
				inboxCall++
				if req.Op != "mailboxes" {
					t.Fatalf("expected the first call to resolve mailboxes, got %q", req.Op)
				}
				return bridgeResponse{Mailboxes: []bridgeMailbox{
					{Name: "Archive", PathSegment: "Archive"},
					{Name: "INBOX", PathSegment: "INBOX"},
				}}, nil
			},
			func(req bridgeRequest) (bridgeResponse, error) {
				if req.Op != "search" {
					t.Fatalf("expected the second call to search, got %q", req.Op)
				}
				if len(req.Search.Scopes) != 1 || req.Search.Scopes[0].Path[0] != "INBOX" {
					t.Fatalf("expected search to be scoped to INBOX, got %+v", req.Search.Scopes)
				}
				return bridgeResponse{Page: &bridgePage{Complete: true}}, nil
			},
		},
	}
	b := newJXAMailBackend(fake)
	account := ResourceRef{Kind: KindAccount, Adapter: AdapterMail, AccountID: "acct-1"}
	_, err := b.Search(context.Background(), MailQuery{Accounts: []ResourceRef{account}, Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if inboxCall != 1 {
		t.Fatalf("expected exactly one mailbox-resolution call, got %d", inboxCall)
	}
}

func TestJXAMailBackendSearchNoInboxIsAnError(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{Mailboxes: []bridgeMailbox{{Name: "Archive", PathSegment: "Archive"}}}}
	b := newJXAMailBackend(fake)
	account := ResourceRef{Kind: KindAccount, Adapter: AdapterMail, AccountID: "acct-1"}
	_, err := b.Search(context.Background(), MailQuery{Accounts: []ResourceRef{account}, Limit: 10})
	if err == nil {
		t.Fatal("expected an error when the account has no Inbox mailbox")
	}
}

func TestJXAMailBackendSearchRoundTripsResults(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{Page: &bridgePage{
		Messages: []bridgeMessage{{
			AccountID: "acct-1", Path: []string{"INBOX"}, NativeID: "42", MessageID: "rfc-1",
			Subject: "hi", From: "a@example.com", Received: "2026-01-02T03:04:05Z",
			Unread: true, Flagged: false,
		}},
		Scanned: 5, Complete: true,
	}}}
	b := newJXAMailBackend(fake)
	mailbox := ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "acct-1", ContainerPath: []string{"INBOX"}}
	page, err := b.Search(context.Background(), MailQuery{Mailboxes: []ResourceRef{mailbox}, Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.Scanned != 5 || !page.Complete {
		t.Fatalf("unexpected coverage: %+v", page)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(page.Messages))
	}
	m := page.Messages[0]
	if m.Ref.NativeID != "42" || m.Ref.ExternalID != "rfc-1" || m.Subject != "hi" {
		t.Fatalf("unexpected message: %+v", m)
	}
	wantReceived := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if !m.Received.Equal(wantReceived) {
		t.Fatalf("unexpected received time: %v", m.Received)
	}
}

func TestJXAMailBackendSearchCursorRoundTrip(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{Page: &bridgePage{
		Complete: false, Reason: "page_limit",
		Resume: &bridgeResume{ScopeIndex: 0, MessageIndex: 7},
	}}}
	b := newJXAMailBackend(fake)
	mailbox := ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "acct-1", ContainerPath: []string{"INBOX"}}
	query := MailQuery{Mailboxes: []ResourceRef{mailbox}, Limit: 10}

	page, err := b.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("expected a next cursor for an incomplete page")
	}

	// Feeding the cursor back into the identical query must reach the
	// script as a resume position.
	query.Cursor = page.NextCursor
	fake.response = bridgeResponse{Page: &bridgePage{Complete: true}}
	if _, err := b.Search(context.Background(), query); err != nil {
		t.Fatalf("Search (resumed): %v", err)
	}
	if fake.lastRequest.Search.Resume == nil || fake.lastRequest.Search.Resume.MessageIndex != 7 {
		t.Fatalf("expected the resume position to reach the script, got %+v", fake.lastRequest.Search.Resume)
	}
}

func TestJXAMailBackendSearchCursorFromDifferentQueryIsIgnored(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{Page: &bridgePage{
		Complete: false, Resume: &bridgeResume{ScopeIndex: 0, MessageIndex: 3},
	}}}
	b := newJXAMailBackend(fake)
	mailbox := ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "acct-1", ContainerPath: []string{"INBOX"}}
	page, err := b.Search(context.Background(), MailQuery{Mailboxes: []ResourceRef{mailbox}, Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Reuse the cursor against a search with a different filter. It must
	// not be honored — the scan should restart rather than resume with a
	// mismatched position.
	fake.response = bridgeResponse{Page: &bridgePage{Complete: true}}
	other := MailQuery{Mailboxes: []ResourceRef{mailbox}, Limit: 10, Cursor: page.NextCursor, Subject: "different"}
	if _, err := b.Search(context.Background(), other); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if fake.lastRequest.Search.Resume != nil {
		t.Fatalf("expected a mismatched cursor to be ignored, got resume %+v", fake.lastRequest.Search.Resume)
	}
}

func TestJXAMailBackendMessagesRoundTrip(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{Messages: []bridgeMessage{{
		AccountID: "acct-1", Path: []string{"INBOX"}, NativeID: "42",
		Subject: "hi", From: "a@example.com", Received: "2026-01-02T03:04:05Z",
		Body: "hello", BodyAvailable: true,
		Attachments: []bridgeAttachment{{Name: "a.pdf", Size: 100}},
	}}}}
	b := newJXAMailBackend(fake)
	ref := ResourceRef{Kind: KindMessage, Adapter: AdapterMail, AccountID: "acct-1", ContainerPath: []string{"INBOX"}, NativeID: "42"}
	msgs, err := b.Messages(context.Background(), []ResourceRef{ref}, 1024)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if !m.BodyAvailable || m.Body != "hello" {
		t.Fatalf("unexpected body: %+v", m)
	}
	if len(m.AttachmentInfo) != 1 || m.AttachmentInfo[0].Name != "a.pdf" {
		t.Fatalf("unexpected attachments: %+v", m.AttachmentInfo)
	}
	if fake.lastRequest.Messages.MaxBodyChars != 1024 {
		t.Fatalf("expected max_body_chars to be forwarded, got %d", fake.lastRequest.Messages.MaxBodyChars)
	}
}

func TestJXAMailBackendMessagesEmptyRefsIsNoop(t *testing.T) {
	fake := &fakeBridgeRunner{}
	b := newJXAMailBackend(fake)
	msgs, err := b.Messages(context.Background(), nil, 1024)
	if err != nil || msgs != nil {
		t.Fatalf("expected a no-op for empty refs, got (%v, %v)", msgs, err)
	}
	if fake.lastRequest.Op != "" {
		t.Fatal("expected no request to be sent for empty refs")
	}
}

func TestJXAMailBackendUnparsableReceivedTimeIsProtocolError(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{Messages: []bridgeMessage{{
		AccountID: "acct-1", Path: []string{"INBOX"}, NativeID: "42", Received: "not-a-time",
	}}}}
	b := newJXAMailBackend(fake)
	ref := ResourceRef{Kind: KindMessage, Adapter: AdapterMail, AccountID: "acct-1", NativeID: "42"}
	_, err := b.Messages(context.Background(), []ResourceRef{ref}, 100)
	if CodeOf(err) != CodeBridgeProtocolError {
		t.Fatalf("expected CodeBridgeProtocolError, got %v (%v)", CodeOf(err), err)
	}
}

func TestJXAMailBackendBridgeErrorMapsToDomainCode(t *testing.T) {
	cases := []struct {
		bridgeCode string
		want       Code
	}{
		{"permission_denied", CodePermissionDenied},
		{"app_unavailable", CodeAppUnavailable},
		{"not_found", CodeStaleReference},
		{"invalid_request", CodeInvalidRequest},
		{"something_else", CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.bridgeCode, func(t *testing.T) {
			fake := &fakeBridgeRunner{response: bridgeResponse{Error: &bridgeError{Code: tc.bridgeCode, Message: "boom"}}}
			b := newJXAMailBackend(fake)
			_, err := b.Accounts(context.Background())
			if CodeOf(err) != tc.want {
				t.Fatalf("expected %v, got %v (%v)", tc.want, CodeOf(err), err)
			}
		})
	}
}

func TestJXAMailBackendVersionMismatchIsRejected(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{Version: 99}}
	b := newJXAMailBackend(fake)
	if _, err := b.Accounts(context.Background()); CodeOf(err) != CodeBridgeProtocolError {
		t.Fatalf("expected a protocol error for a version mismatch, got %v", err)
	}
}

func TestJXAMailBackendOpMismatchIsRejected(t *testing.T) {
	fake := &fakeBridgeRunner{response: bridgeResponse{Op: "mailboxes"}}
	b := newJXAMailBackend(fake)
	if _, err := b.Accounts(context.Background()); CodeOf(err) != CodeBridgeProtocolError {
		t.Fatalf("expected a protocol error for an op mismatch, got %v", err)
	}
}

func TestJXAMailBackendTrailingJSONIsRejected(t *testing.T) {
	fake := &fakeBridgeRunner{rawOverride: []byte(`{"version":1,"op":"accounts"}{"extra":true}`)}
	b := newJXAMailBackend(fake)
	if _, err := b.Accounts(context.Background()); CodeOf(err) != CodeBridgeProtocolError {
		t.Fatalf("expected a protocol error for trailing JSON, got %v", err)
	}
}

func TestJXAMailBackendMalformedOutputIsRejected(t *testing.T) {
	fake := &fakeBridgeRunner{rawOverride: []byte(`not json`)}
	b := newJXAMailBackend(fake)
	if _, err := b.Accounts(context.Background()); CodeOf(err) != CodeBridgeProtocolError {
		t.Fatalf("expected a protocol error for malformed output, got %v", err)
	}
}

func TestJXAMailBackendRunnerErrorsAreTranslated(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Code
	}{
		{"deadline", context.DeadlineExceeded, CodeAppUnavailable},
		{"oversized", errBridgeOutputTooLarge, CodeBridgeProtocolError},
		{"other", errors.New("boom"), CodeAppUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeBridgeRunner{err: tc.err}
			b := newJXAMailBackend(fake)
			_, err := b.Accounts(context.Background())
			if CodeOf(err) != tc.want {
				t.Fatalf("expected %v, got %v (%v)", tc.want, CodeOf(err), err)
			}
		})
	}
}

func TestJXAMailBackendCanceledIsPassedThrough(t *testing.T) {
	fake := &fakeBridgeRunner{err: context.Canceled}
	b := newJXAMailBackend(fake)
	_, err := b.Accounts(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled to pass through, got %v", err)
	}
}

// multiCallRunner returns a different response for each successive call,
// so tests can assert what each of several round trips actually sent.
type multiCallRunner struct {
	steps       []func(bridgeRequest) (bridgeResponse, error)
	call        int
	lastRequest bridgeRequest
}

func (m *multiCallRunner) run(_ context.Context, request []byte) ([]byte, error) {
	_ = json.Unmarshal(request, &m.lastRequest)
	if m.call >= len(m.steps) {
		return nil, errors.New("unexpected extra call")
	}
	resp, err := m.steps[m.call](m.lastRequest)
	m.call++
	if err != nil {
		return nil, err
	}
	if resp.Version == 0 {
		resp.Version = bridgeVersion
	}
	if resp.Op == "" {
		resp.Op = m.lastRequest.Op
	}
	return json.Marshal(resp)
}
