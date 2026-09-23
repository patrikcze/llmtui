package entity

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Registry is a bounded in-memory entity store. It is safe for controller and
// tool-result goroutines to use concurrently.
type Registry struct {
	mu       sync.Mutex
	limits   Limits
	seq      int
	items    map[ID]*record
	pinned   map[ID]int
	total    int
	expanded int

	// Body storage (Registry.Publish/OpenBody). This is a parallel path
	// with its own ID scheme, its own byte budget, and its own storage; it
	// never merges into items/pinned/total above.
	bodyBackend bodyBackend
	bodies      map[ID]*bodyRecord
	bodyPinned  map[ID]int
	bodyTotal   int
	// bodyGeneration increments on every Reset. Registry.Publish captures
	// it before releasing the mutex to do backend I/O outside the lock, and
	// checks it again before inserting the finished record — if Reset ran
	// in between, the in-flight Publish discards its own write instead of
	// resurrecting a byte reservation into a registry generation that never
	// made it.
	bodyGeneration int
}

type record struct {
	view       View
	provenance Provenance
	scopeID    string
	payload    string
	createdAt  time.Time
	lastUsedAt time.Time
	expiresAt  time.Time
}

// NewRegistry constructs an empty registry with normalized bounds.
func NewRegistry(limits Limits) *Registry {
	return &Registry{
		limits:      limits.normalized(),
		items:       make(map[ID]*record),
		pinned:      make(map[ID]int),
		bodyBackend: newMemoryBodyBackend(),
		bodies:      make(map[ID]*bodyRecord),
		bodyPinned:  make(map[ID]int),
	}
}

// Put registers one candidate and returns its new opaque reference. Similar
// text is never merged: callers must provide a source identity if they need
// an application-level deduplication policy.
func (r *Registry) Put(candidate Candidate) (View, error) {
	if r == nil {
		return View{}, fmt.Errorf("entity registry is unavailable")
	}
	if err := validCandidate(candidate); err != nil {
		return View{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seq >= maxIDSequence {
		return View{}, fmt.Errorf("entity ID space is exhausted")
	}
	now := r.limits.Now().UTC()
	r.purgeExpiredLocked(now)
	payload := candidate.Payload
	if candidate.Redacted {
		payload = ""
	}
	boundedPayload, truncated := truncateBytes(payload, r.limits.MaxPayloadBytes)
	preview := candidate.Preview
	if preview == "" {
		preview = boundedPayload
	}
	preview, previewTruncated := truncateBytes(preview, r.limits.MaxPreviewBytes)
	truncated = truncated || previewTruncated

	for len(r.items) >= r.limits.MaxEntities || r.total+len(boundedPayload) > r.limits.MaxTotalPayload {
		if !r.evictOneLocked() {
			return View{}, fmt.Errorf("entity registry capacity is pinned")
		}
	}

	r.seq++
	id := ID(fmt.Sprintf("%s%0*d", IDPrefix, idDigits, r.seq))
	view := View{
		ID:        id,
		Kind:      candidate.Kind,
		Label:     boundedLabel(candidate.Label),
		Source:    boundedLabel(candidate.Provenance.Source),
		Metadata:  boundedMetadata(candidate.Metadata),
		Trust:     candidate.Trust,
		Scope:     candidate.Scope,
		Level:     LevelMinimal,
		Preview:   preview,
		Digest:    digest(payload),
		Truncated: truncated,
		Redacted:  candidate.Redacted,
	}
	r.items[id] = &record{
		view:       view,
		provenance: candidate.Provenance,
		scopeID:    candidate.ScopeID,
		payload:    boundedPayload,
		createdAt:  now,
		lastUsedAt: now,
		expiresAt:  candidate.ExpiresAt,
	}
	r.total += len(boundedPayload)
	return view, nil
}

// Resolve returns the requested representation of one entity.
func (r *Registry) Resolve(rawID string, level Level) Resolution {
	if r == nil {
		return Resolution{ID: rawID, Status: StatusUnavailable, Error: "entity registry is unavailable"}
	}
	id, err := ParseID(rawID)
	if err != nil {
		return Resolution{ID: rawID, Status: StatusInvalidID, Error: err.Error()}
	}
	if !validLevel(level) {
		return Resolution{ID: rawID, Status: StatusDetailNotAvailable, Error: fmt.Sprintf("unsupported entity detail level %q", level)}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok {
		return Resolution{ID: rawID, Status: StatusNotFound, Error: "entity is not present in this session"}
	}
	now := r.limits.Now().UTC()
	if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
		return Resolution{ID: rawID, Status: StatusExpired, Error: "entity lifetime has ended"}
	}
	item.lastUsedAt = now
	view := item.view
	view.Level = level
	if level == LevelIdentifier {
		view.Preview = ""
	}
	if level == LevelFull {
		if item.view.Redacted || item.payload == "" {
			return Resolution{ID: rawID, Status: StatusDetailNotAvailable, Error: "full entity detail is unavailable"}
		}
		if r.expanded >= r.limits.MaxFullExpansions {
			return Resolution{ID: rawID, Status: StatusDetailNotAvailable, Error: "full entity expansion budget is exhausted"}
		}
		r.expanded++
		view.Payload = item.payload
	}
	return Resolution{ID: rawID, Status: StatusOK, View: view}
}

// ResolveMany resolves a bounded batch in input order. Every ID is checked;
// one invalid or stale ID does not cause another ID's content to be guessed.
func (r *Registry) ResolveMany(ids []string, level Level, max int) []Resolution {
	if max > 0 && len(ids) > max {
		ids = ids[:max]
	}
	out := make([]Resolution, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.Resolve(id, level))
	}
	return out
}

// BeginRequest resets the per-request full expansion budget.
func (r *Registry) BeginRequest() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.expanded = 0
	r.mu.Unlock()
}

// Retain prevents deterministic eviction while a caller owns a pending
// operation referring to id. Release must be paired with Retain.
func (r *Registry) Retain(id ID) bool {
	if r == nil || !id.Valid() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return false
	}
	r.pinned[id]++
	return true
}

// Release drops one pending-operation retention.
func (r *Registry) Release(id ID) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pinned[id] <= 1 {
		delete(r.pinned, id)
		return
	}
	r.pinned[id]--
}

// ReleaseScope removes all entities and bodies owned by scopeID.
// Session-scoped records are unaffected. This is used when a bounded agent
// run ends. Exactly like the existing item release above, a pinned body
// (an open BodyLease) is left in place — ReleaseScope never evicts an
// active retention or an active lease out from under its owner.
func (r *Registry) ReleaseScope(scope Scope, scopeID string) {
	if r == nil || scopeID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, item := range r.items {
		if item.view.Scope == scope && item.scopeID == scopeID && r.pinned[id] == 0 {
			r.deleteLocked(id)
		}
	}
	for id, rec := range r.bodies {
		if rec.scope == scope && rec.scopeID == scopeID && r.bodyPinned[id] == 0 {
			r.deleteBodyLocked(id)
		}
	}
}

// Reset removes all entities and bodies while preserving the sequence, so
// old IDs can never resolve to a different object after a conversation
// reset. Reset is a hard wipe: exactly like the existing item map below (it
// has never checked pins), Reset removes body storage unconditionally, even
// for a body with a currently open BodyLease — a full session
// reset/reload must never leave stale body bytes reachable via a fresh
// OpenBody, and Reset is a session boundary, not routine eviction, so the
// "eviction cannot remove an open body" contract intentionally does not
// apply here. A BodyLease obtained before Reset keeps working: the memory
// backend's open() hands back a reader over an independent byte-slice copy
// (see body_memory.go), so already-open reads stay valid; only a new
// OpenBody call for that ID is affected, and it correctly reports the body
// as gone. bodyGeneration is bumped so an in-flight Publish that reserved
// quota before this Reset discards its own write instead of resurrecting a
// byte count into the new, empty generation.
func (r *Registry) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	for _, rec := range r.bodies {
		r.bodyBackend.remove(rec.handle)
	}
	r.items = make(map[ID]*record)
	r.pinned = make(map[ID]int)
	r.total = 0
	r.expanded = 0
	r.bodies = make(map[ID]*bodyRecord)
	r.bodyPinned = make(map[ID]int)
	r.bodyTotal = 0
	r.bodyGeneration++
	r.mu.Unlock()
}

// MinimalViews returns the most recently used live entities within a byte
// budget. Results are sorted by ID for stable prompt/cache output.
func (r *Registry) MinimalViews(maxBytes int) []View {
	if r == nil || maxBytes <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.limits.Now().UTC()
	ids := make([]ID, 0, len(r.items))
	for id, item := range r.items {
		if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := r.items[ids[i]], r.items[ids[j]]
		if !a.lastUsedAt.Equal(b.lastUsedAt) {
			return a.lastUsedAt.After(b.lastUsedAt)
		}
		return ids[i] < ids[j]
	})
	views := make([]View, 0, len(ids))
	used := 0
	for _, id := range ids {
		view := r.items[id].view
		cost := len(view.ID) + len(view.Kind) + len(view.Label) + len(view.Source) + len(view.Preview) + 64
		if len(views) > 0 && used+cost > maxBytes {
			continue
		}
		used += cost
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views
}

// Stats is a content-free snapshot suitable for diagnostics.
type Stats struct {
	Entities          int
	PayloadBytes      int
	MaxEntities       int
	MaxPayloadBytes   int
	MaxTotalPayload   int
	ExpandedThisReq   int
	MaxFullExpansions int
}

// Stats returns bounded registry counters without exposing payloads.
func (r *Registry) Stats() Stats {
	if r == nil {
		return Stats{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purgeExpiredLocked(r.limits.Now().UTC())
	return Stats{
		Entities:          len(r.items),
		PayloadBytes:      r.total,
		MaxEntities:       r.limits.MaxEntities,
		MaxPayloadBytes:   r.limits.MaxPayloadBytes,
		MaxTotalPayload:   r.limits.MaxTotalPayload,
		ExpandedThisReq:   r.expanded,
		MaxFullExpansions: r.limits.MaxFullExpansions,
	}
}

func (r *Registry) purgeExpiredLocked(now time.Time) {
	for id, item := range r.items {
		if r.pinned[id] == 0 && !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
			r.deleteLocked(id)
		}
	}
}

func validLevel(level Level) bool {
	return level == LevelIdentifier || level == LevelMinimal || level == LevelFull
}

func boundedLabel(value string) string {
	value, _ = truncateBytes(value, 256)
	return value
}

func (r *Registry) evictOneLocked() bool {
	var oldest ID
	var oldestTime time.Time
	for id, item := range r.items {
		if r.pinned[id] > 0 {
			continue
		}
		if oldest == "" || item.lastUsedAt.Before(oldestTime) ||
			(item.lastUsedAt.Equal(oldestTime) && id < oldest) {
			oldest = id
			oldestTime = item.lastUsedAt
		}
	}
	if oldest == "" {
		return false
	}
	r.deleteLocked(oldest)
	return true
}

func (r *Registry) deleteLocked(id ID) {
	item, ok := r.items[id]
	if !ok {
		return
	}
	delete(r.items, id)
	delete(r.pinned, id)
	r.total -= len(item.payload)
}
