package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/rag"
)

// seedRagIndex gives the model a small in-memory index for command tests.
func seedRagIndex(m *Model) {
	m.ragIndex = rag.NewIndex([]rag.DocumentChunk{
		{ID: "stream.go#1-3", Path: "stream.go", StartLine: 1, EndLine: 3, Text: "func streaming() {\n// parse server-sent events\n}"},
		{ID: "add.go#1-1", Path: "add.go", StartLine: 1, EndLine: 1, Text: "func add(a, b int) int { return a + b }"},
	})
}

func TestCmdRagStatusOpensOverlay(t *testing.T) {
	m := newTestModel(t)
	if cmd := cmdRag(m, "status"); cmd != nil {
		t.Fatalf("status returned a command: %v", cmd)
	}
	if !m.overlayOpen {
		t.Fatal("status did not open an overlay")
	}
}

func TestCmdRagOnOffTogglesState(t *testing.T) {
	m := newTestModel(t)
	cmdRag(m, "on")
	if !m.ragOn {
		t.Error("ragOn not set after /rag on")
	}
	cmdRag(m, "off")
	if m.ragOn {
		t.Error("ragOn still set after /rag off")
	}
}

func TestCmdRagSearchRequiresQuery(t *testing.T) {
	m := newTestModel(t)
	seedRagIndex(m)
	cmdRag(m, "search")
	if m.errText == "" {
		t.Error("expected an error message for empty query")
	}
	if m.overlayOpen {
		t.Error("empty query should not open a results overlay")
	}
}

func TestCmdRagSearchShowsResults(t *testing.T) {
	m := newTestModel(t)
	seedRagIndex(m)
	cmdRag(m, "search streaming parser")
	if !m.overlayOpen {
		t.Fatal("search did not open an overlay")
	}
	// The overlay builder is what the command renders into the viewport.
	overlay := m.ragSearchOverlay("streaming parser")
	if !strings.Contains(overlay, "stream.go") {
		t.Errorf("search overlay missing expected match:\n%s", overlay)
	}
}

func TestCmdRagClearResetsIndex(t *testing.T) {
	m := newTestModel(t)
	seedRagIndex(m)
	m.ragOn = true
	cmdRag(m, "clear")
	if m.ragIndex != nil {
		t.Error("ragIndex not cleared")
	}
}

func TestRagRetrievalFlowsIntoComposition(t *testing.T) {
	m := newTestModel(t)
	seedRagIndex(m)
	m.ragOn = true

	out, _ := m.compose("how does streaming work", nil, false)
	system := out.Messages[0].Content
	if !strings.Contains(system, "stream.go") {
		t.Errorf("retrieved context not in composed system prompt:\n%s", system)
	}
	// Raw message stays verbatim and last.
	last := out.Messages[len(out.Messages)-1]
	if last.Content != "how does streaming work" {
		t.Errorf("raw message altered: %q", last.Content)
	}
	if len(m.ragLast) == 0 {
		t.Error("ragLast not recorded for /debug last")
	}
}

func TestRagOffSkipsRetrieval(t *testing.T) {
	m := newTestModel(t)
	seedRagIndex(m)
	m.ragOn = false

	out, _ := m.compose("how does streaming work", nil, false)
	if strings.Contains(out.Messages[0].Content, "stream.go") {
		t.Error("retrieval ran while RAG was off")
	}
}
