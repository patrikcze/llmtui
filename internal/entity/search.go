package entity

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

// Search finds live entities using case-insensitive keywords in safe metadata,
// previews and stored payloads. It returns minimal candidates, never inferred
// identities or full payloads. It does not touch lifetime or expansion budgets.
// Work is bounded by the registry's storage limits and the caller's result cap.
func (r *Registry) Search(query string, limit int) ([]View, int) {
	if r == nil || limit <= 0 {
		return nil, 0
	}
	terms := strings.FieldsFunc(strings.ToLower(query), func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsNumber(c)
	})
	if len(terms) == 0 {
		return nil, 0
	}
	slices.Sort(terms)
	terms = slices.Compact(terms)
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
		label := strings.ToLower(item.view.Label)
		metadata := strings.ToLower(item.view.Metadata.Path + " " + item.view.Metadata.URL)
		preview := strings.ToLower(item.view.Preview)
		payload := strings.ToLower(item.payload)
		score := 0
		for _, term := range terms {
			switch {
			case strings.Contains(label, term):
				score += 8
			case strings.Contains(metadata, term):
				score += 4
			case strings.Contains(preview, term):
				score += 2
			case strings.Contains(payload, term):
				score++
			}
		}
		if score > 0 {
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
	views := make([]View, 0, min(limit, len(matches)))
	for _, match := range matches[:min(limit, len(matches))] {
		views = append(views, match.item.view)
	}
	return views, len(matches)
}
