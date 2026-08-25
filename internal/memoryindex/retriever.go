package memoryindex

import (
	"context"
	"errors"
	"sort"
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
	var raw []Hit
	var errs []error

	for _, src := range r.sources {
		hits, err := src.Search(ctx, q)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		raw = append(raw, hits...)
	}

	if len(r.sources) > 0 && len(errs) == len(r.sources) {
		return nil, errors.Join(errs...)
	}

	filtered := filterByKind(raw, q.Kinds)
	deduped := dedupByContentHash(filtered)

	sort.SliceStable(deduped, func(i, j int) bool {
		return hitLess(deduped[i], deduped[j])
	})

	if q.TopK > 0 && len(deduped) > q.TopK {
		deduped = deduped[:q.TopK]
	}

	return deduped, nil
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
	kept := make([]Hit, 0, len(hits))
	indexByHash := make(map[string]int, len(hits))

	for _, h := range hits {
		hash := h.Item.ContentHash
		if hash == "" {
			kept = append(kept, h)
			continue
		}
		if idx, ok := indexByHash[hash]; ok {
			if hitLess(h, kept[idx]) {
				kept[idx] = h
			}
			continue
		}
		indexByHash[hash] = len(kept)
		kept = append(kept, h)
	}

	return kept
}

// hitLess reports whether a should sort before b: by Score descending, then
// Item.Kind ascending (lexicographic), then Item.UpdatedAt descending, then
// Item.ID ascending.
func hitLess(a, b Hit) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.Item.Kind != b.Item.Kind {
		return a.Item.Kind < b.Item.Kind
	}
	if !a.Item.UpdatedAt.Equal(b.Item.UpdatedAt) {
		return a.Item.UpdatedAt.After(b.Item.UpdatedAt)
	}
	return a.Item.ID < b.Item.ID
}
