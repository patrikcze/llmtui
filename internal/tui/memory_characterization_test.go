package tui

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agentverify"
	"github.com/patrikcze/llmtui/internal/cache"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/memory"
	"github.com/patrikcze/llmtui/internal/memoryindex"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/rag"
)

// These tests pin the CURRENT (pre-refactor) behavior of compositionBase's
// independent memory and RAG selection, ahead of the unified
// internal/memoryindex.Retriever refactor. They are characterization tests:
// they describe what the system does today, not what it should do, so a
// later task can diff its new behavior against this baseline.

// TestCharacterization_UserMemorySelectsTop3ByKeywordOverlap pins that
// compositionBase selects at most 3 memory snippets, in exactly the order
// memory.Relevant would produce, and drops a snippet with zero keyword
// overlap.
func TestCharacterization_UserMemorySelectsTop3ByKeywordOverlap(t *testing.T) {
	m := newTestModel(t)
	m.memEnabled = true

	// Overlap with the query "go generics testing example concise" is
	// distinct per snippet by construction: 4, 3, 2, 1, and 0 shared
	// keywords respectively (memory.Relevant does exact keyword matching,
	// not stemming, so word forms are chosen deliberately).
	seeds := []string{
		"Go generics testing example code.",    // go, generics, testing, example -> 4
		"Go generics testing basics.",          // go, generics, testing -> 3
		"Go generics overview.",                // go, generics -> 2
		"Go language basics.",                  // go -> 1
		"Python scripting avoid abstractions.", // 0
	}
	for _, text := range seeds {
		if _, err := m.memStore.Add(text); err != nil {
			t.Fatalf("seed snippet %q: %v", text, err)
		}
	}

	raw := "go generics testing example concise"
	loaded, err := m.memStore.Load()
	if err != nil {
		t.Fatalf("load snippets: %v", err)
	}
	want := memory.Relevant(loaded, raw, 3)
	if len(want) != 3 {
		t.Fatalf("test setup: memory.Relevant returned %d snippets, want 3", len(want))
	}

	base := m.compositionBase(raw, nil, false)

	if len(base.input.MemorySnippets) > 3 {
		t.Fatalf("MemorySnippets length = %d, want <= 3", len(base.input.MemorySnippets))
	}
	if len(base.input.MemorySnippets) != len(want) {
		t.Fatalf("MemorySnippets = %d entries, want %d matching memory.Relevant(loaded, raw, 3)",
			len(base.input.MemorySnippets), len(want))
	}
	for i, sn := range want {
		if base.input.MemorySnippets[i] != sn.Text {
			t.Errorf("MemorySnippets[%d] = %q, want %q (same order as memory.Relevant)", i, base.input.MemorySnippets[i], sn.Text)
		}
	}
	for _, s := range base.input.MemorySnippets {
		if strings.Contains(s, "Python scripting") {
			t.Error("snippet with zero keyword overlap was included in MemorySnippets")
		}
		if strings.Contains(s, "Go language basics") {
			t.Error("4th-ranked snippet (below the top-3 cutoff) was included in MemorySnippets")
		}
	}
}

// TestCharacterization_RAGReturnsDeterministicTopKWithProvenance pins that
// compositionBase's RAG results are exactly what m.ragIndex.Search(raw,
// m.ragTopK()) returns, and that each result's file/line provenance survives
// into the rag.FormatContext output used for RetrievedContext.
func TestCharacterization_RAGReturnsDeterministicTopKWithProvenance(t *testing.T) {
	m := newTestModel(t)
	m.ragOn = true
	m.ragIndex = rag.NewIndex([]rag.DocumentChunk{
		{
			ID: "auth.go#1-4", Path: "internal/auth/auth.go", StartLine: 1, EndLine: 4,
			Text: "func Authenticate(token string) error {\n// validate the bearer token\nreturn nil\n}",
		},
		{
			ID: "session.go#1-4", Path: "internal/auth/session.go", StartLine: 1, EndLine: 4,
			Text: "func NewSession(token string) *Session {\n// bearer token check only, no validation performed here\nreturn &Session{}\n}",
		},
		{
			ID: "math.go#1-1", Path: "internal/util/math.go", StartLine: 1, EndLine: 1,
			Text: "func Add(a, b int) int { return a + b }",
		},
	})

	raw := "token validate bearer"

	base := m.compositionBase(raw, nil, false)
	want := m.ragIndex.Search(raw, m.ragTopK())

	if len(want) < 2 {
		t.Fatalf("test setup: Search returned %d results, want at least 2 to exercise ordering", len(want))
	}
	if !reflect.DeepEqual(base.ragResults, want) {
		t.Fatalf("compositionBase ragResults = %+v, want m.ragIndex.Search(raw, m.ragTopK()) = %+v", base.ragResults, want)
	}

	formatted := rag.FormatContext(base.ragResults, m.ragMaxContextChars())
	if base.input.RetrievedContext != formatted {
		t.Fatal("compositionBase RetrievedContext diverges from rag.FormatContext(ragResults, ragMaxContextChars())")
	}
	for _, r := range base.ragResults {
		if !strings.Contains(formatted, r.Chunk.Path) {
			t.Errorf("formatted context missing source path %q", r.Chunk.Path)
		}
		lineMarker := fmt.Sprintf("lines %d-%d", r.Chunk.StartLine, r.Chunk.EndLine)
		if !strings.Contains(formatted, lineMarker) {
			t.Errorf("formatted context missing line range %q for %q", lineMarker, r.Chunk.Path)
		}
	}
}

// TestCharacterization_RawUserMessageVerbatimAndLast pins Global Constraint
// #1: with both memory and RAG populated and contributing content, the raw
// user message still ends up byte-for-byte identical and last in the
// composed messages.
func TestCharacterization_RawUserMessageVerbatimAndLast(t *testing.T) {
	m := newTestModel(t)
	m.memEnabled = true
	if _, err := m.memStore.Add("Prefer concise Go code with comments."); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	m.ragOn = true
	m.ragIndex = rag.NewIndex([]rag.DocumentChunk{
		{ID: "concise.go#1-1", Path: "concise.go", StartLine: 1, EndLine: 1, Text: "func concise() {}"},
	})

	raw := "give me concise Go code <script>evil()</script>"
	prepared, err := m.prepareRequest(raw, nil, false)
	if err != nil {
		t.Fatalf("prepareRequest: %v", err)
	}

	var sawMemory, sawRAG bool
	for _, section := range prepared.composed.Sections {
		if section.Title == "Relevant Memory" && strings.TrimSpace(section.Content) != "" {
			sawMemory = true
		}
		if section.Title == "Retrieved Workspace Context" && strings.TrimSpace(section.Content) != "" {
			sawRAG = true
		}
	}
	if !sawMemory {
		t.Fatal("test setup: memory did not contribute a non-empty section")
	}
	if !sawRAG {
		t.Fatal("test setup: RAG did not contribute a non-empty section")
	}

	if len(prepared.composed.Messages) == 0 {
		t.Fatal("no messages composed")
	}
	last := prepared.composed.Messages[len(prepared.composed.Messages)-1]
	if last.Role != provider.RoleUser {
		t.Fatalf("last message role = %v, want RoleUser", last.Role)
	}
	if last.Content != raw {
		t.Fatalf("last message content = %q, want raw message %q byte-for-byte", last.Content, raw)
	}
}

// TestCharacterization_CacheKeyVariesWithMemoryAndRAG pins that the cache
// key's SystemPrompt (and therefore the overall key) already reflects
// whether a memory snippet or RAG hit was folded into the composed system
// prompt, per the existing "hash the fully composed system prompt" safety
// property.
func TestCharacterization_CacheKeyVariesWithMemoryAndRAG(t *testing.T) {
	raw := "give me a go example"

	keyFor := func(configure func(m *Model)) cache.Key {
		m := newTestModel(t)
		configure(m)
		prepared, err := m.prepareRequest(raw, nil, false)
		if err != nil {
			t.Fatalf("prepareRequest: %v", err)
		}
		return m.cacheKeyFromPrepared(raw, prepared)
	}

	// Memory variance: one model has a matching snippet, the other has none.
	withMem := keyFor(func(m *Model) {
		m.memEnabled = true
		if _, err := m.memStore.Add("Prefer concise Go examples."); err != nil {
			t.Fatalf("seed memory: %v", err)
		}
	})
	withoutMem := keyFor(func(m *Model) {
		m.memEnabled = true // memStore left empty: no snippet can match.
	})
	if withMem.SystemPrompt == withoutMem.SystemPrompt {
		t.Error("cache key SystemPrompt unchanged when memory snippet availability changed")
	}
	if withMem == withoutMem {
		t.Error("cache key unchanged when memory snippet availability changed")
	}

	// Typed project-memory variance follows the same fully composed prompt
	// rule without changing legacy user-memory selection.
	withProjectMemory := keyFor(func(m *Model) {
		m.memEnabled = true
		if _, err := m.projectStore.Add(
			memoryindex.KindProjectConvention,
			"Go examples must pass gofmt.",
		); err != nil {
			t.Fatalf("seed project memory: %v", err)
		}
	})
	withoutProjectMemory := keyFor(func(m *Model) {
		m.memEnabled = true
	})
	if withProjectMemory.SystemPrompt == withoutProjectMemory.SystemPrompt {
		t.Error("cache key SystemPrompt unchanged when project memory availability changed")
	}
	if withProjectMemory == withoutProjectMemory {
		t.Error("cache key unchanged when project memory availability changed")
	}

	// RAG variance: one model's index has a matching chunk, the other's does not.
	withRAG := keyFor(func(m *Model) {
		m.ragOn = true
		m.ragIndex = rag.NewIndex([]rag.DocumentChunk{
			{ID: "a.go#1-2", Path: "a.go", StartLine: 1, EndLine: 2, Text: "// go example reference\nfunc run() {}"},
		})
	})
	withoutRAG := keyFor(func(m *Model) {
		m.ragOn = true
		m.ragIndex = rag.NewIndex([]rag.DocumentChunk{
			{ID: "b.go#1-2", Path: "b.go", StartLine: 1, EndLine: 2, Text: "// completely unrelated content\nfunc other() {}"},
		})
	})
	if withRAG.SystemPrompt == withoutRAG.SystemPrompt {
		t.Error("cache key SystemPrompt unchanged when RAG hit availability changed")
	}
	if withRAG == withoutRAG {
		t.Error("cache key unchanged when RAG hit availability changed")
	}
}

// TestCharacterization_AgentVerifierReceivesNoGenericMemoryOrRAG pins that
// agentverify.Input — the boundary the fresh-context verifier actually
// receives — carries no field sourced from internal/memory or internal/rag.
// A structural allowlist check is used because agentverify.Input takes no
// *Model and is built purely from run/cycle evidence, so there is nothing to
// seed markers into that this boundary would ever see; the field set itself
// is the property under test.
func TestCharacterization_AgentVerifierReceivesNoGenericMemoryOrRAG(t *testing.T) {
	allowed := map[string]bool{
		"RunID": true, "Cycle": true, "Task": true, "Objective": true,
		"AcceptanceCriteria": true, "Criteria": true, "Evidence": true,
		"PriorCycles": true, "EstablishCriteria": true, "Execution": true, "Tools": true,
	}

	typ := reflect.TypeOf(agentverify.Input{})
	if typ.Kind() != reflect.Struct {
		t.Fatalf("agentverify.Input is not a struct: %v", typ.Kind())
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !allowed[field.Name] {
			t.Errorf("agentverify.Input has unexpected field %q (type %s) — confirm it is not sourced from "+
				"internal/memory or internal/rag before extending the allowlist", field.Name, field.Type)
		}
		typeName := strings.ToLower(field.Type.String())
		if strings.Contains(typeName, "memory.") || strings.Contains(typeName, "rag.") {
			t.Errorf("agentverify.Input field %q has a type from internal/memory or internal/rag: %s", field.Name, field.Type)
		}
	}
	if typ.NumField() != len(allowed) {
		t.Errorf("agentverify.Input has %d fields, want %d (allowlist is out of date — verify any new field's provenance)",
			typ.NumField(), len(allowed))
	}
}

// TestCharacterization_MemoryDisabledByDefault pins that a fresh config load
// with no config file and no environment overrides leaves user memory off.
func TestCharacterization_MemoryDisabledByDefault(t *testing.T) {
	v, err := config.NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := config.Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Memory.Enabled {
		t.Error("Memory.Enabled = true, want false by default with no config file/env present")
	}
}
