package tools

import (
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

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
