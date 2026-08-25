package memoryindex

import (
	"context"
	"os"
	"testing"
)

func TestProjectSourceRanksApprovedRecords(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	architecture, err := store.Add(
		KindProjectArchitecture,
		"The Go service uses hexagonal architecture and explicit ports.",
		"go",
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.Add(
		KindProjectDecision,
		"Use PostgreSQL for durable storage.",
		"database",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(KindProjectConvention, "Commit messages use Conventional Commits."); err != nil {
		t.Fatal(err)
	}

	source := ProjectSource{Store: store, TopK: 10}
	hits, err := source.Search(context.Background(), Query{
		Text:      "How does the Go architecture store durable data in PostgreSQL?",
		ProjectID: store.ProjectID(),
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %+v, want architecture and decision", hits)
	}
	if hits[0].Item.ID != architecture.ID || hits[1].Item.ID != decision.ID {
		t.Fatalf("hit order = [%s %s], want [%s %s]", hits[0].Item.ID, hits[1].Item.ID, architecture.ID, decision.ID)
	}
	for _, hit := range hits {
		if hit.Score <= 0 || hit.Score > 1 {
			t.Errorf("score = %f, want (0,1]", hit.Score)
		}
		if hit.Item.Scope != ScopeProject || hit.Item.ProjectID != store.ProjectID() {
			t.Errorf("hit scope = %+v", hit.Item)
		}
		if len(hit.MatchedTerms) == 0 || hit.Why == "" {
			t.Errorf("hit lacks explanation: %+v", hit)
		}
	}
}

func TestProjectSourceIsolatesProjectQueries(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	if _, err := store.Add(KindProjectDecision, "Use PostgreSQL for storage."); err != nil {
		t.Fatal(err)
	}
	source := ProjectSource{Store: store}

	tests := []struct {
		name      string
		projectID string
	}{
		{name: "missing project id"},
		{name: "different project id", projectID: "different-workspace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hits, err := source.Search(context.Background(), Query{
				Text:      "PostgreSQL storage",
				ProjectID: test.projectID,
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(hits) != 0 {
				t.Fatalf("cross-project hits = %+v", hits)
			}
		})
	}
}

func TestProjectSourceExcludesUnapprovedProposals(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	record, err := store.Propose(KindProjectDecision, "Use Badger for the local index.")
	if err != nil {
		t.Fatal(err)
	}
	source := ProjectSource{Store: store}
	query := Query{Text: "Badger local index", ProjectID: store.ProjectID()}

	hits, err := source.Search(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("unapproved proposal was searchable: %+v", hits)
	}
	if err := store.Approve(record.ID); err != nil {
		t.Fatal(err)
	}
	hits, err = source.Search(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Item.ID != record.ID {
		t.Fatalf("approved proposal hits = %+v", hits)
	}
	if hits[0].Item.Trust != TrustModelProposed {
		t.Fatalf("approved proposal lost provenance: %+v", hits[0].Item)
	}
}

func TestProjectSourceHonorsKindAndTopK(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	if _, err := store.Add(KindProjectArchitecture, "Go packages follow a layered architecture."); err != nil {
		t.Fatal(err)
	}
	decision, err := store.Add(KindProjectDecision, "Go errors are wrapped with context.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(KindProjectConvention, "Go files use gofmt."); err != nil {
		t.Fatal(err)
	}

	source := ProjectSource{Store: store, TopK: 1}
	hits, err := source.Search(context.Background(), Query{
		Text:      "Go architecture errors gofmt",
		ProjectID: store.ProjectID(),
		Kinds:     []Kind{KindProjectDecision},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Item.ID != decision.ID {
		t.Fatalf("filtered hits = %+v", hits)
	}
}

func TestProjectSourceHonorsCanceledContext(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (ProjectSource{Store: store}).Search(ctx, Query{
		Text:      "anything",
		ProjectID: store.ProjectID(),
	})
	if err != context.Canceled {
		t.Fatalf("Search error = %v, want context.Canceled", err)
	}
}

func TestProjectSourcePropagatesCorruptStore(t *testing.T) {
	store := newTestProjectStore(t, t.TempDir())
	if err := os.MkdirAll(store.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (ProjectSource{Store: store}).Search(context.Background(), Query{
		Text:      "anything",
		ProjectID: store.ProjectID(),
	})
	if err == nil {
		t.Fatal("Search succeeded with corrupt store")
	}
}
