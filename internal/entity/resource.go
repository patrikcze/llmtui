package entity

import (
	"io"
	"time"
)

// ObservationMetadata records how and when a resource body was acquired. It
// is provenance about the *acquisition*, distinct from the byte-identity
// fields in ResourceMetadata.
type ObservationMetadata struct {
	ObservedAt  time.Time // body acquisition time
	ValidatedAt time.Time // last successful network validation, if any
	ValidUntil  time.Time // zero means no freshness assurance
	Freshness   string    // snapshot | fresh | stale | unknown
	Acquisition string    // local | network | retained | revalidated
}

// FileVersion identifies the exact on-disk file a body was captured from.
// Digest is always over the complete raw file, never a rendered or
// truncated range, so a caller can tell whether a retained body still
// matches the file on disk.
type FileVersion struct {
	Path      string // validated workspace-relative identity
	Digest    string // SHA-256 of the complete raw file
	SizeBytes int64
	Complete  bool
}

// ResourceMetadata describes a published body's provenance and content
// identity. It travels with a ResourceView; it never itself holds body
// bytes.
type ResourceMetadata struct {
	ContentType string
	BodyDigest  string // SHA-256 of the retained canonical representation
	// SourceDigest is optional. Never pretend a prefix hash is the full
	// hash: this field is set only when the digest genuinely covers the
	// complete source, not a truncated capture of it.
	SourceDigest string
	ParentID     ID     // only for actual derived bodies (e.g. a search result's source)
	Relation     string // search_of | extraction_of | snapshot_of
	FileVersion  *FileVersion
	Observation  ObservationMetadata
	// Web identity/cache metadata is optional and only populated for retained
	// web snapshots. It is descriptive; freshness admission remains owned by
	// the session controller.
	RequestedURL string
	FinalURL     string
	ETag         string
	LastModified string
	CacheControl string
	Vary         string
	NoStore      bool
	FreshUntil   time.Time
	Preview      string
}

// BodyLease is a pinned, immutable, read-only handle to a published body's
// bytes. Close releases the pin exactly once; a double-Close is safe (the
// second call is a no-op, not a panic or error) since a caller's own
// cleanup path (defer plus an explicit early-exit path) can plausibly call
// it twice. I/O through a lease never requires holding the registry's
// internal mutex.
type BodyLease interface {
	io.ReaderAt
	Size() int64
	Close() error
}

// ResourceView is what a caller gets back from Publish/OpenBody alongside a
// lease: the metadata needed to interpret and safely consume the body,
// without exposing bytes until OpenBody is called. It is a distinct type
// from View (not an extension of it) — a resource is not a semantic entity
// with a body bolted on, and most View fields (Preview, Payload, Truncated)
// don't apply the same way to a bounded body a caller pages through rather
// than reads whole.
type ResourceView struct {
	ID        ID
	Kind      Kind
	Label     string
	Source    string
	Trust     Trust
	Scope     Scope
	SizeBytes int64
	Resource  ResourceMetadata
	CreatedAt time.Time
}
