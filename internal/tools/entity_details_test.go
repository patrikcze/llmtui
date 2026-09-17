package tools

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestEntityDetailsQueryProtocols(t *testing.T) {
	for _, native := range []bool{false, true} {
		name := "fenced"
		if native {
			name = "native"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				args  string
				valid bool
			}{
				{name: "topic", args: `{"query":"release notes"}`, valid: true},
				{name: "visual topic", args: `{"query":"release notes screenshot","kinds":["vision_observation"]}`, valid: true},
				{name: "minimal", args: `{"query":"release notes","level":"minimal"}`, valid: true},
				{name: "mixed selectors", args: `{"query":"notes","entity_ids":["ent_00001"]}`},
				{name: "query cannot expand", args: `{"query":"notes","level":"full"}`},
				{name: "empty", args: `{"query":" "}`},
				{name: "oversized", args: `{"query":"` + strings.Repeat("a", 513) + `"}`},
				{name: "unknown kind", args: `{"query":"notes","kinds":["web_resultish"]}`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					calls := Parse("```tool get_entity_details\n" + tc.args + "\n```")
					if native {
						calls = CallsFromNative([]provider.ToolCall{{ID: "lookup", Name: ToolGetEntityDetails, Arguments: tc.args}})
					}
					if len(calls) != 1 || (calls[0].InputErr == "") != tc.valid {
						t.Fatalf("calls = %+v, want valid=%v", calls, tc.valid)
					}
					if tc.valid && calls[0].EntityLevel != "minimal" {
						t.Fatalf("query level = %q", calls[0].EntityLevel)
					}
				})
			}
		})
	}
}

func TestEntityDetailsFencedCallIsBoundedAndNormalized(t *testing.T) {
	calls := Parse("```tool get_entity_details\n{\"entity_ids\":[\"ent_00001\"],\"level\":\"MINIMAL\"}\n```")
	if len(calls) != 1 || calls[0].EntityIDCount != 1 || calls[0].EntityIDs[0] != "ent_00001" || calls[0].EntityLevel != "minimal" {
		t.Fatalf("parsed entity details = %+v", calls)
	}
}

func TestEntityDetailsNativeCallRejectsOversizedBatch(t *testing.T) {
	args := `{"entity_ids":["ent_00001","ent_00002","ent_00003","ent_00004","ent_00005","ent_00006","ent_00007","ent_00008","ent_00009"]}`
	calls := CallsFromNative([]provider.ToolCall{{ID: "call-1", Name: ToolGetEntityDetails, Arguments: args}})
	if len(calls) != 1 || calls[0].InputErr == "" {
		t.Fatalf("oversized native call = %+v", calls)
	}
}

func TestEntityDetailsValidationRejectsBlankIDs(t *testing.T) {
	call := Call{Tool: ToolGetEntityDetails, EntityIDs: [MaxEntityDetailsIDs]string{"ent_00001", " "}, EntityIDCount: 2}
	if err := ValidateEntityDetailsCall(&call); err == nil {
		t.Fatal("blank entity ID was accepted")
	}
}

func TestEntityDetailsKindsAreNormalizedAndBounded(t *testing.T) {
	calls := Parse("```tool get_entity_details\n{" +
		`"query":"screenshot","kinds":[" VISION_OBSERVATION "]` + "}\n```")
	if len(calls) != 1 || calls[0].InputErr != "" || calls[0].EntityKindCount != 1 ||
		calls[0].EntityKinds[0] != "vision_observation" {
		t.Fatalf("visual kind call = %+v", calls)
	}
}
