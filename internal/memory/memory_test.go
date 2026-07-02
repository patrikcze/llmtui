package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	return NewStore(filepath.Join(t.TempDir(), "memory.yaml"), 100)
}

func TestAddListRemove(t *testing.T) {
	s := newTestStore(t)

	sn, err := s.Add("Prefer Go examples using Cobra and Viper.")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if sn.ID == "" || sn.CreatedAt.IsZero() {
		t.Errorf("snippet missing metadata: %+v", sn)
	}

	list, err := s.Load()
	if err != nil || len(list) != 1 {
		t.Fatalf("Load = (%v, %v), want 1 snippet", list, err)
	}

	if err := s.Remove(sn.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	list, _ = s.Load()
	if len(list) != 0 {
		t.Error("snippet not removed")
	}

	if err := s.Remove("nonexistent"); err == nil {
		t.Error("removing unknown id should error")
	}
}

func TestAddEmptyRejected(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("   "); err == nil {
		t.Error("empty snippet should be rejected")
	}
}

func TestMaxSnippetsEnforced(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "m.yaml"), 3)
	for i := 0; i < 5; i++ {
		if _, err := s.Add(strings.Repeat("x", i+1)); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := s.Load()
	if len(list) != 3 {
		t.Fatalf("len = %d, want max 3", len(list))
	}
	// Oldest evicted: shortest entries added first.
	for _, sn := range list {
		if len(sn.Text) < 3 {
			t.Errorf("oldest snippet %q should have been evicted", sn.Text)
		}
	}
}

func TestRelevantSelection(t *testing.T) {
	snippets := []Snippet{
		{ID: "1", Text: "Prefer Go examples using Cobra and Viper"},
		{ID: "2", Text: "Answer PowerShell questions with modern syntax"},
		{ID: "3", Text: "Prefer concise answers"},
	}
	got := Relevant(snippets, "show me a cobra command in go", 2)
	if len(got) == 0 || got[0].ID != "1" {
		t.Errorf("Relevant = %+v, want cobra/go snippet first", got)
	}
	for _, sn := range got {
		if sn.ID == "2" {
			t.Error("PowerShell snippet should not match a Go query")
		}
	}

	if got := Relevant(snippets, "unrelated quantum physics", 2); len(got) != 0 {
		t.Errorf("no-overlap query returned %+v", got)
	}
	if got := Relevant(nil, "anything", 2); got != nil {
		t.Error("empty store should return nil")
	}
}

func TestClear(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Add("something"); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	list, _ := s.Load()
	if len(list) != 0 {
		t.Error("Clear should remove all snippets")
	}
	// Clearing an already-empty store is fine.
	if err := s.Clear(); err != nil {
		t.Errorf("second Clear: %v", err)
	}
}

func TestRemoveByPrefix(t *testing.T) {
	s := newTestStore(t)
	sn, _ := s.Add("prefix removal test")
	if err := s.Remove(sn.ID[:4]); err != nil {
		t.Errorf("Remove by 4-char prefix: %v", err)
	}
}
