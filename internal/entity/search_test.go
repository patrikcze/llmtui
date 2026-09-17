package entity

import (
	"strings"
	"testing"
	"time"
)

func TestSearchFindsOlderEntitiesWithoutExpanding(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRegistry(Limits{Now: func() time.Time { return now }})
	candidate := testCandidate("body with migration details beyond the preview")
	candidate.Label = "Release notes"
	candidate.Preview = "brief preview"
	candidate.Metadata.Path = "docs/upgrade.md"
	candidate.Provenance.Reference = "private provenance marker"
	want, err := r.Put(candidate)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	unrelated := testCandidate("unrelated")
	unrelated.Label = "other.txt"
	if _, err := r.Put(unrelated); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"RELEASE notes", "migration", "upgrade.md"} {
		t.Run(query, func(t *testing.T) {
			views, total := r.Search(query, 8)
			if total != 1 || len(views) != 1 || views[0].ID != want.ID {
				t.Fatalf("Search = %+v, total=%d", views, total)
			}
			if views[0].Payload != "" || views[0].Level != LevelMinimal {
				t.Fatal("lookup expanded a payload")
			}
		})
	}
	if _, total := r.Search("private provenance marker", 8); total != 0 {
		t.Fatal("lookup searched private provenance")
	}
	if r.Stats().ExpandedThisReq != 0 {
		t.Fatal("lookup consumed full expansion budget")
	}
}

func TestSearchAmbiguityLimitsAndLifecycle(t *testing.T) {
	now := time.Unix(100, 0)
	r := NewRegistry(Limits{Now: func() time.Time { return now }})
	for range 10 {
		if _, err := r.Put(testCandidate("shared topic")); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		views, total := r.Search("SHARED", 8)
		if total != 10 || len(views) != 8 || views[0].ID != "ent_00010" {
			t.Fatalf("ambiguous search = %+v, total=%d", views, total)
		}
	}
	expired := testCandidate("expired-only")
	expired.ExpiresAt = now.Add(-time.Second)
	if _, err := r.Put(expired); err != nil {
		t.Fatal(err)
	}
	redacted := testCandidate("hidden-only")
	redacted.Preview = "redacted"
	redacted.Redacted = true
	if _, err := r.Put(redacted); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"expired", "hidden", "missing", "", "!!!"} {
		if views, total := r.Search(query, 8); len(views) != 0 || total != 0 {
			t.Fatalf("Search(%q) returned unavailable data: %+v", query, views)
		}
	}
	r.Reset()
	if _, total := r.Search("shared", 8); total != 0 {
		t.Fatal("reset entities remained discoverable")
	}
	r = NewRegistry(Limits{MaxEntities: 1})
	if _, err := r.Put(testCandidate("evicted")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Put(testCandidate(strings.Repeat("new", 10))); err != nil {
		t.Fatal(err)
	}
	if _, total := r.Search("evicted", 8); total != 0 {
		t.Fatal("evicted data remained discoverable")
	}
}

func TestSearchKindFilterNeverSubstitutesAnotherEvidenceClass(t *testing.T) {
	r := NewRegistry(Limits{})
	for _, candidate := range []Candidate{
		{
			Kind: KindWebResult, Provenance: Provenance{Source: "web"}, Label: "iPhone Duo Czech price",
			Trust: TrustWebUntrusted, Scope: ScopeSession, Payload: "web price result",
		},
		{
			Kind: KindVisionObservation, Provenance: Provenance{Source: "user_provided_image"}, Label: "user screenshot",
			Trust: TrustVisionModelDerived, Scope: ScopeSession, Payload: `{"summary":"iPhone Duo Czech prices"}`,
		},
	} {
		if _, err := r.Put(candidate); err != nil {
			t.Fatal(err)
		}
	}

	views, total := r.SearchWithOptions(SearchOptions{
		Query: "iPhone Duo Czech price screenshot",
		Kinds: []Kind{KindVisionObservation},
		Limit: 8,
	})
	if total != 1 || len(views) != 1 || views[0].Kind != KindVisionObservation {
		t.Fatalf("filtered search = %+v, total=%d", views, total)
	}
}

func TestSearchRejectsWeakLongQueryMatch(t *testing.T) {
	r := NewRegistry(Limits{})
	if _, err := r.Put(Candidate{
		Kind: KindWebResult, Provenance: Provenance{Source: "web"}, Label: "unrelated page",
		Trust: TrustWebUntrusted, Scope: ScopeSession, Payload: "screenshot",
	}); err != nil {
		t.Fatal(err)
	}
	if views, total := r.Search("iPhone Duo Czech price screenshot", 8); total != 0 || len(views) != 0 {
		t.Fatalf("weak long-query match = %+v, total=%d", views, total)
	}
}
