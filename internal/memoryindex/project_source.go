package memoryindex

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

const defaultProjectSourceTopK = 10

// ProjectSource exposes approved records from one workspace-isolated
// ProjectStore to the unified Retriever.
type ProjectSource struct {
	Store *ProjectStore
	TopK  int
}

// Search returns lexical matches only when the query carries the exact
// workspace identity owned by the store. Pending proposals are never returned.
func (s ProjectSource) Search(ctx context.Context, q Query) ([]Hit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Store == nil || q.ProjectID == "" || q.ProjectID != s.Store.ProjectID() {
		return nil, nil
	}
	queryTerms := lexicalTerms(q.Text)
	if len(queryTerms) == 0 {
		return nil, nil
	}
	records, err := s.Store.Load()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	hits := make([]Hit, 0, len(records))
	for _, record := range records {
		if record.Review != ReviewApproved || !queryAllowsKind(q.Kinds, record.Kind) {
			continue
		}
		matched := matchingTerms(queryTerms, lexicalTerms(record.Text+" "+strings.Join(record.Tags, " ")))
		if len(matched) == 0 {
			continue
		}
		hits = append(hits, Hit{
			Item: Item{
				ID:        record.ID,
				Text:      record.Text,
				Kind:      record.Kind,
				Scope:     ScopeProject,
				ProjectID: record.ProjectID,
				RunID:     record.SourceRunID,
				Source: SourceRef{
					RunID: record.SourceRunID,
					Cycle: record.SourceCycle,
				},
				Tags:        slices.Clone(record.Tags),
				CreatedAt:   record.CreatedAt,
				UpdatedAt:   record.UpdatedAt,
				Trust:       record.Trust,
				ContentHash: record.ContentHash,
			},
			Score:        float64(len(matched)) / float64(len(queryTerms)),
			MatchedTerms: matched,
			Why: fmt.Sprintf(
				"matched %d query term(s): %s",
				len(matched),
				strings.Join(matched, ", "),
			),
		})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		return hitLess(hits[i], hits[j])
	})
	limit := s.TopK
	if limit <= 0 {
		limit = defaultProjectSourceTopK
	}
	if q.TopK > 0 && q.TopK < limit {
		limit = q.TopK
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func queryAllowsKind(kinds []Kind, kind Kind) bool {
	if len(kinds) == 0 {
		return true
	}
	return slices.Contains(kinds, kind)
}

func matchingTerms(queryTerms, recordTerms map[string]struct{}) []string {
	matched := make([]string, 0, min(len(queryTerms), len(recordTerms)))
	for term := range queryTerms {
		if _, exists := recordTerms[term]; exists {
			matched = append(matched, term)
		}
	}
	sort.Strings(matched)
	return matched
}

var lexicalStopwords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "and": {}, "or": {}, "to": {},
	"of": {}, "in": {}, "for": {}, "is": {}, "are": {}, "i": {},
	"me": {}, "my": {}, "you": {}, "it": {}, "with": {}, "use": {},
	"using": {}, "please": {}, "prefer": {}, "when": {}, "how": {},
	"what": {}, "can": {}, "do": {}, "does": {},
}

func lexicalTerms(text string) map[string]struct{} {
	terms := map[string]struct{}{}
	isTermRune := func(r rune) bool {
		return 'a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '.' || r == '-'
	}
	for _, term := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !isTermRune(r)
	}) {
		if len(term) < 2 {
			continue
		}
		if _, isStopword := lexicalStopwords[term]; isStopword {
			continue
		}
		terms[term] = struct{}{}
	}
	return terms
}
