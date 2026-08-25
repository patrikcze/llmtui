package memoryindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/patrikcze/llmtui/internal/memory"
)

// UserSource wraps the existing internal/memory user-preference store. It
// must select exactly what memory.Relevant already selects today: at most
// 3 snippets (memoryindex does not add its own limit — it asks for 3 to
// match current compositionBase behavior, see Global Constraint 2).
type UserSource struct {
	Snippets func() ([]memory.Snippet, error) // e.g. m.memStore.Load
}

// Search returns memory.Relevant's top-3 snippets projected into Hits,
// scored by rank since memory.Relevant does not expose a raw score.
func (s UserSource) Search(ctx context.Context, q Query) ([]Hit, error) {
	snippets, err := s.Snippets()
	if err != nil {
		return nil, err
	}

	relevant := memory.Relevant(snippets, q.Text, 3)
	if len(relevant) == 0 {
		return nil, nil
	}

	hits := make([]Hit, len(relevant))
	for i, sn := range relevant {
		hits[i] = Hit{
			Item: Item{
				ID:          sn.ID,
				Text:        sn.Text,
				Kind:        KindUserPreference,
				Scope:       ScopeUser,
				Tags:        sn.Tags,
				CreatedAt:   sn.CreatedAt,
				UpdatedAt:   sn.UpdatedAt,
				Trust:       TrustUserAuthored,
				ContentHash: contentHash(sn.Text),
			},
			Score:        1.0 - float64(i)/float64(len(relevant)),
			MatchedTerms: nil,
		}
	}
	return hits, nil
}

// contentHash returns the first 16 hex characters of the SHA-256 digest of
// text, matching the hashing style used elsewhere in this repo (see
// internal/tui's digestText).
func contentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}
