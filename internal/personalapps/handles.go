package personalapps

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// HandleKind is the resource class a Handle points at. It is the only part
// of a handle a model can read, and it exists so a wrong-kind argument is
// rejected before any adapter call.
type HandleKind string

const (
	KindAccount  HandleKind = "acct"
	KindMailbox  HandleKind = "box"
	KindMessage  HandleKind = "msg"
	KindCalendar HandleKind = "cal"
	KindEvent    HandleKind = "evt"
)

var handleKinds = []HandleKind{KindAccount, KindMailbox, KindMessage, KindCalendar, KindEvent}

// Valid reports whether k is a known resource class.
func (k HandleKind) Valid() bool {
	for _, known := range handleKinds {
		if k == known {
			return true
		}
	}
	return false
}

// handleRandomBytes is the unguessable part of a handle. Handles are minted
// randomly rather than derived from the resource so that a handle carries no
// account name, folder path, subject or event title.
const handleRandomBytes = 12

// Handle is an opaque, host-issued reference to one personal-app resource.
// It is meaningful only to the Registry that minted it, only for the life of
// that connection, and it is never a native identifier the model could have
// guessed or constructed.
type Handle string

// Kind returns the resource class encoded in the handle prefix, or "" when
// the handle is not well formed.
func (h Handle) Kind() HandleKind {
	prefix, rest, ok := strings.Cut(string(h), "_")
	if !ok || rest == "" {
		return ""
	}
	kind := HandleKind(prefix)
	if !kind.Valid() {
		return ""
	}
	return kind
}

// WellFormed reports whether h has a known kind prefix and a plausible
// random suffix. It says nothing about whether the handle was ever issued.
func (h Handle) WellFormed() bool {
	prefix, rest, ok := strings.Cut(string(h), "_")
	if !ok {
		return false
	}
	if !HandleKind(prefix).Valid() {
		return false
	}
	if len(rest) != handleRandomBytes*2 {
		return false
	}
	if _, err := hex.DecodeString(rest); err != nil {
		return false
	}
	return true
}

func (h Handle) String() string { return string(h) }

// ResourceRef is the host-side resolution of a Handle. It stays inside the
// process: it is never serialized to the model, never written to the
// operation ledger in clear text, and never rendered in an error message.
//
// No single field is a durable identity on its own. Mail accounts and
// mailboxes have no stable identifier in the installed scripting dictionary,
// an RFC Message-ID may be missing or duplicated across mailbox copies, and
// EventKit identifiers can change after a full synchronization or a move
// between calendars. Resolution therefore always combines scope, native
// identifier and Fingerprint, and an ambiguous match is an error rather than
// a first-match guess.
type ResourceRef struct {
	// Kind is the resource class; it must match the Handle prefix.
	Kind HandleKind
	// Adapter is the owning application.
	Adapter Adapter
	// AccountID is the native account identifier, or the explicit
	// local-store scope for account-less local mailboxes. Never a display
	// name, which is not unique.
	AccountID string
	// ContainerPath is the mailbox hierarchy from the account root down,
	// used to re-resolve a mailbox that has no stable identifier.
	ContainerPath []string
	// NativeID is the app-side identifier: the Mail message id, the
	// EventKit calendar identifier or the EventKit event identifier.
	NativeID string
	// ExternalID is a secondary identifier that may be absent or
	// duplicated: an RFC Message-ID, or a calendar item UID. It is never
	// the sole selector for a mutation.
	ExternalID string
	// Occurrence is the start instant of one occurrence of a recurring
	// event. It is zero for anything that is not an occurrence.
	Occurrence time.Time
	// Fingerprint is the observed-state digest recorded when the handle was
	// minted. A mutation compares it against a fresh read.
	Fingerprint string
}

// key is the deduplication key for a ref: two mints of the same observed
// resource reuse one handle so a repeated search does not grow the registry.
// Fingerprint is part of the key, so a changed observation mints a new
// handle instead of silently re-pointing an old one.
func (r ResourceRef) key() string {
	var b strings.Builder
	fields := []string{
		string(r.Kind),
		string(r.Adapter),
		r.AccountID,
		strings.Join(r.ContainerPath, "\x1f"),
		r.NativeID,
		r.ExternalID,
		r.Fingerprint,
	}
	for _, f := range fields {
		b.WriteString(f)
		b.WriteByte(0x1e)
	}
	if !r.Occurrence.IsZero() {
		b.WriteString(r.Occurrence.UTC().Format(time.RFC3339Nano))
	}
	return b.String()
}

// clone returns a copy that shares no slice with the original, so a caller
// cannot mutate a stored ref through the slice it handed in or got back.
func (r ResourceRef) clone() ResourceRef {
	out := r
	if len(r.ContainerPath) > 0 {
		out.ContainerPath = append([]string(nil), r.ContainerPath...)
	}
	return out
}

// Fingerprint digests observed resource state into a short, content-free
// version string. Callers pass the exact fields a mutation depends on, for
// example the current mailbox path, unread flag and modification date.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0x1e})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// RegistryOptions bounds a Registry. Zero values take package defaults.
type RegistryOptions struct {
	// Max is the maximum number of live handles. The least recently used
	// entry is evicted beyond it.
	Max int
	// TTL is how long a handle stays resolvable after its last use.
	TTL time.Duration
	// Now supplies the current time.
	Now Clock
}

const (
	defaultRegistryMax = 2000
	defaultRegistryTTL = 30 * time.Minute
)

type registryEntry struct {
	ref  ResourceRef
	used time.Time
}

// Registry mints and resolves opaque handles for one connection. It is
// bounded in both count and age: handles do not survive a disconnect, and a
// model cannot accumulate an unbounded private index of a mailbox by
// searching repeatedly.
//
// A Registry is safe for concurrent use.
type Registry struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     Clock
	entries map[Handle]*registryEntry
	byRef   map[string]Handle
}

// NewRegistry returns an empty Registry. It performs no I/O.
func NewRegistry(opts RegistryOptions) *Registry {
	max := opts.Max
	if max <= 0 {
		max = defaultRegistryMax
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultRegistryTTL
	}
	return &Registry{
		max:     max,
		ttl:     ttl,
		now:     opts.Now,
		entries: make(map[Handle]*registryEntry),
		byRef:   make(map[string]Handle),
	}
}

// Mint returns the handle for ref, reusing an existing one for an identical
// observation. It rejects a ref whose Kind is unknown.
func (r *Registry) Mint(ref ResourceRef) (Handle, error) {
	if !ref.Kind.Valid() {
		return "", Errorf(CodeInternal, "unknown resource kind %q", string(ref.Kind))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now.now()
	r.expireLocked(now)

	key := ref.key()
	if h, ok := r.byRef[key]; ok {
		r.entries[h].used = now
		return h, nil
	}

	h, err := newHandle(ref.Kind)
	if err != nil {
		return "", err
	}
	r.entries[h] = &registryEntry{ref: ref.clone(), used: now}
	r.byRef[key] = h
	r.evictLocked()
	return h, nil
}

// Resolve returns the resource a handle refers to. An unknown, expired or
// malformed handle is ErrUnknownHandle; resolving refreshes the entry's age.
func (r *Registry) Resolve(h Handle) (ResourceRef, error) {
	if !h.WellFormed() {
		return ResourceRef{}, ErrUnknownHandle
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now.now()
	r.expireLocked(now)

	entry, ok := r.entries[h]
	if !ok {
		return ResourceRef{}, ErrUnknownHandle
	}
	entry.used = now
	return entry.ref.clone(), nil
}

// ResolveKind resolves h and additionally requires the expected class, so a
// mailbox handle can never be used where a message handle is required.
func (r *Registry) ResolveKind(h Handle, kind HandleKind) (ResourceRef, error) {
	if h.Kind() != kind {
		return ResourceRef{}, ErrUnknownHandle
	}
	return r.Resolve(h)
}

// Invalidate drops one handle, for example after a move made its recorded
// location wrong.
func (r *Registry) Invalidate(h Handle) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dropLocked(h)
}

// Reset drops every handle. Disconnecting an adapter must call it so no
// previously issued reference stays usable.
func (r *Registry) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = make(map[Handle]*registryEntry)
	r.byRef = make(map[string]Handle)
}

// Len reports how many handles are currently live.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expireLocked(r.now.now())
	return len(r.entries)
}

func (r *Registry) dropLocked(h Handle) {
	entry, ok := r.entries[h]
	if !ok {
		return
	}
	delete(r.entries, h)
	delete(r.byRef, entry.ref.key())
}

func (r *Registry) expireLocked(now time.Time) {
	for h, entry := range r.entries {
		if now.Sub(entry.used) >= r.ttl {
			delete(r.entries, h)
			delete(r.byRef, entry.ref.key())
		}
	}
}

func (r *Registry) evictLocked() {
	for len(r.entries) > r.max {
		var oldest Handle
		var oldestAt time.Time
		for h, entry := range r.entries {
			if oldest == "" || entry.used.Before(oldestAt) {
				oldest, oldestAt = h, entry.used
			}
		}
		r.dropLocked(oldest)
	}
}

func newHandle(kind HandleKind) (Handle, error) {
	buf := make([]byte, handleRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", Errorf(CodeInternal, "generate handle: %v", err)
	}
	return Handle(string(kind) + "_" + hex.EncodeToString(buf)), nil
}
