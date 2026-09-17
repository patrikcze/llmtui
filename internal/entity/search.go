package entity

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

// SearchOptions controls bounded registry lookup. A nil or empty Kinds slice
// preserves the historical cross-kind search behavior.
type SearchOptions struct {
	Query string
	Kinds []Kind
	Limit int
}

// Search finds live entities using case-insensitive keywords in safe metadata,
// previews and stored payloads. It returns minimal candidates, never inferred
// identities or full payloads. It does not touch lifetime or expansion budgets.
// Work is bounded by the registry's storage limits and the caller's result cap.
func (r *Registry) Search(query string, limit int) ([]View, int) {
	return r.SearchWithOptions(SearchOptions{Query: query, Limit: limit})
}

// SearchWithOptions finds live entities using explicit query and kind
// selection. Ranking remains inside entity so callers cannot accidentally
// substitute a topical result from another evidence class.
func (r *Registry) SearchWithOptions(options SearchOptions) ([]View, int) {
	if r == nil || options.Limit <= 0 {
		return nil, 0
	}
	terms := strings.FieldsFunc(strings.ToLower(options.Query), func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsNumber(c)
	})
	terms = meaningfulTerms(terms)
	if len(terms) == 0 {
		return nil, 0
	}
	slices.Sort(terms)
	terms = slices.Compact(terms)
	kinds := make(map[Kind]struct{}, len(options.Kinds))
	for _, kind := range options.Kinds {
		kinds[kind] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.limits.Now().UTC()
	type match struct {
		item  *record
		score int
	}
	matches := make([]match, 0)
	for _, item := range r.items {
		if !item.expiresAt.IsZero() && !item.expiresAt.After(now) {
			continue
		}
		if len(kinds) > 0 {
			if _, ok := kinds[item.view.Kind]; !ok {
				continue
			}
		}
		label := strings.ToLower(item.view.Label)
		metadata := strings.ToLower(item.view.Metadata.Path + " " + item.view.Metadata.URL)
		preview := strings.ToLower(item.view.Preview)
		payload := strings.ToLower(item.payload)
		score := 0
		matched := 0
		for _, term := range terms {
			switch {
			case strings.Contains(label, term):
				score += 8
				matched++
			case strings.Contains(metadata, term):
				score += 4
				matched++
			case strings.Contains(preview, term):
				score += 2
				matched++
			case strings.Contains(payload, term):
				score++
				matched++
			}
		}
		if score > 0 && matched >= minimumTermMatches(len(terms)) {
			matches = append(matches, match{item: item, score: score})
		}
	}
	slices.SortFunc(matches, func(a, b match) int {
		if order := cmp.Compare(b.score, a.score); order != 0 {
			return order
		}
		if order := b.item.lastUsedAt.Compare(a.item.lastUsedAt); order != 0 {
			return order
		}
		return cmp.Compare(b.item.view.ID, a.item.view.ID)
	})
	views := make([]View, 0, min(options.Limit, len(matches)))
	for _, match := range matches[:min(options.Limit, len(matches))] {
		views = append(views, match.item.view)
	}
	return views, len(matches)
}

var searchStopWords = map[string]struct{}{
	"a": {}, "about": {}, "an": {}, "and": {}, "are": {}, "did": {}, "for": {},
	"how": {}, "in": {}, "is": {}, "it": {}, "me": {}, "of": {}, "on": {},
	"or": {}, "the": {}, "to": {}, "was": {}, "what": {}, "with": {},
}

func meaningfulTerms(terms []string) []string {
	filtered := make([]string, 0, len(terms))
	for _, term := range terms {
		if _, stop := searchStopWords[term]; !stop {
			filtered = append(filtered, term)
		}
	}
	return filtered
}

func minimumTermMatches(termCount int) int {
	switch {
	case termCount >= 5:
		return 3
	case termCount >= 3:
		return 2
	default:
		return 1
	}
}
