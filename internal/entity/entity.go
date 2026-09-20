// Package entity provides bounded, session-local runtime entities.
//
// Entities are ephemeral references to useful runtime data. They are not
// durable memory, tool capabilities, or authorization handles.
package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	IDPrefix                 = "ent_"
	idDigits                 = 5
	maxIDSequence            = 99999
	DefaultMaxEntities       = 256
	DefaultMaxPayloadBytes   = 64 * 1024
	DefaultMaxTotalPayload   = 4 * 1024 * 1024
	DefaultMaxPreviewBytes   = 768
	DefaultMaxFullExpansions = 8
)

// Kind identifies the normalized object represented by an entity.
type Kind string

const (
	KindWebResult  Kind = "web_result"
	KindWebPage    Kind = "web_page"
	KindFile       Kind = "file"
	KindMCPResult  Kind = "mcp_result"
	KindCollection Kind = "collection"
	// KindVisionObservation is a bounded, model-derived description of a
	// user-provided image. It is evidence about visible pixels, not external
	// source provenance or durable memory.
	KindVisionObservation Kind = "vision_observation"
)

// Level controls progressive disclosure.
type Level string

const (
	LevelIdentifier Level = "identifier"
	LevelMinimal    Level = "minimal"
	LevelFull       Level = "full"
)

// Trust describes the origin of the entity data.
type Trust string

const (
	TrustControllerObserved Trust = "controller_observed"
	TrustWorkspaceUntrusted Trust = "workspace_untrusted"
	TrustWebUntrusted       Trust = "web_untrusted"
	TrustMCPUntrusted       Trust = "mcp_untrusted"
	// TrustVisionModelDerived keeps model-derived visual evidence distinct
	// from controller-observed attachment metadata.
	TrustVisionModelDerived Trust = "vision_model_derived"
)

// Scope controls the intended lifetime of a record.
type Scope string

const (
	ScopeTurn     Scope = "turn"
	ScopeSession  Scope = "session"
	ScopeAgentRun Scope = "agent_run"
)

// ResolutionStatus is returned for model-visible lookup failures.
type ResolutionStatus string

const (
	StatusOK                 ResolutionStatus = "ok"
	StatusInvalidID          ResolutionStatus = "invalid_id"
	StatusNotFound           ResolutionStatus = "entity_not_found"
	StatusExpired            ResolutionStatus = "entity_expired"
	StatusUnavailable        ResolutionStatus = "entity_unavailable"
	StatusDetailNotAvailable ResolutionStatus = "detail_not_available"
)

var idPattern = regexp.MustCompile(`^` + IDPrefix + `[0-9]{` + fmt.Sprint(idDigits) + `}$`)

// ID is an opaque entity identifier. The string deliberately contains no
// source identity, path, URL, secret, or pointer address.
type ID string

// ParseID validates the exact model-facing identifier format.
func ParseID(raw string) (ID, error) {
	if !idPattern.MatchString(raw) {
		return "", fmt.Errorf("invalid entity ID %q", raw)
	}
	return ID(raw), nil
}

// Valid reports whether id is a canonical entity identifier.
func (id ID) Valid() bool {
	return idPattern.MatchString(string(id))
}

func (id ID) String() string { return string(id) }

// Metadata contains only typed, bounded fields suitable for a runtime view.
// The adapter is responsible for ensuring paths and URLs are safe before it
// constructs a candidate.
type Metadata struct {
	Path        string
	URL         string
	ContentType string
	SizeBytes   int
	StatusCode  int
	Index       int
	Count       int
}

// Provenance identifies the producing subsystem. Reference is retained for
// local diagnostics and is never included in the compact model view.
type Provenance struct {
	Source    string
	Operation string
	Reference string
	CallID    string
	RunID     string
	Cycle     int
	// Vision fields are populated only for KindVisionObservation. They remain
	// controller-owned diagnostics and are not copied into the compact view.
	AttachmentDigest string
	MessageID        string
	ImageIndex       int
	MIME             string
	Provider         string
	Model            string
	CapturedAt       time.Time
	CaptureVersion   string
	CaptureTruncated bool
	RawRetained      bool
}

// Candidate is the normalized input accepted by Registry.Put.
type Candidate struct {
	Kind       Kind
	Provenance Provenance
	Label      string
	Metadata   Metadata
	Trust      Trust
	Scope      Scope
	ScopeID    string
	Payload    string
	Preview    string
	ExpiresAt  time.Time
	Redacted   bool
}

// View is a progressive model-facing representation of one entity. Payload
// is populated only at LevelFull and is always bounded by registry limits.
type View struct {
	ID        ID
	Kind      Kind
	Label     string
	Source    string
	Metadata  Metadata
	Trust     Trust
	Scope     Scope
	Level     Level
	Preview   string
	Payload   string
	Digest    string
	Truncated bool
	Redacted  bool
}

// Resolution is one deterministic result from Resolve or ResolveMany.
type Resolution struct {
	ID     string
	Status ResolutionStatus
	View   View
	Error  string
}

// Limits bounds registry memory and model-facing expansion work.
type Limits struct {
	MaxEntities       int
	MaxPayloadBytes   int
	MaxTotalPayload   int
	MaxPreviewBytes   int
	MaxFullExpansions int
	Now               func() time.Time
}

func (l Limits) normalized() Limits {
	if l.MaxEntities <= 0 {
		l.MaxEntities = DefaultMaxEntities
	}
	if l.MaxPayloadBytes <= 0 {
		l.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	if l.MaxTotalPayload <= 0 {
		l.MaxTotalPayload = DefaultMaxTotalPayload
	}
	if l.MaxPreviewBytes <= 0 {
		l.MaxPreviewBytes = DefaultMaxPreviewBytes
	}
	if l.MaxFullExpansions <= 0 {
		l.MaxFullExpansions = DefaultMaxFullExpansions
	}
	if l.MaxPayloadBytes > l.MaxTotalPayload {
		l.MaxPayloadBytes = l.MaxTotalPayload
	}
	if l.Now == nil {
		l.Now = time.Now
	}
	return l
}

func digest(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func truncateBytes(value string, max int) (string, bool) {
	if max <= 0 || len(value) <= max {
		return value, false
	}
	if max < len("…") {
		cut := max
		for cut > 0 && !utf8.ValidString(value[:cut]) {
			cut--
		}
		return value[:cut], true
	}
	cut := max - len("…")
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + "…", true
}

func validCandidate(c Candidate) error {
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
	if c.Payload == "" && c.Preview == "" && !c.Redacted {
		return errors.New("entity needs payload or preview")
	}
	return nil
}

// ValidKind reports whether kind is one of the model-addressable runtime
// entity kinds. Registry candidates remain typed strings for compatibility,
// but controller lookup filters accept only this closed vocabulary.
func ValidKind(kind Kind) bool {
	switch kind {
	case KindWebResult, KindWebPage, KindFile, KindMCPResult, KindCollection, KindVisionObservation:
		return true
	default:
		return false
	}
}

func boundedMetadata(metadata Metadata) Metadata {
	metadata.Path, _ = truncateBytes(metadata.Path, 512)
	metadata.URL, _ = truncateBytes(metadata.URL, 2048)
	metadata.ContentType, _ = truncateBytes(metadata.ContentType, 128)
	if metadata.SizeBytes < 0 {
		metadata.SizeBytes = 0
	}
	if metadata.StatusCode < 0 {
		metadata.StatusCode = 0
	}
	if metadata.Index < 0 {
		metadata.Index = 0
	}
	if metadata.Count < 0 {
		metadata.Count = 0
	}
	return metadata
}
