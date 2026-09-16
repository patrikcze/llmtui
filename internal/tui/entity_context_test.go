package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestToolResultEntityEntersPromptAsMinimalReference(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Entities.Enabled = true
	results := m.registerResultEntities([]tools.Result{{
		Call:   tools.Call{Tool: tools.ToolWebSearch},
		Output: "large result body",
		Entities: []entity.Candidate{{
			Kind:       entity.KindWebResult,
			Provenance: entity.Provenance{Source: "web", Operation: tools.ToolWebSearch},
			Label:      "Example",
			Trust:      entity.TrustWebUntrusted,
			Scope:      entity.ScopeSession,
			Preview:    "short preview",
			Payload:    "large result body",
		}},
	}})
	if !strings.Contains(results[0].Output, "ent_00001") {
		t.Fatalf("result lacks entity reference: %q", results[0].Output)
	}
	base := m.compositionBase("use the result", nil, false)
	if len(base.input.Entities) != 1 || base.input.Entities[0].ID != "ent_00001" {
		t.Fatalf("prompt entities = %+v", base.input.Entities)
	}
	if strings.Contains(base.input.Entities[0].Preview, "large result body") {
		t.Fatal("full payload leaked into the minimal prompt record")
	}
}

func TestEntityDiagnosticsDoNotExposePayload(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Entities.Enabled = true
	m.registerResultEntities([]tools.Result{{
		Call: tools.Call{Tool: tools.ToolReadFile, Path: "notes.txt"},
		Entities: []entity.Candidate{{
			Kind:       entity.KindFile,
			Provenance: entity.Provenance{Source: "workspace", Operation: tools.ToolReadFile},
			Label:      "notes.txt",
			Trust:      entity.TrustWorkspaceUntrusted,
			Scope:      entity.ScopeSession,
			Payload:    "secret payload should stay out of diagnostics",
		}},
	}})
	overlay := m.entityInspectOverlay("ent_00001")
	if strings.Contains(overlay, "secret payload") {
		t.Fatal("entity inspect exposed payload")
	}
}

func TestDisabledEntitiesAreNotModelVisible(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Entities.Enabled = false
	m.toolsOn = true
	m.toolsNative = true
	for _, spec := range m.activeToolSpecs() {
		if spec.Name == tools.ToolGetEntityDetails {
			t.Fatal("disabled entity capability was offered to the model")
		}
	}
}
