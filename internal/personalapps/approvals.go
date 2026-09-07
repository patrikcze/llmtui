package personalapps

import (
	"sync"
	"time"
)

// ApprovalLedger is a minimal in-process ApprovalChecker: a human approves
// one exact plan digest, and Service consumes that approval on the single
// change_apply call it authorizes.
//
// It is explicitly NOT the durable, cross-session operation ledger described
// for a later release (see the integration plan's Slice 5): it holds no
// history, does not survive a restart, is not keyed by a content-free
// resource/effect digest, and proves nothing about what actually executed
// once Service reports an outcome. It exists so change_apply has a real,
// working, exact-plan-bound gate now rather than either no gate or a
// bypassable one — the durable ledger replaces it without changing the
// ApprovalChecker contract Service depends on.
//
// An ApprovalLedger is safe for concurrent use.
type ApprovalLedger struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     Clock
	granted map[string]approvalEntry
}

type approvalEntry struct {
	digest string
	at     time.Time
}

// NewApprovalLedger returns an empty ledger. now defaults to time.Now.
// Entries older than the default plan TTL are pruned lazily, since a grant
// for an already-expired plan can never be consumed.
func NewApprovalLedger(now Clock) *ApprovalLedger {
	return &ApprovalLedger{ttl: defaultPlanTTL, now: now, granted: make(map[string]approvalEntry)}
}

// Approve records that a human approved planID for exactly this digest.
// Call it only from the code path that rendered that plan's full review to
// the person and received their explicit approval — never speculatively,
// and never for a digest the caller has not itself just displayed.
func (l *ApprovalLedger) Approve(planID, digest string) {
	if planID == "" || digest == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked()
	l.granted[planID] = approvalEntry{digest: digest, at: l.now.now()}
}

// Deny removes any recorded approval for planID, for example when the human
// declines the prompt or it is dismissed unanswered.
func (l *ApprovalLedger) Deny(planID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.granted, planID)
}

// ApprovedPlan implements ApprovalChecker: it reports approval only for the
// exact (planID, digest) pair a human approved, so an edited or re-prepared
// plan — which carries a different digest — is never treated as approved.
func (l *ApprovalLedger) ApprovedPlan(planID, digest string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked()
	entry, ok := l.granted[planID]
	return ok && entry.digest == digest
}

// Reset drops every recorded approval, for use when personal_apps access is
// disconnected or the session's scope is narrowed.
func (l *ApprovalLedger) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.granted = make(map[string]approvalEntry)
}

func (l *ApprovalLedger) pruneLocked() {
	now := l.now.now()
	for id, entry := range l.granted {
		if now.Sub(entry.at) >= l.ttl {
			delete(l.granted, id)
		}
	}
}
