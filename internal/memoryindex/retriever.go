package memoryindex

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// Retriever fans a Query out to every registered Source, merges results,
// deduplicates by Item.ContentHash (when non-empty), sorts deterministically,
// and applies Query.TopK as a total cap.
type Retriever struct {
	sources []Source
}

// NewRetriever constructs a Retriever over the given sources. The order of
// sources does not affect Search's output ordering (Search sorts
// deterministically), only the order in which sources are queried.
func NewRetriever(sources ...Source) *Retriever {
	return &Retriever{sources: sources}
}

// Search queries every registered source sequentially (these sources wrap
// only local, in-process, non-blocking lookups, so concurrency buys
// nothing), tolerates per-source errors, filters by Query.Kinds, deduplicates
// by Item.ContentHash, sorts deterministically, and caps the result to
// Query.TopK.
func (r *Retriever) Search(ctx context.Context, q Query) ([]Hit, error) {
	result, err := r.SearchDetailed(ctx, q, RetrievalPolicy{TopK: q.TopK})
	return result.Hits, err
}

// SearchDetailed applies source-local normalization, scope/expiry filters,
// bounded metadata boosts, deterministic diversity, and an optional total
// context budget while retaining rejection reasons for explainability.
func (r *Retriever) SearchDetailed(ctx context.Context, q Query, policy RetrievalPolicy) (RetrievalResult, error) {
	var raw []Hit
	var errs []error

	for _, src := range r.sources {
		hits, err := src.Search(ctx, q)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		raw = append(raw, normalizeSourceHits(hits)...)
	}

	if len(r.sources) > 0 && len(errs) == len(r.sources) {
		return RetrievalResult{}, errors.Join(errs...)
	}

	filtered, rejected := filterCandidates(raw, q)
	ranked := make([]Hit, 0, len(filtered))
	for _, hit := range filtered {
		ranked = append(ranked, rankHit(hit, q))
	}
	deduped, dedupRejected := dedupByContentHashDetailed(ranked)
	rejected = append(rejected, dedupRejected...)

	sort.SliceStable(deduped, func(i, j int) bool {
		return hitLess(deduped[i], deduped[j])
	})

	diverse, diversityRejected := diversifyHits(deduped, policy.KindCaps)
	rejected = append(rejected, diversityRejected...)
	if policy.TopK <= 0 {
		policy.TopK = q.TopK
	}
	result := packHits(diverse, policy)
	result.Rejected = append(rejected, result.Rejected...)
	return result, nil
}

func normalizeSourceHits(hits []Hit) []Hit {
	if len(hits) == 0 {
		return nil
	}
	maxScore := 0.0
	for _, hit := range hits {
		if hit.Score > maxScore {
			maxScore = hit.Score
		}
	}
	out := slices.Clone(hits)
	for index := range out {
		score := 0.0
		if maxScore > 0 {
			score = out[index].Score / maxScore
		}
		out[index].Score = math.Max(0, math.Min(1, score))
		out[index].BaseScore = out[index].Score
		out[index].Components = []ScoreComponent{{Name: "lexical", Value: out[index].Score}}
	}
	return out
}

func filterCandidates(hits []Hit, q Query) ([]Hit, []RejectedHit) {
	now := q.Now
	if now.IsZero() {
		now = time.Now()
	}
	allowedKinds := make(map[Kind]struct{}, len(q.Kinds))
	for _, kind := range q.Kinds {
		allowedKinds[kind] = struct{}{}
	}
	kept := make([]Hit, 0, len(hits))
	var rejected []RejectedHit
	for _, hit := range hits {
		reason := ""
		switch {
		case len(allowedKinds) > 0 && !containsKind(allowedKinds, hit.Item.Kind):
			reason = "kind_filter"
		case hit.Item.ExpiresAt != nil && !hit.Item.ExpiresAt.After(now):
			reason = "expired"
		case hit.Item.ProjectID != "" && hit.Item.ProjectID != q.ProjectID:
			reason = "wrong_project"
		case hit.Item.RunID != "" && q.RunID != "" && hit.Item.RunID != q.RunID:
			reason = "wrong_run"
		}
		if reason != "" {
			rejected = append(rejected, RejectedHit{Hit: hit, Reason: reason})
			continue
		}
		kept = append(kept, hit)
	}
	return kept, rejected
}

func containsKind(kinds map[Kind]struct{}, kind Kind) bool {
	_, ok := kinds[kind]
	return ok
}

func rankHit(hit Hit, q Query) Hit {
	add := func(name string, value float64) {
		if value <= 0 {
			return
		}
		hit.Components = append(hit.Components, ScoreComponent{Name: name, Value: value})
		hit.Score += value
	}
	if q.ProjectID != "" && hit.Item.ProjectID == q.ProjectID {
		add("current_project", 0.20)
	}
	if q.RunID != "" && hit.Item.RunID == q.RunID {
		add("active_run", 0.30)
	}
	switch hit.Item.Trust {
	case TrustUserAuthored:
		add("user_authored", 0.10)
	case TrustControllerObserved:
		add("controller_observed", 0.10)
	}
	if hit.Item.Kind == KindEpisode && !hit.Item.UpdatedAt.IsZero() {
		now := q.Now
		if now.IsZero() {
			now = time.Now()
		}
		age := now.Sub(hit.Item.UpdatedAt)
		if age >= 0 && age < 30*24*time.Hour {
			add("episode_recency", 0.10*(1-age.Hours()/(30*24)))
		}
	}
	if exactMetadataMatch(q.Text, hit.Item) {
		add("exact_metadata", 0.25)
	}
	hit.Score = math.Min(1, hit.Score)
	hit.Why = scoreExplanation(hit)
	return hit
}

func exactMetadataMatch(query string, item Item) bool {
	terms := lexicalTerms(query)
	metadata := strings.Join(append(slices.Clone(item.Tags), item.Source.Path), " ")
	for term := range lexicalTerms(metadata) {
		if _, ok := terms[term]; ok {
			return true
		}
	}
	return false
}

func scoreExplanation(hit Hit) string {
	parts := make([]string, 0, len(hit.Components))
	for _, component := range hit.Components {
		parts = append(parts, fmt.Sprintf("%s %+.3f", component.Name, component.Value))
	}
	return fmt.Sprintf("%s = %.3f", strings.Join(parts, ", "), hit.Score)
}

// filterByKind drops hits whose Item.Kind is not in kinds. An empty kinds
// slice means no filtering.
func filterByKind(hits []Hit, kinds []Kind) []Hit {
	if len(kinds) == 0 {
		return hits
	}
	allowed := make(map[Kind]struct{}, len(kinds))
	for _, k := range kinds {
		allowed[k] = struct{}{}
	}
	out := make([]Hit, 0, len(hits))
	for _, h := range hits {
		if _, ok := allowed[h.Item.Kind]; ok {
			out = append(out, h)
		}
	}
	return out
}

// dedupByContentHash keeps, for each non-empty ContentHash, only the
// higher-ranked hit (per hitLess). Hits with an empty ContentHash are never
// deduplicated against one another.
func dedupByContentHash(hits []Hit) []Hit {
	kept, _ := dedupByContentHashDetailed(hits)
	return kept
}

func dedupByContentHashDetailed(hits []Hit) ([]Hit, []RejectedHit) {
	kept := make([]Hit, 0, len(hits))
	indexByHash := make(map[string]int, len(hits))
	var rejected []RejectedHit

	for _, h := range hits {
		hash := h.Item.ContentHash
		if hash == "" {
			kept = append(kept, h)
			continue
		}
		if idx, ok := indexByHash[hash]; ok {
			if hitLess(h, kept[idx]) {
				rejected = append(rejected, RejectedHit{Hit: kept[idx], Reason: "content_hash_dedup"})
				kept[idx] = h
			} else {
				rejected = append(rejected, RejectedHit{Hit: h, Reason: "content_hash_dedup"})
			}
			continue
		}
		indexByHash[hash] = len(kept)
		kept = append(kept, h)
	}

	return kept, rejected
}

// hitLess reports whether a should sort before b: by Score descending, then
// Item.Kind ascending (lexicographic), then Item.UpdatedAt descending, then
// Item.ID ascending.
func hitLess(a, b Hit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if kindPriority(a.Item.Kind) != kindPriority(b.Item.Kind) {
		return kindPriority(a.Item.Kind) < kindPriority(b.Item.Kind)
	}
	if a.Item.Kind != b.Item.Kind {
		return a.Item.Kind < b.Item.Kind
	}
	if !a.Item.UpdatedAt.Equal(b.Item.UpdatedAt) {
		return a.Item.UpdatedAt.After(b.Item.UpdatedAt)
	}
	return a.Item.ID < b.Item.ID
}

func kindPriority(kind Kind) int {
	priorities := map[Kind]int{
		KindAgentObjective: 0, KindAgentCriterion: 1, KindAgentFailure: 2, KindAgentEvidence: 3,
		KindUserPreference: 4, KindProjectDecision: 5, KindProjectArchitecture: 6,
		KindProjectConvention: 7, KindEpisode: 8, KindSourceChunk: 9,
	}
	if priority, ok := priorities[kind]; ok {
		return priority
	}
	return 100
}

func diversifyHits(hits []Hit, configured map[Kind]int) ([]Hit, []RejectedHit) {
	caps := map[Kind]int{
		KindAgentObjective: 1, KindAgentCriterion: 12, KindAgentFailure: 4, KindAgentEvidence: 8,
		KindUserPreference: 3, KindProjectDecision: 3, KindProjectArchitecture: 3,
		KindProjectConvention: 3, KindEpisode: 3, KindSourceChunk: 5,
	}
	for kind, cap := range configured {
		caps[kind] = cap
	}
	counts := map[Kind]int{}
	var kept []Hit
	var rejected []RejectedHit
	for _, hit := range hits {
		if cap := caps[hit.Item.Kind]; cap > 0 && counts[hit.Item.Kind] >= cap {
			rejected = append(rejected, RejectedHit{Hit: hit, Reason: "kind_cap"})
			continue
		}
		if hit.Item.Kind == KindEpisode && hit.Item.SessionID != "" {
			duplicate := false
			for _, existing := range kept {
				if existing.Item.Kind == KindEpisode && existing.Item.SessionID == hit.Item.SessionID {
					duplicate = true
					break
				}
			}
			if duplicate {
				rejected = append(rejected, RejectedHit{Hit: hit, Reason: "episode_session_cap"})
				continue
			}
		}
		if hit.Item.Kind == KindSourceChunk && overlapsSelectedChunk(hit, kept) {
			rejected = append(rejected, RejectedHit{Hit: hit, Reason: "overlapping_source_chunk"})
			continue
		}
		kept = append(kept, hit)
		counts[hit.Item.Kind]++
	}
	return kept, rejected
}

func overlapsSelectedChunk(candidate Hit, selected []Hit) bool {
	ref := candidate.Item.Source
	if ref.Path == "" {
		return false
	}
	for _, hit := range selected {
		other := hit.Item.Source
		if hit.Item.Kind == KindSourceChunk && other.Path == ref.Path && ref.StartLine <= other.EndLine && other.StartLine <= ref.EndLine {
			return true
		}
	}
	return false
}

func packHits(hits []Hit, policy RetrievalPolicy) RetrievalResult {
	result := RetrievalResult{TierTokens: map[Scope]int{}}
	deferred := make([]Hit, 0)
	for _, hit := range hits {
		hit.Tokens = estimateHitTokens(hit)
		if policy.TopK > 0 && len(result.Hits) >= policy.TopK {
			result.Rejected = append(result.Rejected, RejectedHit{Hit: hit, Reason: "top_k"})
			continue
		}
		if policy.MaxTokens > 0 && result.TotalTokens+hit.Tokens > policy.MaxTokens {
			result.Rejected = append(result.Rejected, RejectedHit{Hit: hit, Reason: "total_budget"})
			continue
		}
		cap := tierTokenCap(hit.Item.Kind, policy)
		if cap > 0 && result.TierTokens[hit.Item.Scope]+hit.Tokens > cap {
			deferred = append(deferred, hit)
			continue
		}
		selectHit(&result, hit)
	}
	for _, hit := range deferred {
		if policy.TopK > 0 && len(result.Hits) >= policy.TopK {
			result.Rejected = append(result.Rejected, RejectedHit{Hit: hit, Reason: "top_k"})
			continue
		}
		if policy.MaxTokens > 0 && result.TotalTokens+hit.Tokens > policy.MaxTokens {
			result.Rejected = append(result.Rejected, RejectedHit{Hit: hit, Reason: "tier_budget"})
			continue
		}
		selectHit(&result, hit)
	}
	return result
}

func selectHit(result *RetrievalResult, hit Hit) {
	result.Hits = append(result.Hits, hit)
	result.TotalTokens += hit.Tokens
	result.TierTokens[hit.Item.Scope] += hit.Tokens
}

func tierTokenCap(kind Kind, policy RetrievalPolicy) int {
	switch kind {
	case KindUserPreference:
		return policy.UserTokens
	case KindProjectArchitecture, KindProjectConvention, KindProjectDecision:
		return policy.ProjectTokens
	case KindEpisode:
		return policy.EpisodeTokens
	case KindAgentObjective, KindAgentCriterion, KindAgentFailure, KindAgentEvidence:
		return policy.AgentTokens
	case KindSourceChunk:
		return policy.SourceTokens
	default:
		return 0
	}
}

func estimateHitTokens(hit Hit) int {
	text := hit.Item.Text
	if hit.Item.Summary != "" {
		text = hit.Item.Summary
	}
	return max(1, (len(text)+3)/4+16)
}
