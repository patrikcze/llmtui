// Package memoryindex defines the shared, provider-agnostic types for
// llmtui's tiered memory retrieval: item kinds, scopes, trust classes, and
// the Retriever that fans a Query out across pluggable Sources and merges
// their results deterministically. It also owns the Source adapters that
// wrap internal/memory (UserSource) and internal/rag (RAGSource) — this
// package imports both, and sits above both in the dependency graph, so
// neither internal/memory nor internal/rag may ever import memoryindex.
// internal/tui wires those adapters into the live app's prompt pipeline.
//
// This is Phase 0-1 of a larger tiered-memory plan. It deliberately does
// not implement token-budget packing, kind-priority ordering, cross-source
// score boosts, or diversity/dedup-by-file: Retriever's dedup is
// ContentHash-equality only, and its sort is a simple deterministic
// tie-break (score, then kind, then recency, then ID). Those are explicitly
// deferred to a later phase, not oversights in this one.
package memoryindex

import (
	"context"
	"time"
)

// Kind identifies the category of a memory Item.
type Kind string

const (
	KindUserPreference      Kind = "user_preference"
	KindProjectArchitecture Kind = "project_architecture"
	KindProjectConvention   Kind = "project_convention"
	KindProjectDecision     Kind = "project_decision"
	KindEpisode             Kind = "episode"
	KindAgentObjective      Kind = "agent_objective"
	KindAgentCriterion      Kind = "agent_criterion"
	KindAgentFailure        Kind = "agent_failure"
	KindAgentEvidence       Kind = "agent_evidence"
	KindSourceChunk         Kind = "source_chunk"
)

// Scope identifies the durability/visibility tier a memory Item belongs to.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeSession Scope = "session"
	ScopeRun     Scope = "run"
)

// TrustClass identifies how much a memory Item's content should be trusted,
// based on where it came from.
type TrustClass string

const (
	TrustUserAuthored       TrustClass = "user_authored"
	TrustControllerObserved TrustClass = "controller_observed"
	TrustModelProposed      TrustClass = "model_proposed"
	TrustWorkspaceUntrusted TrustClass = "workspace_untrusted"
)

// SourceRef locates where an Item came from, for provenance display.
// Populate only the fields relevant to the item's Kind; leave the rest zero.
type SourceRef struct {
	Path      string // workspace-relative path, for KindSourceChunk
	StartLine int
	EndLine   int
	SessionID string // for KindEpisode
	RunID     string // for KindAgent*
	Cycle     int    // for KindAgent*
}

// Item is one unit of retrievable memory.
type Item struct {
	ID, Text string
	// Summary is reserved for a future phase (e.g. a condensed form for
	// token-budget packing). No code in this phase populates or reads it.
	Summary                     string
	Kind                        Kind
	Scope                       Scope
	ProjectID, SessionID, RunID string
	Source                      SourceRef
	Tags                        []string
	CreatedAt, UpdatedAt        time.Time
	// ExpiresAt is reserved for a future TTL/expiry phase. Retriever.Search
	// does not read or honor it today — setting it does not filter or expire
	// an Item.
	ExpiresAt *time.Time
	// Confidence is reserved for a future phase (e.g. cross-source score
	// boosts). Retriever.Search does not read or honor it today.
	Confidence  float64
	Trust       TrustClass
	ContentHash string
}

// Query describes a retrieval request against one or more Sources.
type Query struct {
	Text                        string
	ProjectID, SessionID, RunID string
	Kinds                       []Kind // empty = no kind filter
	TopK                        int    // total cap across all sources; <=0 = no cap
	// Now is reserved for a future phase (e.g. evaluating Item.ExpiresAt
	// against a fixed clock for deterministic tests). No Source or the
	// Retriever reads it today.
	Now time.Time
}

// Hit is one scored Item returned by a Source.
type Hit struct {
	Item         Item
	Score        float64 // each Source must return scores normalized to [0,1]
	MatchedTerms []string
	// Why is reserved for a future phase (e.g. a human-readable explanation
	// of why this Hit matched). No Source populates it today.
	Why string
}

// Source is one pluggable memory backend. Implementations should honor ctx
// where the lookup can block (e.g. a future network- or disk-backed
// source); purely in-process sources may ignore it. Implementations must
// apply their own internal relevance/top-K limiting before returning — the
// Retriever does not re-rank within a single source's results, only across
// sources.
type Source interface {
	Search(ctx context.Context, q Query) ([]Hit, error)
}
