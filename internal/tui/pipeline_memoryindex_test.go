package tui

import (
	"reflect"
	"testing"

	"github.com/patrikcze/llmtui/internal/memory"
	"github.com/patrikcze/llmtui/internal/memoryindex"
	"github.com/patrikcze/llmtui/internal/rag"
)

// TestCompositionBase_DebugHitsIncludeUserAndRAGKinds verifies Task 4's new
// debug-visible ranked hit list: with both user memory and RAG populated,
// compositionBase's merged memoryHits (threaded into debugInfo.MemoryHits for
// /debug last) contains at least one hit of each kind.
func TestCompositionBase_DebugHitsIncludeUserAndRAGKinds(t *testing.T) {
	m := newTestModel(t)
	m.memEnabled = true
	if _, err := m.memStore.Add("Prefer concise Go code with comments."); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	m.ragOn = true
	m.ragIndex = rag.NewIndex([]rag.DocumentChunk{
		{ID: "concise.go#1-1", Path: "concise.go", StartLine: 1, EndLine: 1, Text: "func concise() {}"},
	})

	base := m.compositionBase("give me concise Go code", nil, false)

	var sawUserPreference, sawSourceChunk bool
	for _, h := range base.memoryHits {
		switch h.Item.Kind {
		case memoryindex.KindUserPreference:
			sawUserPreference = true
		case memoryindex.KindSourceChunk:
			sawSourceChunk = true
		}
	}
	if !sawUserPreference {
		t.Error("memoryHits has no KindUserPreference entry with both memory and RAG populated")
	}
	if !sawSourceChunk {
		t.Error("memoryHits has no KindSourceChunk entry with both memory and RAG populated")
	}
}

// TestCompositionBase_MemoryDisabledProducesNoUserPreferenceHits mirrors the
// existing disabled-by-default guard (m.memEnabled == false): the unified
// Retriever must not produce any KindUserPreference hits, matching today's
// "if m.memEnabled && m.memStore != nil" guard exactly.
func TestCompositionBase_MemoryDisabledProducesNoUserPreferenceHits(t *testing.T) {
	m := newTestModel(t)
	// m.memEnabled defaults to false in newTestModel (cfg.Memory.Enabled is
	// unset). Seed the store anyway so a leak would be caught even though
	// memory is disabled at the guard.
	if _, err := m.memStore.Add("Prefer concise Go code with comments."); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	m.ragOn = true
	m.ragIndex = rag.NewIndex([]rag.DocumentChunk{
		{ID: "concise.go#1-1", Path: "concise.go", StartLine: 1, EndLine: 1, Text: "func concise() {}"},
	})

	if m.memEnabled {
		t.Fatal("test setup: memEnabled must be false")
	}

	base := m.compositionBase("give me concise Go code", nil, false)

	for _, h := range base.memoryHits {
		if h.Item.Kind == memoryindex.KindUserPreference {
			t.Errorf("memoryHits contains a KindUserPreference hit while memory is disabled: %+v", h)
		}
	}
	if len(base.input.MemorySnippets) != 0 {
		t.Errorf("MemorySnippets = %v, want empty while memory is disabled", base.input.MemorySnippets)
	}
}

// TestCompositionBase_MemorySnippetsSurviveContentHashDedup is the Finding 1
// regression test from the final whole-branch review: two memory snippets
// with identical (thus identical-ContentHash) text, both ranked into
// memory.Relevant's top-3 against a fixed query, plus a third, differently
// worded, lower-ranked snippet that also scores > 0. Retriever.Search
// dedupes merged hits by ContentHash, so if MemorySnippets were derived from
// the merged/deduped Retriever output (as it was before the fix), one of the
// two duplicate-text snippets would be silently dropped, breaking Global
// Constraint 2 (MemorySnippets must equal memory.Relevant's output exactly,
// in order, length included). MemorySnippets must instead come from a direct,
// independent call to memory.Relevant, exactly like the pre-refactor code and
// exactly like RAG's SearchRaw bypass just below in the same function.
func TestCompositionBase_MemorySnippetsSurviveContentHashDedup(t *testing.T) {
	m := newTestModel(t)
	m.memEnabled = true

	dup := "Always write concise Go code with clear comments."
	other := "Add tests for every function."

	if _, err := m.memStore.Add(dup); err != nil {
		t.Fatalf("seed memory (dup 1): %v", err)
	}
	if _, err := m.memStore.Add(dup); err != nil {
		t.Fatalf("seed memory (dup 2): %v", err)
	}
	if _, err := m.memStore.Add(other); err != nil {
		t.Fatalf("seed memory (other): %v", err)
	}

	raw := "Prefer concise Go code with comments and tests for every function"

	loaded, err := m.memStore.Load()
	if err != nil {
		t.Fatalf("load memory: %v", err)
	}
	want := memory.Relevant(loaded, raw, 3)
	if len(want) != 3 {
		t.Fatalf("test setup: memory.Relevant returned %d snippets, want 3 (dup text %q must outrank/tie %q, "+
			"and both must outrank %q) — adjust raw/snippet wording", len(want), dup, dup, other)
	}

	base := m.compositionBase(raw, nil, false)

	if len(base.input.MemorySnippets) != len(want) {
		t.Fatalf("MemorySnippets len = %d, want %d (duplicate ContentHash must not be silently deduped from "+
			"MemorySnippets); got %v", len(base.input.MemorySnippets), len(want), base.input.MemorySnippets)
	}
	for i, sn := range want {
		if base.input.MemorySnippets[i] != sn.Text {
			t.Errorf("MemorySnippets[%d] = %q, want %q", i, base.input.MemorySnippets[i], sn.Text)
		}
	}
}

// TestCompositionBase_MemoryAndRAGBothCorrectWhenCombined is the Finding 2
// regression test: with both a relevant memory store AND a populated RAG
// index active in the same compositionBase call (so both sources are
// registered into one Retriever and merged/deduped/sorted together for the
// debug hit list), both legacy outputs must independently remain correct:
// MemorySnippets must equal memory.Relevant's own output, and ragResults
// must equal Index.Search's own output — neither derived from the merged
// Retriever hits. No prior test exercised both sources active at once,
// which is exactly how Finding 1 escaped per-task review.
func TestCompositionBase_MemoryAndRAGBothCorrectWhenCombined(t *testing.T) {
	m := newTestModel(t)
	m.memEnabled = true
	if _, err := m.memStore.Add("Prefer concise Go code with comments."); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	if _, err := m.memStore.Add("Write clear commit messages."); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	m.ragOn = true
	m.ragIndex = rag.NewIndex([]rag.DocumentChunk{
		{ID: "concise.go#1-1", Path: "concise.go", StartLine: 1, EndLine: 1, Text: "func concise() {}"},
		{ID: "other.go#1-1", Path: "other.go", StartLine: 1, EndLine: 1, Text: "func other() {}"},
	})

	raw := "give me concise Go code"

	loaded, err := m.memStore.Load()
	if err != nil {
		t.Fatalf("load memory: %v", err)
	}
	wantSnippets := memory.Relevant(loaded, raw, 3)
	wantRAG := m.ragIndex.Search(raw, m.ragTopK())

	if len(wantSnippets) == 0 {
		t.Fatal("test setup: expected at least one relevant memory snippet")
	}
	if len(wantRAG) == 0 {
		t.Fatal("test setup: expected at least one RAG result")
	}

	base := m.compositionBase(raw, nil, false)

	if len(base.input.MemorySnippets) != len(wantSnippets) {
		t.Fatalf("MemorySnippets len = %d, want %d: got %v", len(base.input.MemorySnippets), len(wantSnippets), base.input.MemorySnippets)
	}
	for i, sn := range wantSnippets {
		if base.input.MemorySnippets[i] != sn.Text {
			t.Errorf("MemorySnippets[%d] = %q, want %q", i, base.input.MemorySnippets[i], sn.Text)
		}
	}

	if !reflect.DeepEqual(base.ragResults, wantRAG) {
		t.Errorf("ragResults = %+v, want %+v", base.ragResults, wantRAG)
	}
}
