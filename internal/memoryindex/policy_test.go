package memoryindex

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestRetrieverDetailedAppliesBoostsScopeAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	source := fakeSource{hits: []Hit{
		{Item: Item{ID: "project", Kind: KindProjectDecision, ProjectID: "p1", Trust: TrustUserAuthored}, Score: 0.5},
		{Item: Item{ID: "wrong", Kind: KindProjectDecision, ProjectID: "p2"}, Score: 0.9},
		{Item: Item{ID: "expired", Kind: KindEpisode, ProjectID: "p1", ExpiresAt: &expired}, Score: 1},
	}}
	result, err := NewRetriever(source).SearchDetailed(context.Background(), Query{
		Text: "project", ProjectID: "p1", Now: now,
	}, RetrievalPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Hits[0].Item.ID != "project" {
		t.Fatalf("selected hits = %+v", result.Hits)
	}
	hit := result.Hits[0]
	if hit.BaseScore != 0.5 || math.Abs(hit.Score-0.8) > 1e-9 {
		t.Fatalf("ranked hit = base %.3f final %.3f components %+v", hit.BaseScore, hit.Score, hit.Components)
	}
	wantRejected := map[string]bool{"wrong_project": false, "expired": false}
	for _, rejected := range result.Rejected {
		if _, ok := wantRejected[rejected.Reason]; ok {
			wantRejected[rejected.Reason] = true
		}
	}
	for reason, found := range wantRejected {
		if !found {
			t.Errorf("missing rejection %q: %+v", reason, result.Rejected)
		}
	}
}

func TestRetrieverDetailedDiversifiesKindsAndOverlappingChunks(t *testing.T) {
	source := fakeSource{hits: []Hit{
		{Item: Item{ID: "u1", Kind: KindUserPreference}, Score: 1},
		{Item: Item{ID: "u2", Kind: KindUserPreference}, Score: .9},
		{Item: Item{ID: "c1", Kind: KindSourceChunk, Source: SourceRef{Path: "a.go", StartLine: 1, EndLine: 20}}, Score: .8},
		{Item: Item{ID: "c2", Kind: KindSourceChunk, Source: SourceRef{Path: "a.go", StartLine: 15, EndLine: 30}}, Score: .7},
	}}
	result, err := NewRetriever(source).SearchDetailed(context.Background(), Query{}, RetrievalPolicy{
		KindCaps: map[Kind]int{KindUserPreference: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 2 || result.Hits[0].Item.ID != "u1" || result.Hits[1].Item.ID != "c1" {
		t.Fatalf("diverse hits = %+v", result.Hits)
	}
	want := map[string]bool{"kind_cap": false, "overlapping_source_chunk": false}
	for _, rejected := range result.Rejected {
		if _, ok := want[rejected.Reason]; ok {
			want[rejected.Reason] = true
		}
	}
	for reason, found := range want {
		if !found {
			t.Errorf("missing rejection %q: %+v", reason, result.Rejected)
		}
	}
}

func TestRetrieverDetailedEnforcesTotalBudgetAndReassignsSoftCaps(t *testing.T) {
	source := fakeSource{hits: []Hit{
		{Item: Item{ID: "p1", Kind: KindProjectDecision, Scope: ScopeProject, Text: "short project decision"}, Score: 1},
		{Item: Item{ID: "p2", Kind: KindProjectDecision, Scope: ScopeProject, Text: "second project decision"}, Score: .9},
		{Item: Item{ID: "p3", Kind: KindProjectDecision, Scope: ScopeProject, Text: "third project decision"}, Score: .8},
	}}
	result, err := NewRetriever(source).SearchDetailed(context.Background(), Query{}, RetrievalPolicy{
		MaxTokens: 50, ProjectTokens: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("soft-cap reassignment selected %d hits, want 2: %+v", len(result.Hits), result)
	}
	if result.TotalTokens > 50 {
		t.Fatalf("total tokens = %d, exceeds 50", result.TotalTokens)
	}
	if len(result.Rejected) != 1 || result.Rejected[0].Reason != "tier_budget" {
		t.Fatalf("budget rejections = %+v", result.Rejected)
	}
}
