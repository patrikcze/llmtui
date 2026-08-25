// Package memoryindex defines the shared, provider-agnostic types for
// llmtui's tiered memory retrieval: item kinds, scopes, trust classes, and
// the Retriever that fans a Query out across pluggable Sources and merges
// their results deterministically.
//
// This package owns no persistence and no wiring into the live app; it is
// pure types and merge logic. Adapters that expose internal/memory and
// internal/rag as Sources, and the composition that registers them into the
// TUI's prompt pipeline, live elsewhere and are added by later work.
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
	ID, Text, Summary           string
	Kind                        Kind
	Scope                       Scope
	ProjectID, SessionID, RunID string
	Source                      SourceRef
	Tags                        []string
	CreatedAt, UpdatedAt        time.Time
	ExpiresAt                   *time.Time
	Confidence                  float64
	Trust                       TrustClass
	ContentHash                 string
}

// Query describes a retrieval request against one or more Sources.
type Query struct {
	Text                        string
	ProjectID, SessionID, RunID string
	Kinds                       []Kind // empty = no kind filter
	TopK                        int    // total cap across all sources; <=0 = no cap
	Now                         time.Time
}

// Hit is one scored Item returned by a Source.
type Hit struct {
	Item         Item
	Score        float64 // each Source must return scores normalized to [0,1]
	MatchedTerms []string
	Why          string
}

// Source is one pluggable memory backend. Implementations must honor ctx
// and must apply their own internal relevance/top-K limiting before
// returning — the Retriever does not re-rank within a single source's
// results, only across sources.
type Source interface {
	Search(ctx context.Context, q Query) ([]Hit, error)
}
