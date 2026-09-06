package personalapps

import (
	"sort"
	"strings"
)

// Scope is the user's configured authorization. It is set through human CLI
// and TUI commands only: no model argument reaches it, and no operation can
// widen it.
//
// An empty allowlist authorizes nothing. That is the whole point of the
// type: a feature that reads a person's mail must fail closed when it was
// never told which mail it may read. Enumerating labels during an explicit
// connect is how the lists get filled, and enumeration is not a grant.
//
// AllowedMailboxes and AllowedCalendars narrow further inside what is
// already allowed: with AllowedAccounts set and AllowedMailboxes empty,
// every mailbox of those accounts is in scope, and a non-empty
// AllowedMailboxes restricts to those subtrees. AllowedAccounts empty means
// no mail at all, whatever AllowedMailboxes says.
type Scope struct {
	// MailEnabled and CalendarEnabled mirror configuration. A disabled
	// adapter is not reachable even when connected.
	MailEnabled     bool
	CalendarEnabled bool
	// AllowedAccounts holds native mail account identifiers.
	AllowedAccounts []string
	// AllowedMailboxes holds "account\x1fpath/segments" prefixes.
	AllowedMailboxes []MailboxScope
	// AllowedCalendars holds native calendar identifiers.
	AllowedCalendars []string
	// MutationsEnabled allows any change to be applied. It is off by
	// default, and a read grant never implies it.
	MutationsEnabled bool
}

// MailboxScope names one allowed mailbox subtree.
type MailboxScope struct {
	AccountID string
	// Path is the mailbox hierarchy from the account root. An entry matches
	// that mailbox and everything under it.
	Path []string
}

// ConnectionState records which adapters the human explicitly connected.
// Configuration alone never sets it: enabling a feature must not launch an
// app or request a permission.
type ConnectionState struct {
	MailConnected     bool
	CalendarConnected bool
	// PrivateSession reports that personal content has already entered this
	// session, so the persistence and egress restrictions are in force.
	PrivateSession bool
}

// AllowsAccount reports whether a native mail account is in scope.
func (s Scope) AllowsAccount(nativeID string) bool {
	if !s.MailEnabled || nativeID == "" {
		return false
	}
	for _, allowed := range s.AllowedAccounts {
		if allowed == nativeID {
			return true
		}
	}
	return false
}

// AllowsCalendar reports whether a native calendar is in scope.
func (s Scope) AllowsCalendar(nativeID string) bool {
	if !s.CalendarEnabled || nativeID == "" {
		return false
	}
	for _, allowed := range s.AllowedCalendars {
		if allowed == nativeID {
			return true
		}
	}
	return false
}

// AllowsMailbox reports whether a mailbox path inside an allowed account is
// in scope. Comparison is on the resolved path segments, never on a display
// string, so a mailbox named to look like another one does not match it.
func (s Scope) AllowsMailbox(accountID string, path []string) bool {
	if !s.AllowsAccount(accountID) {
		return false
	}
	if len(s.AllowedMailboxes) == 0 {
		return true
	}
	for _, allowed := range s.AllowedMailboxes {
		if allowed.AccountID != accountID {
			continue
		}
		if hasPathPrefix(path, allowed.Path) {
			return true
		}
	}
	return false
}

// AllowsRef reports whether one resolved resource is in scope.
func (s Scope) AllowsRef(ref ResourceRef) bool {
	switch ref.Kind {
	case KindAccount:
		return s.AllowsAccount(ref.AccountID)
	case KindMailbox:
		return s.AllowsMailbox(ref.AccountID, ref.ContainerPath)
	case KindMessage:
		return s.AllowsMailbox(ref.AccountID, ref.ContainerPath)
	case KindCalendar:
		return s.AllowsCalendar(ref.NativeID)
	case KindEvent:
		return s.AllowsCalendar(ref.AccountID)
	default:
		return false
	}
}

// CheckRef returns ErrScopeDenied for a resource outside the scope. The
// error names no account, mailbox or calendar: a denial must not become a
// way to enumerate what exists.
func (s Scope) CheckRef(ref ResourceRef) error {
	if s.AllowsRef(ref) {
		return nil
	}
	return ErrScopeDenied
}

// AdapterEnabled reports whether an adapter is configured on.
func (s Scope) AdapterEnabled(a Adapter) bool {
	switch a {
	case AdapterMail:
		return s.MailEnabled
	case AdapterCalendar:
		return s.CalendarEnabled
	default:
		return true
	}
}

// Connected reports whether an adapter was explicitly connected.
func (c ConnectionState) Connected(a Adapter) bool {
	switch a {
	case AdapterMail:
		return c.MailConnected
	case AdapterCalendar:
		return c.CalendarConnected
	default:
		return true
	}
}

// CheckOperation decides whether an operation may run at all, before any
// argument is resolved and before any adapter is contacted.
func (s Scope) CheckOperation(op Operation, conn ConnectionState) error {
	if !op.Valid() {
		return Errorf(CodeInvalidRequest, "unknown operation %q", clip(string(op), 40))
	}
	if op == OpStatus {
		return nil
	}
	if op == OpChangeApply || op == OpChangePrepare {
		if !s.MutationsEnabled {
			return ErrMutationsDisabled
		}
		// The adapters a plan touches are checked per change; a plan is not
		// tied to one adapter.
		if !s.MailEnabled && !s.CalendarEnabled {
			return ErrDisabled
		}
		return nil
	}
	adapter := op.Adapter()
	if adapter == AdapterNone {
		// open_item resolves its adapter from the item it names.
		return nil
	}
	if !s.AdapterEnabled(adapter) {
		return ErrDisabled
	}
	if !conn.Connected(adapter) {
		return ErrNotConnected
	}
	return nil
}

// CheckChange decides whether one prepared change is permitted: mutations
// must be on, the owning adapter enabled and connected, and the change type
// supported in this release.
func (s Scope) CheckChange(c Change, conn ConnectionState) error {
	if !s.MutationsEnabled {
		return ErrMutationsDisabled
	}
	adapter := c.Adapter()
	if adapter == AdapterNone {
		return Errorf(CodeUnsupportedOperation, "change type %q has no adapter", clip(string(c.Type), 40))
	}
	if !s.AdapterEnabled(adapter) {
		return ErrDisabled
	}
	if !conn.Connected(adapter) {
		return ErrNotConnected
	}
	return nil
}

// AllowedOperations lists exactly what may be called right now, in the
// vocabulary's stable order. The model-visible tool schema is built from
// this, so a disabled or disconnected adapter's operations are absent rather
// than present and failing.
func (s Scope) AllowedOperations(conn ConnectionState, platformSupported bool) []Operation {
	if !platformSupported {
		return []Operation{OpStatus}
	}
	out := make([]Operation, 0, len(operations))
	for _, op := range Operations() {
		if err := s.CheckOperation(op, conn); err == nil {
			out = append(out, op)
		}
	}
	return out
}

// AllowedChangeTypes lists the change variants that may currently be
// prepared.
func (s Scope) AllowedChangeTypes(conn ConnectionState) []ChangeType {
	if !s.MutationsEnabled {
		return nil
	}
	out := make([]ChangeType, 0, len(changeTypes))
	for _, t := range ChangeTypes() {
		if err := s.CheckChange(Change{Type: t}, conn); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// Normalize sorts and deduplicates the allowlists so scope comparisons and
// diagnostics are stable. It never adds an entry.
func (s Scope) Normalize() Scope {
	out := s
	out.AllowedAccounts = normalizeIDs(s.AllowedAccounts)
	out.AllowedCalendars = normalizeIDs(s.AllowedCalendars)
	if len(s.AllowedMailboxes) > 0 {
		boxes := make([]MailboxScope, 0, len(s.AllowedMailboxes))
		for _, b := range s.AllowedMailboxes {
			if strings.TrimSpace(b.AccountID) == "" || len(b.Path) == 0 {
				continue
			}
			copied := MailboxScope{AccountID: b.AccountID, Path: append([]string(nil), b.Path...)}
			boxes = append(boxes, copied)
		}
		sort.Slice(boxes, func(i, j int) bool {
			if boxes[i].AccountID != boxes[j].AccountID {
				return boxes[i].AccountID < boxes[j].AccountID
			}
			return strings.Join(boxes[i].Path, "\x1f") < strings.Join(boxes[j].Path, "\x1f")
		})
		out.AllowedMailboxes = boxes
	}
	return out
}

func normalizeIDs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, id := range in {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	sort.Strings(out)
	return dedupeStrings(out)
}

func hasPathPrefix(path, prefix []string) bool {
	if len(prefix) > len(path) {
		return false
	}
	for i, segment := range prefix {
		if path[i] != segment {
			return false
		}
	}
	return true
}
