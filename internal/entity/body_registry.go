package entity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ErrBodyCapacityExhausted is returned by Publish when every published body
// is currently pinned (an open BodyLease) and admitting the new body would
// require evicting one of them. Callers should surface a bounded retention
// warning rather than retry in a loop; existing pins are always left
// intact.
var ErrBodyCapacityExhausted = errors.New("entity body retention is at capacity")

// ErrNotAResourceBody is returned by OpenBody when id resolves to a
// Registry.Put-created semantic entity rather than a Registry.Publish-created
// body. Use errors.Is to detect this case.
var ErrNotAResourceBody = errors.New("entity is not a resource body")

// bodyRecord is Registry's internal bookkeeping for one published body. The
// bytes themselves live only in bodyBackend, addressed by handle.
type bodyRecord struct {
	view       ResourceView
	handle     string
	scope      Scope
	scopeID    string
	sizeBytes  int
	createdAt  time.Time
	lastUsedAt time.Time
	expiresAt  time.Time
}

// validResourceCandidate checks the Candidate fields Publish actually uses.
// Unlike validCandidate (Put's validator), it does not require
// Payload/Preview — Publish takes the body separately as []byte.
func validResourceCandidate(c Candidate) error {
	if c.Kind == "" {
		return errors.New("entity kind is required")
	}
	if strings.TrimSpace(c.Label) == "" {
		return errors.New("entity label is required")
	}
	if c.Trust == "" {
		return errors.New("entity trust is required")
	}
	if c.Scope == "" {
		return errors.New("entity scope is required")
	}
	return nil
}

// Publish stores body as a new, randomly-identified resource body and
// returns a view describing it. Publish does not itself decide truncation —
// the caller has already decided how many bytes to hand it (even if the
// original source exceeded some capture limit); Publish's job is only to
// store exactly those bytes and hand back an ID. Quota is reserved before
// any byte is copied into storage, so a rejected Publish never partially
// writes a body. ctx is honored for cancellation before and during the
// store; a canceled context after quota is reserved releases that
// reservation before returning.
func (r *Registry) Publish(ctx context.Context, candidate Candidate, body []byte) (ResourceView, error) {
	if r == nil {
		return ResourceView{}, fmt.Errorf("entity registry is unavailable")
	}
	if err := validResourceCandidate(candidate); err != nil {
		return ResourceView{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResourceView{}, fmt.Errorf("publish entity body: %w", err)
	}
	size := len(body)

	r.mu.Lock()
	now := r.limits.Now().UTC()
	r.purgeExpiredBodiesLocked(now)
	if size > r.limits.MaxBodyBytes {
		r.mu.Unlock()
		return ResourceView{}, fmt.Errorf("entity body of %d bytes exceeds the %d byte per-body limit", size, r.limits.MaxBodyBytes)
	}
	for r.bodyTotal+size > r.limits.MaxTotalBodyBytes {
		if !r.evictOneBodyLocked() {
			r.mu.Unlock()
			return ResourceView{}, ErrBodyCapacityExhausted
		}
	}
	r.bodyTotal += size
	gen := r.bodyGeneration
	r.mu.Unlock()

	id, err := newRandomID()
	if err != nil {
		r.releaseBodyReservation(gen, size)
		return ResourceView{}, fmt.Errorf("publish entity body: %w", err)
	}
	if err := ctx.Err(); err != nil {
		r.releaseBodyReservation(gen, size)
		return ResourceView{}, fmt.Errorf("publish entity body: %w", err)
	}
	handle, err := r.bodyBackend.put(body)
	if err != nil {
		r.releaseBodyReservation(gen, size)
		return ResourceView{}, fmt.Errorf("store entity body: %w", err)
	}

	view := ResourceView{
		ID:        id,
		Kind:      candidate.Kind,
		Label:     boundedLabel(candidate.Label),
		Source:    boundedLabel(candidate.Provenance.Source),
		Trust:     candidate.Trust,
		Scope:     candidate.Scope,
		SizeBytes: int64(size),
		Resource:  candidate.Resource,
		CreatedAt: now,
	}

	r.mu.Lock()
	if r.bodyGeneration != gen {
		// Reset ran while the backend write was in flight (see Reset's doc
		// comment on bodyGeneration): this publish's reservation is gone
		// along with the generation it belonged to, and inserting into the
		// new generation would resurrect a byte count that was never
		// reserved there. Discard this write instead.
		r.mu.Unlock()
		r.bodyBackend.remove(handle)
		return ResourceView{}, fmt.Errorf("publish entity body: entity registry was reset while publishing")
	}
	r.bodies[id] = &bodyRecord{
		view:       view,
		handle:     handle,
		scope:      candidate.Scope,
		scopeID:    candidate.ScopeID,
		sizeBytes:  size,
		createdAt:  now,
		lastUsedAt: now,
		expiresAt:  candidate.ExpiresAt,
	}
	r.mu.Unlock()
	return view, nil
}

// releaseBodyReservation undoes the bodyTotal += size reservation Publish
// makes before minting an ID/doing backend I/O, unless a Reset already
// zeroed the counter out from under it (detected via bodyGeneration).
func (r *Registry) releaseBodyReservation(gen, size int) {
	r.mu.Lock()
	if r.bodyGeneration == gen {
		r.bodyTotal -= size
	}
	r.mu.Unlock()
}

// OpenBody atomically validates id/scope/expiry, pins the body so
// deterministic eviction cannot remove it, and returns an immutable lease
// over its bytes. The registry mutex is held only for the validate+pin
// step; the backend's actual open I/O runs outside the lock. Close the
// returned BodyLease to unpin.
func (r *Registry) OpenBody(ctx context.Context, id ID) (ResourceView, BodyLease, error) {
	if r == nil {
		return ResourceView{}, nil, fmt.Errorf("entity registry is unavailable")
	}
	if !id.Valid() {
		return ResourceView{}, nil, fmt.Errorf("invalid entity ID %q", id)
	}
	if err := ctx.Err(); err != nil {
		return ResourceView{}, nil, fmt.Errorf("open entity body: %w", err)
	}

	r.mu.Lock()
	now := r.limits.Now().UTC()
	r.purgeExpiredBodiesLocked(now)
	rec, ok := r.bodies[id]
	if !ok {
		_, isSemantic := r.items[id]
		r.mu.Unlock()
		if isSemantic {
			return ResourceView{}, nil, fmt.Errorf("%w: %s", ErrNotAResourceBody, id)
		}
		return ResourceView{}, nil, fmt.Errorf("entity body %q is not present in this session", id)
	}
	if !rec.expiresAt.IsZero() && !rec.expiresAt.After(now) {
		r.mu.Unlock()
		return ResourceView{}, nil, fmt.Errorf("entity body %q lifetime has ended", id)
	}
	rec.lastUsedAt = now
	r.bodyPinned[id]++
	handle := rec.handle
	view := rec.view
	r.mu.Unlock()

	reader, size, err := r.bodyBackend.open(handle)
	if err != nil {
		r.releaseBodyPin(id)
		return ResourceView{}, nil, fmt.Errorf("open entity body: %w", err)
	}

	return view, &bodyLease{registry: r, id: id, reader: reader, size: size}, nil
}

func (r *Registry) releaseBodyPin(id ID) {
	r.mu.Lock()
	if r.bodyPinned[id] <= 1 {
		delete(r.bodyPinned, id)
	} else {
		r.bodyPinned[id]--
	}
	r.mu.Unlock()
}

// RetainBody reserves a published body against eviction while a controller
// operation (such as an approval prompt) refers to its file version.
func (r *Registry) RetainBody(id ID) bool {
	if r == nil || !id.Valid() {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.limits.Now().UTC()
	r.purgeExpiredBodiesLocked(now)
	if _, ok := r.bodies[id]; !ok {
		return false
	}
	r.bodyPinned[id]++
	return true
}

// ReleaseBody undoes RetainBody. It is idempotent when the body is absent.
func (r *Registry) ReleaseBody(id ID) {
	if r == nil {
		return
	}
	r.releaseBodyPin(id)
}

// bodyLease implements BodyLease. Close is idempotent via closeOnce so a
// caller's defer plus an explicit early-exit Close cannot double-release
// the pin.
type bodyLease struct {
	registry  *Registry
	id        ID
	reader    io.ReaderAt
	size      int64
	closeOnce sync.Once
}

func (l *bodyLease) ReadAt(p []byte, off int64) (int, error) {
	return l.reader.ReadAt(p, off)
}

func (l *bodyLease) Size() int64 { return l.size }

func (l *bodyLease) Close() error {
	l.closeOnce.Do(func() {
		l.registry.releaseBodyPin(l.id)
	})
	return nil
}

// purgeExpiredBodiesLocked removes expired, unpinned bodies. It mirrors
// purgeExpiredLocked's semantics for the existing item map.
func (r *Registry) purgeExpiredBodiesLocked(now time.Time) {
	for id, rec := range r.bodies {
		if r.bodyPinned[id] == 0 && !rec.expiresAt.IsZero() && !rec.expiresAt.After(now) {
			r.deleteBodyLocked(id)
		}
	}
}

// evictOneBodyLocked removes the least-recently-used unpinned body, if any,
// mirroring evictOneLocked's LRU-by-lastUsedAt policy for the existing item
// map. It never selects a pinned body (an open BodyLease).
func (r *Registry) evictOneBodyLocked() bool {
	var oldest ID
	var oldestTime time.Time
	for id, rec := range r.bodies {
		if r.bodyPinned[id] > 0 {
			continue
		}
		if oldest == "" || rec.lastUsedAt.Before(oldestTime) ||
			(rec.lastUsedAt.Equal(oldestTime) && id < oldest) {
			oldest = id
			oldestTime = rec.lastUsedAt
		}
	}
	if oldest == "" {
		return false
	}
	r.deleteBodyLocked(oldest)
	return true
}

func (r *Registry) deleteBodyLocked(id ID) {
	rec, ok := r.bodies[id]
	if !ok {
		return
	}
	delete(r.bodies, id)
	delete(r.bodyPinned, id)
	r.bodyTotal -= rec.sizeBytes
	r.bodyBackend.remove(rec.handle)
}
