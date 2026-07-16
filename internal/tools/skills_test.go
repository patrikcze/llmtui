package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

type fakeLoader struct {
	loaded []string
	err    error
}

func (f *fakeLoader) LoadSkillForRun(id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.loaded = append(f.loaded, id)
	return "Skill " + id + " was loaded for the current run.", nil
}

func TestSkillSpecSchema(t *testing.T) {
	specs := SkillSpecs()
	if len(specs) != 1 || specs[0].Name != ToolSkillLoad {
		t.Fatalf("specs = %+v", specs)
	}
	var schema struct {
		Type       string         `json:"type"`
		Required   []string       `json:"required"`
		Additional *bool          `json:"additionalProperties"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(specs[0].Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || len(schema.Required) != 1 || schema.Required[0] != "skill" {
		t.Errorf("schema = %+v", schema)
	}
	if schema.Additional == nil || *schema.Additional {
		t.Error("additionalProperties must be false")
	}
}

func TestCallsFromNativeSkillLoad(t *testing.T) {
	calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: ToolSkillLoad, Arguments: `{"skill":"go-review"}`}})
	if len(calls) != 1 || calls[0].Tool != ToolSkillLoad || calls[0].Path != "go-review" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestRunnerSkillLoad(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)

	// Without a loader wired, the tool reports it is unavailable.
	res := r.Execute(Call{ID: "c", Tool: ToolSkillLoad, Path: "x"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "not available") {
		t.Errorf("no-loader result = %+v", res)
	}

	loader := &fakeLoader{}
	r.Skills = loader
	res = r.Execute(Call{ID: "c", Tool: ToolSkillLoad, Path: "go-review"})
	if res.Err != nil || !strings.Contains(res.Output, "loaded for the current run") {
		t.Errorf("result = %+v", res)
	}
	if len(loader.loaded) != 1 || loader.loaded[0] != "go-review" {
		t.Errorf("loaded = %v", loader.loaded)
	}

	// A missing id is a recoverable error.
	res = r.Execute(Call{ID: "c", Tool: ToolSkillLoad})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "needs a skill id") {
		t.Errorf("empty-id result = %+v", res)
	}
}

func TestSkillLoadNeedsNoApproval(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	c := Call{Tool: ToolSkillLoad, Path: "x"}
	if NeedsApproval(c) || r.NeedsApproval(c) {
		t.Error("skill_load must be approval-free")
	}
}

func TestFencedSkillLoadParses(t *testing.T) {
	calls := Parse("```tool skill_load go-review\n```")
	if len(calls) != 1 || calls[0].Tool != ToolSkillLoad || calls[0].Path != "go-review" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestDefaultRegistryIncludesSkillLoad(t *testing.T) {
	info, ok := DefaultRegistry().Get(ToolSkillLoad)
	if !ok {
		t.Fatal("skill_load missing from the capability registry")
	}
	if info.Source != "skills" || info.Safety != SafetyReadOnly || info.Approval != "no" {
		t.Errorf("info = %+v", info)
	}
}
