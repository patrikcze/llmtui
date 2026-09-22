package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestToolResultEntityEntersPromptAsMinimalReference(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Entities.Enabled = true
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
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

func TestEntityLookupAfterHistoryIsDropped(t *testing.T) {
	for _, native := range []bool{true, false} {
		t.Run(map[bool]string{true: "native", false: "fenced"}[native], func(t *testing.T) {
			m := newTestModel(t)
			m.toolsOn = true
			m.toolsNative = native
			m.toolRunner = tools.NewRunner(t.TempDir(), 64)
			now := time.Unix(100, 0)
			m.entities = entity.NewRegistry(entity.Limits{Now: func() time.Time { return now }})
			m.registerResultEntities([]tools.Result{{
				Call: tools.Call{Tool: tools.ToolReadFile, Path: "release.md"},
				Entities: []entity.Candidate{{
					Kind: entity.KindFile, Label: "Release notes", Trust: entity.TrustWorkspaceUntrusted,
					Preview: strings.Repeat("migration summary ", 45), Payload: "unique full migration instructions",
				}},
			}})
			for i := range 12 {
				now = now.Add(time.Second)
				if _, err := m.entities.Put(entity.Candidate{
					Kind: entity.KindFile, Label: fmt.Sprintf("unrelated %d", i),
					Trust: entity.TrustWorkspaceUntrusted, Scope: entity.ScopeSession,
					Preview: strings.Repeat("unrelated ", 75), Payload: "other payload",
				}); err != nil {
					t.Fatal(err)
				}
			}
			// The backing entity survives; neither history nor the recent shortlist
			// contains its identity. The user supplies only a natural topic.
			query := `{"query":"release notes"}`
			expand := `{"entity_ids":["ent_00001"],"level":"full"}`
			steps := []agentScriptStep{
				{toolCalls: []provider.ToolCall{{ID: "lookup", Name: tools.ToolGetEntityDetails, Arguments: query}}},
				{toolCalls: []provider.ToolCall{{ID: "expand", Name: tools.ToolGetEntityDetails, Arguments: expand}}},
				{text: "Here are the earlier migration instructions."},
			}
			if !native {
				steps[0] = agentScriptStep{text: "```tool get_entity_details\n" + query + "\n```"}
				steps[1] = agentScriptStep{text: "```tool get_entity_details\n" + expand + "\n```"}
			}
			prov := &scriptedAgentProvider{steps: steps}
			m.prov = prov
			m.input.SetValue("What did the release notes say about migration?")
			driveAgentCommands(t, m, m.send())
			if len(prov.requests) != 3 {
				t.Fatalf("requests=%d err=%s", len(prov.requests), m.errText)
			}
			for _, msg := range prov.requests[0].Messages {
				// The contract contains an illustrative ID, so check the actual record.
				if strings.Contains(msg.Content, `id="ent_00001"`) || strings.Contains(msg.Content, "unique full migration") {
					t.Fatal("old entity was already present before lookup")
				}
			}
			foundMinimal, foundFull := false, false
			for _, msg := range prov.requests[1].Messages {
				if strings.Contains(msg.Content, `"id":"ent_00001"`) && strings.Contains(msg.Content, `"level":"minimal"`) {
					foundMinimal = true
					if native && msg.ToolCallID != "lookup" {
						t.Fatal("lookup result lost native correlation")
					}
				}
				if strings.Contains(msg.Content, "unique full migration") {
					t.Fatal("query auto-expanded a payload")
				}
			}
			for _, msg := range prov.requests[2].Messages {
				if strings.Contains(msg.Content, "unique full migration instructions") {
					foundFull = strings.Contains(msg.Content, "LLMTUI_UNTRUSTED_BEGIN")
				}
			}
			if !foundMinimal || !foundFull {
				t.Fatalf("discovery/expansion failed: minimal=%v full=%v", foundMinimal, foundFull)
			}
		})
	}
}

func TestEntityProtocolFollowsToolAvailability(t *testing.T) {
	m := newTestModel(t)
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	if _, err := m.entities.Put(entity.Candidate{
		Kind: entity.KindFile, Label: "notes", Payload: "data",
		Trust: entity.TrustWorkspaceUntrusted, Scope: entity.ScopeSession,
	}); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		m.toolsOn = enabled
		out := prompt.Compose(m.compositionBase("earlier notes", nil, false).input)
		found := false
		for _, section := range out.Sections {
			found = found || section.Title == "Entity Context"
		}
		if found != enabled {
			t.Fatalf("tools enabled=%v, entity protocol present=%v", enabled, found)
		}
	}
}

func TestEntityQueryReportsPartialAndEmptyMatches(t *testing.T) {
	m := newTestModel(t)
	for range 10 {
		if _, err := m.entities.Put(entity.Candidate{
			Kind: entity.KindFile, Label: "notes", Payload: "data",
			Trust: entity.TrustWorkspaceUntrusted, Scope: entity.ScopeSession,
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		query string
		total int
		count int
	}{
		{name: "partial", query: "notes", total: 10, count: 8},
		{name: "empty", query: "absent", total: 0, count: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := tools.Call{Tool: tools.ToolGetEntityDetails, SearchQuery: tc.query}
			if err := tools.ValidateEntityDetailsCall(&call); err != nil {
				t.Fatal(err)
			}
			var result entityDetailsWire
			output, _ := m.resolveEntityDetails(call)
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			if result.TotalMatches == nil || *result.TotalMatches != tc.total || len(result.Entities) != tc.count {
				t.Fatalf("query result = %+v", result)
			}
			if result.Truncated != (tc.total > tc.count) {
				t.Fatalf("truncated = %v", result.Truncated)
			}
		})
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
