package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
)

// TestAgentEvolutionSyntheticMatrix emits and validates a compact JSONL report
// over the scripted action-class matrix. The independent oracle is the fixture
// expectation, not the verifier's answer, so a verifier that incorrectly
// passes a negative case cannot improve the report. The file lives in a temp
// directory: CI stays hermetic while the test proves the report contract used
// by the opt-in evaluation procedure in docs/agent-evaluation.md.
func TestAgentEvolutionSyntheticMatrix(t *testing.T) {
	type result struct {
		Scenario     string `json:"scenario"`
		Expected     string `json:"expected_action"`
		Observed     string `json:"observed_action"`
		Correct      bool   `json:"correct"`
		FalseSuccess bool   `json:"false_success"`
		Executed     int    `json:"executed_calls"`
	}

	path := filepath.Join(t.TempDir(), "agent-evolution.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	var correct, falseSuccess, executed int
	for _, scenario := range actionScenarios() {
		observed := observeAgentAction(t, scenario)
		row := result{
			Scenario:     scenario.id,
			Expected:     string(scenario.wantAction),
			Observed:     string(observed.action),
			Correct:      observed.action == scenario.wantAction && observed.outcome == scenario.wantOutcome,
			FalseSuccess: scenario.wantFinal != agent.DecisionDone && observed.final == agent.DecisionDone,
			Executed:     len(observed.executed),
		}
		if err := encoder.Encode(row); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if row.Correct {
			correct++
		}
		if row.FalseSuccess {
			falseSuccess++
		}
		executed += row.Executed
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if correct != len(actionScenarios()) || falseSuccess != 0 {
		t.Fatalf("synthetic matrix: correct=%d/%d false_success=%d", correct, len(actionScenarios()), falseSuccess)
	}
	t.Logf("agent evolution synthetic matrix: scenarios=%d correct=%d false_success=%d executed_calls=%d report=%s", len(actionScenarios()), correct, falseSuccess, executed, path)
}
