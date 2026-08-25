package tui

import (
	"testing"

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
