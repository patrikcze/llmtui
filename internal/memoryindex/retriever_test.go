package memoryindex

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeSource is a test-only Source stub whose Search returns canned hits
// and/or an error.
type fakeSource struct {
	hits []Hit
	err  error
}

func (f fakeSource) Search(ctx context.Context, q Query) ([]Hit, error) {
	return f.hits, f.err
}

func TestRetriever_MergesAcrossSources(t *testing.T) {
	a := fakeSource{hits: []Hit{
		{Item: Item{ID: "a1", Kind: KindEpisode}, Score: 0.3},
	}}
	b := fakeSource{hits: []Hit{
		{Item: Item{ID: "b1", Kind: KindEpisode}, Score: 0.9},
	}}

	r := NewRetriever(a, b)
	got, err := r.Search(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2: %+v", len(got), got)
	}
	if got[0].Item.ID != "a1" || got[1].Item.ID != "b1" {
		t.Errorf("got order %q, %q; want a1, b1 (source-local scores tie, then ID asc)", got[0].Item.ID, got[1].Item.ID)
	}
}

func TestRetriever_DeduplicatesByContentHash(t *testing.T) {
	a := fakeSource{hits: []Hit{
		{Item: Item{ID: "a1", Kind: KindEpisode, ContentHash: "h1"}, Score: 0.4},
	}}
	b := fakeSource{hits: []Hit{
		{Item: Item{ID: "b1", Kind: KindEpisode, ContentHash: "h1"}, Score: 0.8},
	}}

	r := NewRetriever(a, b)
	got, err := r.Search(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(got), got)
	}
	if got[0].Item.ID != "a1" {
		t.Errorf("got survivor %q, want a1 (source-local scores tie, then ID asc)", got[0].Item.ID)
	}
	if got[0].Score != 1 {
		t.Errorf("got score %v, want normalized score 1", got[0].Score)
	}
}

func TestRetriever_AppliesKindFilter(t *testing.T) {
	a := fakeSource{hits: []Hit{
		{Item: Item{ID: "pref", Kind: KindUserPreference}, Score: 0.5},
		{Item: Item{ID: "ep", Kind: KindEpisode}, Score: 0.5},
	}}

	r := NewRetriever(a)
	got, err := r.Search(context.Background(), Query{Kinds: []Kind{KindEpisode}})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(got), got)
	}
	if got[0].Item.ID != "ep" {
		t.Errorf("got %q, want ep (only KindEpisode should survive)", got[0].Item.ID)
	}
}

func TestRetriever_AppliesTopKCap(t *testing.T) {
	a := fakeSource{hits: []Hit{
		{Item: Item{ID: "h1", Kind: KindEpisode}, Score: 0.1},
		{Item: Item{ID: "h2", Kind: KindEpisode}, Score: 0.5},
		{Item: Item{ID: "h3", Kind: KindEpisode}, Score: 0.9},
		{Item: Item{ID: "h4", Kind: KindEpisode}, Score: 0.3},
		{Item: Item{ID: "h5", Kind: KindEpisode}, Score: 0.7},
	}}

	r := NewRetriever(a)
	got, err := r.Search(context.Background(), Query{TopK: 2})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2: %+v", len(got), got)
	}
	if got[0].Item.ID != "h3" || got[1].Item.ID != "h5" {
		t.Errorf("got %q, %q; want h3, h5 (top 2 by score)", got[0].Item.ID, got[1].Item.ID)
	}
}

func TestRetriever_DeterministicTieBreak(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	// All four hits share the same score, so every subsequent tie-break
	// level gets exercised:
	//   - h1 (kind "b") sorts after h2/h3/h4 (kind "a")     -> Kind asc
	//   - within kind "a", h3/h4 (t2) sort before h2 (t1)   -> UpdatedAt desc
	//   - within kind "a" and t2, h4 (id "w") sorts before
	//     h3 (id "x")                                       -> ID asc
	h1 := Hit{Item: Item{ID: "z", Kind: Kind("b"), UpdatedAt: t2}, Score: 1.0}
	h2 := Hit{Item: Item{ID: "y", Kind: Kind("a"), UpdatedAt: t1}, Score: 1.0}
	h3 := Hit{Item: Item{ID: "x", Kind: Kind("a"), UpdatedAt: t2}, Score: 1.0}
	h4 := Hit{Item: Item{ID: "w", Kind: Kind("a"), UpdatedAt: t2}, Score: 1.0}

	a := fakeSource{hits: []Hit{h1, h2, h3, h4}}
	r := NewRetriever(a)
	got, err := r.Search(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d hits, want 4: %+v", len(got), got)
	}

	wantOrder := []string{"w", "x", "y", "z"}
	for i, id := range wantOrder {
		if got[i].Item.ID != id {
			gotIDs := make([]string, len(got))
			for j, h := range got {
				gotIDs[j] = h.Item.ID
			}
			t.Fatalf("got order %v, want %v", gotIDs, wantOrder)
		}
	}
}

func TestRetriever_OneSourceErrorsOthersStillReturn(t *testing.T) {
	failing := fakeSource{err: errors.New("boom")}
	ok := fakeSource{hits: []Hit{
		{Item: Item{ID: "ok1", Kind: KindEpisode}, Score: 0.5},
	}}

	r := NewRetriever(failing, ok)
	got, err := r.Search(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Search returned error: %v, want nil (one source still succeeded)", err)
	}
	if len(got) != 1 || got[0].Item.ID != "ok1" {
		t.Fatalf("got %+v, want the successful source's single hit", got)
	}
}

func TestRetriever_AllSourcesError(t *testing.T) {
	a := fakeSource{err: errors.New("boom a")}
	b := fakeSource{err: errors.New("boom b")}

	r := NewRetriever(a, b)
	got, err := r.Search(context.Background(), Query{})
	if err == nil {
		t.Fatal("Search returned nil error, want non-nil (every source errored)")
	}
	if got != nil {
		t.Errorf("got %+v hits, want nil", got)
	}
}
