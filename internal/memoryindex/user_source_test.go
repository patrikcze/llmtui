package memoryindex

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/memory"
)

func testSnippets() []memory.Snippet {
	now := time.Now()
	return []memory.Snippet{
		{ID: "s1", Text: "prefer tabs over spaces in go code", CreatedAt: now, UpdatedAt: now, Tags: []string{"style"}},
		{ID: "s2", Text: "always use go modules for dependency management", CreatedAt: now, UpdatedAt: now},
		{ID: "s3", Text: "the sky is blue and the sea is deep", CreatedAt: now, UpdatedAt: now},
		{ID: "s4", Text: "go tests should use table driven style", CreatedAt: now, UpdatedAt: now},
	}
}

func TestUserSource_MatchesMemoryRelevantExactly(t *testing.T) {
	snippets := testSnippets()
	query := "go tabs style"

	s := UserSource{
		Snippets: func() ([]memory.Snippet, error) { return snippets, nil },
	}

	got, err := s.Search(context.Background(), Query{Text: query})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	want := memory.Relevant(snippets, query, 3)
	if len(got) != len(want) {
		t.Fatalf("got %d hits, want %d matching memory.Relevant: got=%+v want=%+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].Item.Text != want[i].Text {
			t.Errorf("hit %d: got Text %q, want %q (memory.Relevant order)", i, got[i].Item.Text, want[i].Text)
		}
	}
}

func TestUserSource_ScoresDescendingInRange01(t *testing.T) {
	snippets := testSnippets()
	s := UserSource{
		Snippets: func() ([]memory.Snippet, error) { return snippets, nil },
	}

	got, err := s.Search(context.Background(), Query{Text: "go style tabs tests"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("need at least 2 hits to check ordering, got %d: %+v", len(got), got)
	}

	for i, h := range got {
		if h.Score < 0 || h.Score > 1 {
			t.Errorf("hit %d score %v out of [0,1]", i, h.Score)
		}
		if i > 0 && got[i-1].Score < h.Score {
			t.Errorf("scores not non-increasing at index %d: %v then %v", i, got[i-1].Score, h.Score)
		}
	}
}

func TestUserSource_PropagatesLoadError(t *testing.T) {
	wantErr := errors.New("boom")
	s := UserSource{
		Snippets: func() ([]memory.Snippet, error) { return nil, wantErr },
	}

	got, err := s.Search(context.Background(), Query{Text: "anything"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got error %v, want %v", err, wantErr)
	}
	if got != nil {
		t.Errorf("got %+v hits, want nil on error", got)
	}
}

func TestUserSource_NilIndexOrEmptySnippets(t *testing.T) {
	s := UserSource{
		Snippets: func() ([]memory.Snippet, error) { return nil, nil },
	}

	got, err := s.Search(context.Background(), Query{Text: "anything"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v hits, want nil for empty snippet store", got)
	}
}
