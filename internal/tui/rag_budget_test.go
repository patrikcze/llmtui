package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/memoryindex"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/rag"
)

func TestRAGSourceTokenCapOnlyTightens(t *testing.T) {
	tests := []struct {
		name          string
		source, ragMx int
		want          int
	}{
		{"rag setting lower tightens the cap", 768, 200, 200},
		{"default rag setting never raises the cap", 768, 3000, 768},
		{"equal", 768, 768, 768},
		{"unset rag setting keeps the tier cap", 768, 0, 768},
		{"negative rag setting is ignored", 768, -5, 768},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ragSourceTokenCap(tt.source, tt.ragMx); got != tt.want {
				t.Errorf("ragSourceTokenCap(%d, %d) = %d, want %d", tt.source, tt.ragMx, got, tt.want)
			}
		})
	}
}

func TestMemoryRetrievalPolicyAppliesRAGCapToSourceTier(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Memory.Retrieval.SourceTokens = 768
	for _, tt := range []struct{ rag, want int }{{200, 200}, {3000, 768}, {0, 768}} {
		m.cfg.RAG.Retrieval.MaxContextTokens = tt.rag
		if got := m.memoryRetrievalPolicy().SourceTokens; got != tt.want {
			t.Errorf("rag.max_context_tokens=%d: SourceTokens = %d, want %d", tt.rag, got, tt.want)
		}
	}
}

// bigChunks returns n distinct ~1.2k-token chunks that all match "alpha".
func bigChunks(n int) []rag.DocumentChunk {
	var chunks []rag.DocumentChunk
	for i := 0; i < n; i++ {
		chunks = append(chunks, rag.DocumentChunk{
			ID: fmt.Sprintf("f%d.go#1-40", i), Path: fmt.Sprintf("f%d.go", i), StartLine: 1, EndLine: 40,
			Text: "alpha " + strings.Repeat("filler words here ", 270),
		})
	}
	return chunks
}

func sourceRecords(base compositionBase) int {
	n := 0
	for _, r := range base.input.ActiveContext {
		if r.Kind == "source_chunk" {
			n++
		}
	}
	return n
}

func TestRAGCapLimitsActiveContextSourceChunks(t *testing.T) {
	m := newTestModel(t)
	m.ragOn = true
	m.ragIndex = rag.NewIndex(bigChunks(4))
	m.cfg.Memory.Retrieval.SourceTokens = 4000
	m.cfg.Memory.Retrieval.MaxContextTokens = 8000
	m.cfg.RAG.Retrieval.MaxContextTokens = 3000

	if got := sourceRecords(m.compositionBase("alpha", nil, false)); got < 2 {
		t.Fatalf("setup: expected several chunks under a generous cap, got %d", got)
	}
	m.cfg.RAG.Retrieval.MaxContextTokens = 1500 // room for exactly one ~1.2k chunk
	base := m.compositionBase("alpha", nil, false)
	if got := sourceRecords(base); got != 1 {
		t.Fatalf("rag.max_context_tokens=1500 kept %d source chunks, want 1", got)
	}
}

func TestPrepareShedsRAGBeforeRejectingActiveContextRequest(t *testing.T) {
	m := newTestModel(t)
	m.ragOn = true
	m.ragIndex = rag.NewIndex(bigChunks(4))
	m.cfg.Memory.Retrieval.SourceTokens = 6000
	m.cfg.Memory.Retrieval.MaxContextTokens = 8000
	m.cfg.RAG.Retrieval.MaxContextTokens = 6000
	m.cfg.Context.ReserveResponseTokens = 512

	m.cfg.Context.MaxContextTokens = 20000
	full, err := m.prepareRequest("alpha", nil, false)
	if err != nil {
		t.Fatalf("roomy window: %v", err)
	}
	fullChunks := len(full.memoryHits)
	if fullChunks < 3 {
		t.Fatalf("setup: expected >=3 retrieved chunks, got %d", fullChunks)
	}

	// A window that fits the prompt only without most of the retrieval.
	m.cfg.Context.MaxContextTokens = 3500
	shed, err := m.prepareRequest("alpha", nil, false)
	if err != nil {
		t.Fatalf("retrieval should be shed to fit, not rejected: %v", err)
	}
	if len(shed.memoryHits) >= fullChunks {
		t.Errorf("hits = %d, want fewer than %d after shedding", len(shed.memoryHits), fullChunks)
	}
	if len(shed.ragResults) != len(shed.memoryHits) {
		t.Errorf("ragResults (%d) and memoryHits (%d) disagree after shedding", len(shed.ragResults), len(shed.memoryHits))
	}
	if shed.estimate.Total+shed.estimate.Reserve > shed.estimate.Window {
		t.Errorf("estimate %d + reserve %d exceeds window %d", shed.estimate.Total, shed.estimate.Reserve, shed.estimate.Window)
	}
	for _, s := range shed.composed.Sections {
		if s.Title == "Active Context" {
			for i := range full.composed.Sections {
				if full.composed.Sections[i].Title == "Active Context" && len(s.Content) >= len(full.composed.Sections[i].Content) {
					t.Error("Active Context section did not shrink")
				}
			}
		}
	}
	// The top-ranked chunk is the one retained.
	if len(shed.memoryHits) > 0 && shed.memoryHits[0].Item.ID != full.memoryHits[0].Item.ID {
		t.Errorf("kept %q, want the top-ranked %q", shed.memoryHits[0].Item.ID, full.memoryHits[0].Item.ID)
	}
}

func TestPrepareStillRejectsWhenRAGIsNotTheProblem(t *testing.T) {
	m := newTestModel(t)
	m.ragOn = true
	m.ragIndex = rag.NewIndex(bigChunks(2))
	m.cfg.Context.MaxContextTokens = 100 // below even the bare system prompt
	m.cfg.Context.ReserveResponseTokens = 50
	if _, err := m.prepareRequest("alpha", nil, false); err == nil || !strings.Contains(err.Error(), "request overhead is too large") {
		t.Fatalf("err = %v, want the overhead error once retrieval is exhausted", err)
	}
}

func TestPrepareShedsLegacyRetrievedContext(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Memory.Retrieval.Enabled = false // legacy RetrievedContext path
	m.ragOn = true
	m.ragIndex = rag.NewIndex(bigChunks(4))
	m.cfg.RAG.Retrieval.MaxContextTokens = 6000
	m.cfg.Context.ReserveResponseTokens = 512

	m.cfg.Context.MaxContextTokens = 20000
	full, err := m.prepareRequest("alpha", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	m.cfg.Context.MaxContextTokens = 3500
	shed, err := m.prepareRequest("alpha", nil, false)
	if err != nil {
		t.Fatalf("legacy retrieval should be shed to fit: %v", err)
	}
	if len(shed.ragResults) >= len(full.ragResults) {
		t.Errorf("ragResults = %d, want fewer than %d", len(shed.ragResults), len(full.ragResults))
	}
}

func TestShedOptionalRetrievalLeavesMemoryAndInputUntouched(t *testing.T) {
	base := compositionBase{memoryHits: []memoryindex.Hit{
		{Item: memoryindex.Item{ID: "u1", Kind: memoryindex.KindUserPreference}},
		{Item: memoryindex.Item{ID: "s1", Kind: memoryindex.KindSourceChunk}},
	}}
	base.input.UseActiveContext = true
	base.input.ActiveContext = nil
	original := base.memoryHits
	if shedOptionalRetrieval(&base, 0) {
		t.Fatal("nothing to shed, but reported a removal")
	}
	if len(original) != len(base.memoryHits) {
		t.Error("memory hits were modified with no source chunks in the prompt")
	}
}

type staticSource []memoryindex.Hit

func (s staticSource) Search(context.Context, memoryindex.Query) ([]memoryindex.Hit, error) {
	return s, nil
}

// TestActiveContextEstimateTracksRenderedPrompt guards the estimate against
// drifting from the record format prompt.formatActiveContext really renders.
// It measures each record's marginal size in a composed prompt and checks the
// retriever's per-hit token estimate is never below it and stays close.
func TestActiveContextEstimateTracksRenderedPrompt(t *testing.T) {
	updated := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	hits := []memoryindex.Hit{
		{Item: memoryindex.Item{
			ID: "pref-1", Kind: memoryindex.KindUserPreference, Scope: memoryindex.ScopeUser,
			Text: "Prefer concise Go code with comments.", Trust: memoryindex.TrustUserAuthored, UpdatedAt: updated,
		}, Score: 1},
		{Item: memoryindex.Item{
			ID: "internal/tui/pipeline.go#41-80", Kind: memoryindex.KindSourceChunk, Scope: memoryindex.ScopeProject,
			Text: strings.Repeat("func compose() {}\n", 40), Trust: memoryindex.TrustWorkspaceUntrusted,
			Source: memoryindex.SourceRef{Path: "internal/tui/pipeline.go", StartLine: 41, EndLine: 80}, UpdatedAt: updated,
		}, Score: .9},
		{Item: memoryindex.Item{
			ID: "dec-1", Kind: memoryindex.KindProjectDecision, Scope: memoryindex.ScopeProject, ProjectID: "0123456789abcdef0123",
			Text: "Use PostgreSQL for durable storage.", Trust: memoryindex.TrustUserAuthored, UpdatedAt: updated,
		}, Score: .8},
	}
	result, err := memoryindex.NewRetriever(staticSource(hits)).SearchDetailed(context.Background(), memoryindex.Query{ProjectID: "0123456789abcdef0123"}, memoryindex.RetrievalPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != len(hits) {
		t.Fatalf("retriever kept %d of %d hits", len(result.Hits), len(hits))
	}

	activeBytes := func(hs []memoryindex.Hit) int {
		out := prompt.Compose(prompt.Input{UseActiveContext: true, ActiveContext: activeContextRecords(hs)})
		for _, s := range out.Sections {
			if s.Title == "Active Context" {
				return len(s.Content)
			}
		}
		t.Fatal("no Active Context section rendered")
		return 0
	}
	// Marginal rendered size of record i is measured against the prompt
	// holding records 0..i-1, which cancels the fixed wrapper and preamble.
	for i := 1; i < len(result.Hits); i++ {
		hit := result.Hits[i]
		rendered := activeBytes(result.Hits[:i+1]) - activeBytes(result.Hits[:i])
		want := (rendered + 3) / 4
		if hit.Tokens < want {
			t.Errorf("%s: estimate %d tokens < rendered %d tokens: framing undercounted", hit.Item.ID, hit.Tokens, want)
		}
		if hit.Tokens > want+8 {
			t.Errorf("%s: estimate %d tokens far above rendered %d tokens", hit.Item.ID, hit.Tokens, want)
		}
	}
}
