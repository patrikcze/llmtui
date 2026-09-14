package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// evolutionScenarioReport is test-only observability for the Phase 0
// characterization fixtures. It deliberately reports current controller
// behavior and its limitation; it is not a production evaluation format.
type evolutionScenarioReport struct {
	ID         string
	Verdict    string
	Executed   []string
	Requests   int
	Tokens     int
	Limitation string
	SkipReason string
}

type evolutionManifest struct {
	SchemaVersion int                         `json:"schema_version"`
	Scenarios     []evolutionManifestScenario `json:"scenarios"`
}

type evolutionManifestScenario struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Limitation string `json:"limitation"`
}

func loadEvolutionManifest(t *testing.T) evolutionManifest {
	t.Helper()
	path := filepath.Join("testdata", "agent_evolution", "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest evolutionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func reportEvolutionScenario(t *testing.T, report evolutionScenarioReport) {
	t.Helper()
	manifest := loadEvolutionManifest(t)
	for _, scenario := range manifest.Scenarios {
		if scenario.ID != report.ID {
			continue
		}
		if report.Limitation != scenario.Limitation {
			t.Fatalf("scenario %q limitation = %q, want manifest wording %q", report.ID, report.Limitation, scenario.Limitation)
		}
		t.Logf("scenario=%s verdict=%s executed=%v requests=%d tokens=%d skip=%q limitation=%s",
			report.ID,
			report.Verdict,
			report.Executed,
			report.Requests,
			report.Tokens,
			report.SkipReason,
			report.Limitation,
		)
		return
	}
	t.Fatalf("scenario %q is missing from the Phase 0 manifest", report.ID)
}

func TestAgentEvolutionManifest(t *testing.T) {
	manifest := loadEvolutionManifest(t)
	if manifest.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", manifest.SchemaVersion)
	}
	if len(manifest.Scenarios) == 0 {
		t.Fatal("manifest must contain at least one scenario")
	}
	ids := make([]string, 0, len(manifest.Scenarios))
	for _, scenario := range manifest.Scenarios {
		if scenario.ID == "" || scenario.Kind == "" || scenario.Limitation == "" {
			t.Fatalf("incomplete scenario: %+v", scenario)
		}
		ids = append(ids, scenario.ID)
	}
	sort.Strings(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatalf("duplicate scenario ID %q", ids[i])
		}
	}
}
