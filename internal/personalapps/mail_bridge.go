package personalapps

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// bridgeVersion is the wire version this Go build and the embedded JXA
// script agree on. Bump both together when the schema changes; a mismatch
// is a hard error, never a best-effort fallback — see mail_darwin.go's
// embedded script and the architecture doc's "Mail JXA transport" section.
const bridgeVersion = 1

// errBridgeOutputTooLarge is returned by a bridgeRunner when the helper
// produced more output than the transport allows. It is a distinct sentinel
// so callers can tell "the script misbehaved" apart from an ordinary
// process failure.
var errBridgeOutputTooLarge = errors.New("mail bridge output exceeded the size limit")

// bridgeRunner executes one request/response round trip with the Mail
// bridge and returns its raw stdout. It exists so the protocol logic below
// can be tested without osascript or a real Mail.app: mail_darwin.go
// supplies the real subprocess-based implementation, tests inject a fake.
type bridgeRunner interface {
	run(ctx context.Context, request []byte) ([]byte, error)
}

// jxaMailBackend implements MailBackend by exchanging bounded JSON with the
// fixed Mail bridge script over a bridgeRunner. It holds no native resource
// itself and launches nothing directly; the runner owns the subprocess.
type jxaMailBackend struct {
	runner bridgeRunner
}

func newJXAMailBackend(runner bridgeRunner) *jxaMailBackend {
	return &jxaMailBackend{runner: runner}
}

// --- wire types --------------------------------------------------------
//
// These mirror the fixed JXA script's JSON exactly. Every field the script
// can omit is optional here; every value that reaches a model still passes
// through the domain conversions in backend.go's callers (scope re-check,
// handle minting), so a field added here does not by itself become visible
// to a model.

type bridgeRequest struct {
	Version    int                  `json:"version"`
	Op         string               `json:"op"`
	AccountID  string               `json:"account_id,omitempty"`
	ParentPath []string             `json:"parent_path,omitempty"`
	Search     *bridgeSearchRequest `json:"search,omitempty"`
	Messages   *bridgeReadRequest   `json:"messages,omitempty"`
}

type bridgeScope struct {
	AccountID string   `json:"account_id"`
	Path      []string `json:"path"`
}

type bridgeSearchRequest struct {
	Scopes         []bridgeScope `json:"scopes"`
	ReceivedAfter  string        `json:"received_after,omitempty"`
	ReceivedBefore string        `json:"received_before,omitempty"`
	From           string        `json:"from,omitempty"`
	Subject        string        `json:"subject,omitempty"`
	Unread         *bool         `json:"unread,omitempty"`
	Flagged        *bool         `json:"flagged,omitempty"`
	Newest         bool          `json:"newest"`
	Limit          int           `json:"limit"`
	MaxCandidates  int           `json:"max_candidates"`
	Resume         *bridgeResume `json:"resume,omitempty"`
}

// bridgeResume positions a continued scan. It is meaningful only paired
// with the fingerprint encodeSearchCursor binds it to — see that function's
// doc comment for why the cursor is not just this struct on its own.
type bridgeResume struct {
	ScopeIndex   int `json:"scope_index"`
	MessageIndex int `json:"message_index"`
}

type bridgeMessageRef struct {
	AccountID string   `json:"account_id"`
	Path      []string `json:"path"`
	NativeID  string   `json:"native_id"`
}

type bridgeReadRequest struct {
	Refs         []bridgeMessageRef `json:"refs"`
	MaxBodyChars int                `json:"max_body_chars"`
}

type bridgeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type bridgeAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type bridgeMailbox struct {
	Name        string `json:"name"`
	PathSegment string `json:"path_segment"`
	HasChildren bool   `json:"has_children"`
	Unread      int    `json:"unread"`
	Total       int    `json:"total"`
}

type bridgeMessage struct {
	AccountID      string   `json:"account_id"`
	Path           []string `json:"path"`
	NativeID       string   `json:"native_id"`
	MessageID      string   `json:"message_id,omitempty"`
	Subject        string   `json:"subject"`
	From           string   `json:"from"`
	To             []string `json:"to,omitempty"`
	Cc             []string `json:"cc,omitempty"`
	Received       string   `json:"received"`
	Unread         bool     `json:"unread"`
	Flagged        bool     `json:"flagged"`
	HasAttachments bool     `json:"has_attachments"`

	Body          string             `json:"body,omitempty"`
	BodyAvailable bool               `json:"body_available"`
	BodyTruncated bool               `json:"body_truncated"`
	Unavailable   string             `json:"unavailable,omitempty"`
	Attachments   []bridgeAttachment `json:"attachments,omitempty"`
}

type bridgeAttachment struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type bridgePage struct {
	Messages []bridgeMessage `json:"messages"`
	Scanned  int             `json:"scanned"`
	Complete bool            `json:"complete"`
	Reason   string          `json:"reason,omitempty"`
	Resume   *bridgeResume   `json:"resume,omitempty"`
}

type bridgeResponse struct {
	Version   int             `json:"version"`
	Op        string          `json:"op"`
	Error     *bridgeError    `json:"error,omitempty"`
	Accounts  []bridgeAccount `json:"accounts,omitempty"`
	Mailboxes []bridgeMailbox `json:"mailboxes,omitempty"`
	Page      *bridgePage     `json:"page,omitempty"`
	Messages  []bridgeMessage `json:"messages,omitempty"`
}

// --- MailBackend -------------------------------------------------------

func (b *jxaMailBackend) Accounts(ctx context.Context) ([]BackendAccount, error) {
	resp, err := b.call(ctx, bridgeRequest{Op: "accounts"})
	if err != nil {
		return nil, err
	}
	out := make([]BackendAccount, 0, len(resp.Accounts))
	for _, a := range resp.Accounts {
		if a.ID == "" {
			continue
		}
		out = append(out, BackendAccount{
			Ref:         ResourceRef{Kind: KindAccount, Adapter: AdapterMail, AccountID: a.ID},
			DisplayName: a.Name,
		})
	}
	return out, nil
}

func (b *jxaMailBackend) Mailboxes(ctx context.Context, account, parent ResourceRef) ([]BackendMailbox, error) {
	if account.AccountID == "" {
		return nil, Errorf(CodeInvalidRequest, "mailboxes requires an account")
	}
	req := bridgeRequest{Op: "mailboxes", AccountID: account.AccountID}
	if parent.Kind == KindMailbox {
		req.ParentPath = parent.ContainerPath
	}
	resp, err := b.call(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make([]BackendMailbox, 0, len(resp.Mailboxes))
	for _, mb := range resp.Mailboxes {
		if mb.PathSegment == "" {
			continue
		}
		path := append(append([]string(nil), parent.ContainerPath...), mb.PathSegment)
		out = append(out, BackendMailbox{
			Ref:         ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: account.AccountID, ContainerPath: path},
			DisplayName: mb.Name,
			HasChildren: mb.HasChildren,
			UnreadCount: mb.Unread,
			TotalCount:  mb.Total,
		})
	}
	return out, nil
}

// resolveInbox finds the top-level mailbox named "Inbox" (case-insensitive,
// matching every provider observed during the Slice 0 spike) so a search
// scoped to a whole account has somewhere bounded to run. Mail exposes no
// account-wide message collection, and scanning every mailbox of an account
// with no explicit target is exactly the unbounded cost the architecture
// warns against — so account-only search deliberately narrows to Inbox
// rather than guessing wider. Callers that want other mailboxes must select
// them explicitly via mail_mailboxes -> mailbox_ids.
func (b *jxaMailBackend) resolveInbox(ctx context.Context, account ResourceRef) (ResourceRef, error) {
	boxes, err := b.Mailboxes(ctx, account, ResourceRef{})
	if err != nil {
		return ResourceRef{}, err
	}
	for _, box := range boxes {
		if strings.EqualFold(box.DisplayName, "inbox") {
			return box.Ref, nil
		}
	}
	return ResourceRef{}, Errorf(CodeAppUnavailable, "account has no top-level Inbox mailbox to search")
}

func (b *jxaMailBackend) Search(ctx context.Context, query MailQuery) (BackendMailPage, error) {
	scopes := make([]ResourceRef, 0, len(query.Mailboxes)+len(query.Accounts))
	scopes = append(scopes, query.Mailboxes...)
	for _, acct := range query.Accounts {
		inbox, err := b.resolveInbox(ctx, acct)
		if err != nil {
			return BackendMailPage{}, err
		}
		scopes = append(scopes, inbox)
	}
	if len(scopes) == 0 {
		return BackendMailPage{}, Errorf(CodeInvalidRequest, "search requires at least one resolvable mailbox")
	}

	req := bridgeSearchRequest{
		Newest:        query.Newest,
		Limit:         query.Limit,
		MaxCandidates: query.MaxCandidates,
		From:          query.From,
		Subject:       query.Subject,
		Unread:        query.Unread,
		Flagged:       query.Flagged,
	}
	for _, s := range scopes {
		req.Scopes = append(req.Scopes, bridgeScope{AccountID: s.AccountID, Path: append([]string(nil), s.ContainerPath...)})
	}
	if !query.ReceivedAfter.IsZero() {
		req.ReceivedAfter = query.ReceivedAfter.UTC().Format(time.RFC3339)
	}
	if !query.ReceivedBefore.IsZero() {
		req.ReceivedBefore = query.ReceivedBefore.UTC().Format(time.RFC3339)
	}

	fp := searchFingerprint(req)
	if query.Cursor != "" {
		// A cursor that fails to decode or belongs to a different query is
		// not an error: the scan restarts from the beginning and reports
		// honest best-effort coverage, per architecture ("offer restart
		// rather than claiming an exact snapshot").
		if resume, ok := decodeSearchCursor(query.Cursor, fp); ok {
			req.Resume = resume
		}
	}

	resp, err := b.call(ctx, bridgeRequest{Op: "search", Search: &req})
	if err != nil {
		return BackendMailPage{}, err
	}
	if resp.Page == nil {
		return BackendMailPage{}, Errorf(CodeBridgeProtocolError, "mail bridge search returned no page")
	}
	page := BackendMailPage{
		Scanned:  resp.Page.Scanned,
		Complete: resp.Page.Complete,
		Reason:   resp.Page.Reason,
	}
	for _, m := range resp.Page.Messages {
		bm, err := decodeBridgeMessage(m)
		if err != nil {
			return BackendMailPage{}, err
		}
		page.Messages = append(page.Messages, bm)
	}
	if resp.Page.Resume != nil {
		page.NextCursor = encodeSearchCursor(*resp.Page.Resume, fp)
	}
	return page, nil
}

func (b *jxaMailBackend) Messages(ctx context.Context, refs []ResourceRef, maxBodyBytes int) ([]BackendMessage, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	req := bridgeReadRequest{MaxBodyChars: maxBodyBytes}
	for _, ref := range refs {
		req.Refs = append(req.Refs, bridgeMessageRef{
			AccountID: ref.AccountID,
			Path:      append([]string(nil), ref.ContainerPath...),
			NativeID:  ref.NativeID,
		})
	}
	resp, err := b.call(ctx, bridgeRequest{Op: "messages", Messages: &req})
	if err != nil {
		return nil, err
	}
	out := make([]BackendMessage, 0, len(resp.Messages))
	for _, m := range resp.Messages {
		bm, err := decodeBridgeMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, bm)
	}
	return out, nil
}

func decodeBridgeMessage(m bridgeMessage) (BackendMessage, error) {
	received, err := time.Parse(time.RFC3339, m.Received)
	if err != nil {
		return BackendMessage{}, Errorf(CodeBridgeProtocolError, "mail bridge returned an unparsable received time")
	}
	path := append([]string(nil), m.Path...)
	out := BackendMessage{
		Ref: ResourceRef{
			Kind: KindMessage, Adapter: AdapterMail,
			AccountID: m.AccountID, ContainerPath: path,
			NativeID: m.NativeID, ExternalID: m.MessageID,
		},
		MailboxRef: ResourceRef{
			Kind: KindMailbox, Adapter: AdapterMail,
			AccountID: m.AccountID, ContainerPath: append([]string(nil), path...),
		},
		Subject:        m.Subject,
		From:           m.From,
		To:             append([]string(nil), m.To...),
		Cc:             append([]string(nil), m.Cc...),
		Received:       received,
		Unread:         m.Unread,
		Flagged:        m.Flagged,
		HasAttachments: m.HasAttachments,
		Body:           m.Body,
		BodyAvailable:  m.BodyAvailable,
		BodyTruncated:  m.BodyTruncated,
		Unavailable:    m.Unavailable,
	}
	for _, a := range m.Attachments {
		out.AttachmentInfo = append(out.AttachmentInfo, BackendAttachment{Name: a.Name, Size: a.Size})
	}
	return out, nil
}

// searchFingerprint digests everything about a search that must match
// before a resume cursor is honored: the exact scopes and filters. It
// deliberately excludes Resume itself.
func searchFingerprint(req bridgeSearchRequest) string {
	parts := make([]string, 0, len(req.Scopes)*2+8)
	for _, s := range req.Scopes {
		parts = append(parts, s.AccountID, strings.Join(s.Path, "/"))
	}
	parts = append(parts,
		req.ReceivedAfter, req.ReceivedBefore, req.From, req.Subject,
		fmt.Sprintf("newest=%t", req.Newest), fmt.Sprintf("limit=%d", req.Limit),
	)
	if req.Unread != nil {
		parts = append(parts, fmt.Sprintf("unread=%t", *req.Unread))
	}
	if req.Flagged != nil {
		parts = append(parts, fmt.Sprintf("flagged=%t", *req.Flagged))
	}
	return Fingerprint(parts...)
}

// encodeSearchCursor binds a resume position to the query fingerprint that
// produced it. The cursor is opaque to the model (base64 of a small JSON
// blob), but it is never trusted blind: decodeSearchCursor only honors it
// when the fingerprint still matches the query it is paired with on the
// next call, so a cursor cannot be replayed against a different search.
func encodeSearchCursor(r bridgeResume, fp string) string {
	raw, err := json.Marshal(struct {
		FP string `json:"fp"`
		bridgeResume
	}{FP: fp, bridgeResume: r})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeSearchCursor(cursor, fp string) (*bridgeResume, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, false
	}
	var payload struct {
		FP string `json:"fp"`
		bridgeResume
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if payload.FP != fp {
		return nil, false
	}
	r := payload.bridgeResume
	return &r, true
}

// call sends one request and validates the response envelope before
// returning it: version and op must match, and the response must be
// exactly one complete JSON value — a second trailing value is treated the
// same as malformed output, never silently ignored.
func (b *jxaMailBackend) call(ctx context.Context, req bridgeRequest) (bridgeResponse, error) {
	req.Version = bridgeVersion
	payload, err := json.Marshal(req)
	if err != nil {
		return bridgeResponse{}, Errorf(CodeInternal, "encode mail bridge request: %v", err)
	}
	out, err := b.runner.run(ctx, payload)
	if err != nil {
		return bridgeResponse{}, translateRunnerError(err)
	}
	var resp bridgeResponse
	dec := json.NewDecoder(bytes.NewReader(out))
	if err := dec.Decode(&resp); err != nil {
		return bridgeResponse{}, wrap(&Error{Code: CodeBridgeProtocolError, Message: "mail bridge returned malformed output"}, err)
	}
	if dec.More() {
		return bridgeResponse{}, Errorf(CodeBridgeProtocolError, "mail bridge returned more than one JSON value")
	}
	if resp.Version != bridgeVersion {
		return bridgeResponse{}, Errorf(CodeBridgeProtocolError, "mail bridge version %d does not match expected %d", resp.Version, bridgeVersion)
	}
	if resp.Op != req.Op {
		return bridgeResponse{}, Errorf(CodeBridgeProtocolError, "mail bridge answered op %q for request %q", resp.Op, req.Op)
	}
	if resp.Error != nil {
		return bridgeResponse{}, bridgeErrorToDomain(*resp.Error)
	}
	return resp, nil
}

func bridgeErrorToDomain(e bridgeError) error {
	msg := clip(e.Message, 200)
	switch e.Code {
	case "permission_denied":
		return Errorf(CodePermissionDenied, "%s", msg)
	case "app_unavailable":
		return Errorf(CodeAppUnavailable, "%s", msg)
	case "not_found":
		return Errorf(CodeStaleReference, "%s", msg)
	case "invalid_request":
		return Errorf(CodeInvalidRequest, "%s", msg)
	default:
		return Errorf(CodeInternal, "%s", msg)
	}
}

func translateRunnerError(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return wrap(&Error{Code: CodeAppUnavailable, Message: "Mail did not respond in time"}, err)
	case errors.Is(err, context.Canceled):
		return err
	case errors.Is(err, errBridgeOutputTooLarge):
		return Errorf(CodeBridgeProtocolError, "mail bridge produced more output than allowed")
	default:
		return wrap(&Error{Code: CodeAppUnavailable, Message: "could not reach Mail"}, err)
	}
}
