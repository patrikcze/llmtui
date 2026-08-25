package memoryindex

import (
	"context"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/provider"
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

func TestEpisodeSourceReturnsOnlyPriorProjectScopedSummaries(t *testing.T) {
	dir := t.TempDir()
	saveEpisode := func(name, projectID, goal, outcome string) {
		t.Helper()
		session := history.Session{
			Provider:  "mock",
			Model:     "model",
			ProjectID: projectID,
			Messages: []provider.Message{
				{Role: provider.RoleUser, Content: goal},
				{Role: provider.RoleAssistant, Content: outcome, Reasoning: "private reasoning"},
			},
		}
		session.Episode = history.BuildEpisode(session)
		if _, err := history.Save(dir, name, session); err != nil {
			t.Fatal(err)
		}
	}
	saveEpisode("prior", "project-a", "Add session memory", "Session memory tests pass")
	saveEpisode("current", "project-a", "Current session memory", "Still working")
	saveEpisode("other-project", "project-b", "Add session memory", "Must stay isolated")
	if _, err := history.Save(dir, "legacy", history.Session{ProjectID: "project-a"}); err != nil {
		t.Fatal(err)
	}

	s := EpisodeSource{Dir: dir, TopK: 5}
	got, err := s.Search(context.Background(), Query{
		Text:      "session memory tests",
		ProjectID: "project-a",
		SessionID: "current",
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want one prior project episode", got)
	}
	hit := got[0]
	if hit.Item.ID != "prior" || hit.Item.Kind != KindEpisode || hit.Item.Scope != ScopeSession {
		t.Fatalf("episode hit = %+v", hit)
	}
	if hit.Item.Source.SessionID != "prior" || hit.Item.ProjectID != "project-a" {
		t.Fatalf("episode provenance = %+v", hit.Item)
	}
	if strings.Contains(hit.Item.Text, "private reasoning") || strings.Contains(hit.Item.Text, "other-project") {
		t.Fatalf("episode hit leaked excluded content: %q", hit.Item.Text)
	}
	if !strings.Contains(hit.Item.Text, "Goal: Add session memory") || !strings.Contains(hit.Item.Text, "Outcome: Session memory tests pass") {
		t.Fatalf("episode summary missing visible boundaries: %q", hit.Item.Text)
	}
	if hit.Item.Trust != TrustModelProposed {
		t.Fatalf("episode trust = %q", hit.Item.Trust)
	}
}

func TestEpisodeSourceRequiresExactProjectScope(t *testing.T) {
	s := EpisodeSource{Dir: t.TempDir()}
	got, err := s.Search(context.Background(), Query{Text: "anything"})
	if err != nil || got != nil {
		t.Fatalf("unscoped episode search = (%+v, %v), want nil", got, err)
	}
}
