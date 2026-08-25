package memoryindex

import (
	"context"
	"testing"
)

func TestAgentRunSource_NoActiveRun(t *testing.T) {
	s := AgentRunSource{
		Snapshot: func() (AgentRunSnapshot, bool) {
			return AgentRunSnapshot{}, false
		},
	}

	got, err := s.Search(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v hits, want nil", got)
	}
}

func TestAgentRunSource_ProjectsObjectiveCriteriaEvidence(t *testing.T) {
	snap := AgentRunSnapshot{
		RunID:     "run-1",
		Objective: "Ship the feature",
		Criteria: []AgentCriterionSnapshot{
			{ID: "c1", Text: "Tests pass", Status: "pending"},
			{ID: "c2", Text: "Docs updated", Status: "done"},
		},
		Evidence: []AgentEvidenceSnapshot{
			{Cycle: 1, Kind: "test_run", Source: "go test", Summary: "all green", Success: true},
		},
	}
	s := AgentRunSource{
		Snapshot: func() (AgentRunSnapshot, bool) {
			return snap, true
		},
	}

	got, err := s.Search(context.Background(), Query{})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d hits, want 4: %+v", len(got), got)
	}

	wantKinds := map[Kind]int{
		KindAgentObjective: 1,
		KindAgentCriterion: 2,
		KindAgentEvidence:  1,
	}
	gotKinds := map[Kind]int{}
	for _, h := range got {
		gotKinds[h.Item.Kind]++
		if h.Item.Trust != TrustControllerObserved {
			t.Errorf("hit %+v has Trust %q, want %q", h, h.Item.Trust, TrustControllerObserved)
		}
	}
	for k, want := range wantKinds {
		if gotKinds[k] != want {
			t.Errorf("got %d hits of kind %q, want %d", gotKinds[k], k, want)
		}
	}
}

func TestAgentRunSource_RunIDMismatchReturnsNoHits(t *testing.T) {
	s := AgentRunSource{
		Snapshot: func() (AgentRunSnapshot, bool) {
			return AgentRunSnapshot{RunID: "run-1", Objective: "Ship it"}, true
		},
	}

	got, err := s.Search(context.Background(), Query{RunID: "run-2"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v hits, want nil", got)
	}
}

func TestEpisodeSource_AlwaysEmpty(t *testing.T) {
	var s EpisodeSource

	got, err := s.Search(context.Background(), Query{Text: "anything"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if got != nil {
		t.Errorf("got %+v hits, want nil", got)
	}
}
