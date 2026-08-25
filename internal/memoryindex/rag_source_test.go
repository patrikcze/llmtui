package memoryindex

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/rag"
)

func testRAGIndex() *rag.Index {
	now := time.Now()
	return rag.NewIndex([]rag.DocumentChunk{
		{
			ID: "auth.go#1-4", Path: "internal/auth/auth.go", StartLine: 1, EndLine: 4,
			Text: "func Authenticate(token string) error {\n// validate the bearer token\nreturn nil\n}",
			Hash: "hash-auth", UpdatedAt: now,
		},
		{
			ID: "session.go#1-4", Path: "internal/auth/session.go", StartLine: 1, EndLine: 4,
			Text: "func NewSession(token string) *Session {\n// bearer token check only, no validation performed here\nreturn &Session{}\n}",
			Hash: "hash-session", UpdatedAt: now,
		},
		{
			ID: "math.go#1-1", Path: "internal/util/math.go", StartLine: 1, EndLine: 1,
			Text: "func Add(a, b int) int { return a + b }",
			Hash: "hash-math", UpdatedAt: now,
		},
	})
}

func TestRAGSource_MatchesIndexSearchExactly(t *testing.T) {
	idx := testRAGIndex()
	query := "token validate bearer"
	topK := 5

	s := RAGSource{
		Index: func() *rag.Index { return idx },
		TopK:  func() int { return topK },
	}

	got := s.SearchRaw(query)
	want := idx.Search(query, topK)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchRaw diverged from Index.Search:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestRAGSource_SearchProducesNormalizedHits(t *testing.T) {
	idx := testRAGIndex()
	query := "token validate bearer"
	topK := 5

	s := RAGSource{
		Index: func() *rag.Index { return idx },
		TopK:  func() int { return topK },
	}

	want := idx.Search(query, topK)
	if len(want) < 2 {
		t.Fatalf("need at least 2 raw results to check normalization, got %d", len(want))
	}

	got, err := s.Search(context.Background(), Query{Text: query})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d hits, want %d", len(got), len(want))
	}

	for i, h := range got {
		if h.Score < 0 || h.Score > 1 {
			t.Errorf("hit %d score %v out of [0,1]", i, h.Score)
		}
		if !reflect.DeepEqual(h.MatchedTerms, want[i].MatchedTerms) {
			t.Errorf("hit %d MatchedTerms = %+v, want %+v", i, h.MatchedTerms, want[i].MatchedTerms)
		}
		if h.Item.Source.Path != want[i].Chunk.Path {
			t.Errorf("hit %d Source.Path = %q, want %q", i, h.Item.Source.Path, want[i].Chunk.Path)
		}
		if h.Item.Source.StartLine != want[i].Chunk.StartLine {
			t.Errorf("hit %d Source.StartLine = %d, want %d", i, h.Item.Source.StartLine, want[i].Chunk.StartLine)
		}
		if h.Item.Source.EndLine != want[i].Chunk.EndLine {
			t.Errorf("hit %d Source.EndLine = %d, want %d", i, h.Item.Source.EndLine, want[i].Chunk.EndLine)
		}
	}

	// The top result (highest raw score) must normalize to 1.0.
	if got[0].Score != 1.0 {
		t.Errorf("top hit score = %v, want 1.0", got[0].Score)
	}
}

func TestRAGSource_NilIndexReturnsNoHits(t *testing.T) {
	s := RAGSource{
		Index: func() *rag.Index { return nil },
		TopK:  func() int { return 5 },
	}

	gotHits, err := s.Search(context.Background(), Query{Text: "anything"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotHits != nil {
		t.Errorf("Search got %+v hits, want nil", gotHits)
	}

	gotRaw := s.SearchRaw("anything")
	if gotRaw != nil {
		t.Errorf("SearchRaw got %+v, want nil", gotRaw)
	}
}
