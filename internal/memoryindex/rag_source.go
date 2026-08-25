package memoryindex

import (
	"context"

	"github.com/patrikcze/llmtui/internal/rag"
)

// RAGSource wraps the existing internal/rag workspace index. It must select
// exactly what Index.Search already selects today (Global Constraint 2).
// Chunk is carried on the Hit's Item via SourceRef so the caller can
// reconstruct a rag.Result identical to what Index.Search would have
// returned directly — see Task 4.
type RAGSource struct {
	Index func() *rag.Index // e.g. func() *rag.Index { return m.ragIndex }
	TopK  func() int        // e.g. m.ragTopK
}

// Search returns hits projecting rag.Index.Search's results, with scores
// normalized into [0,1] by dividing each result's score by the maximum
// score in the result set (1.0 when there is one result or all scores are
// equal). MatchedTerms are carried through unchanged.
func (s RAGSource) Search(ctx context.Context, q Query) ([]Hit, error) {
	idx := s.Index()
	if idx == nil {
		return nil, nil
	}

	results := idx.Search(q.Text, s.TopK())
	if len(results) == 0 {
		return nil, nil
	}

	scores := normalizeScores(results)

	hits := make([]Hit, len(results))
	for i, r := range results {
		hits[i] = Hit{
			Item: Item{
				ID:    r.Chunk.ID,
				Text:  r.Chunk.Text,
				Kind:  KindSourceChunk,
				Scope: ScopeProject,
				Source: SourceRef{
					Path:      r.Chunk.Path,
					StartLine: r.Chunk.StartLine,
					EndLine:   r.Chunk.EndLine,
				},
				UpdatedAt:   r.Chunk.UpdatedAt,
				Trust:       TrustWorkspaceUntrusted,
				ContentHash: r.Chunk.Hash,
			},
			Score:        scores[i],
			MatchedTerms: r.MatchedTerms,
		}
	}
	return hits, nil
}

// SearchRaw returns the underlying rag.Result values unmodified, in the
// same order Search's Hits would be produced from. Task 4 uses this to
// preserve exact legacy output (m.ragLast, RetrievedContext) while Search
// (above) is what feeds the unified Retriever/debug ranking.
func (s RAGSource) SearchRaw(query string) []rag.Result {
	idx := s.Index()
	if idx == nil {
		return nil
	}
	return idx.Search(query, s.TopK())
}

// normalizeScores maps each result's raw BM25 score into [0,1] by dividing
// by the maximum score in the set. When there is a single result, or all
// results share the same score (including a shared zero), every score
// normalizes to 1.0 rather than dividing by zero or producing a
// meaningless flat ratio.
func normalizeScores(results []rag.Result) []float64 {
	out := make([]float64, len(results))

	max := results[0].Score
	allEqual := true
	for _, r := range results {
		if r.Score > max {
			max = r.Score
		}
		if r.Score != results[0].Score {
			allEqual = false
		}
	}

	if len(results) == 1 || allEqual || max == 0 {
		for i := range out {
			out[i] = 1.0
		}
		return out
	}

	for i, r := range results {
		out[i] = r.Score / max
	}
	return out
}
