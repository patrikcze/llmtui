package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestVerifiedAgentExactReadMatchesAcrossPathSpellings is the end-to-end
// regression for audit P2-5's identity half: a model reading "./src/…" or
// the absolute in-workspace path must satisfy a criterion naming
// "src/…", exactly like the plain spelling. (Absolute paths never reach
// here: the read tool rejects them.)
func TestVerifiedAgentExactReadMatchesAcrossPathSpellings(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "notes.txt"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"dot-slash": "./src/notes.txt",
		"redundant": "src/../src/notes.txt",
	} {
		t.Run(name, func(t *testing.T) {
			m, prov := configureAgentTestModel(t,
				agentScriptStep{toolCalls: []provider.ToolCall{{ID: "read-1", Name: tools.ToolReadFile, Arguments: `{"path":"` + filepath.ToSlash(path) + `"}`}}},
				agentScriptStep{text: "The notes say alpha and beta."},
			)
			prov.contractReplies = []string{`{"criteria":["Read the file src/notes.txt"],"needs_user_input":false,"question":"","user_options":[]}`}
			m.toolsOn = true
			m.toolsNative = true
			m.toolRunner = tools.NewRunner(root, 64)
			driveAgentCommands(t, m, m.startVerifiedRun("What do the notes say?", nil))

			run := m.agentLoop.run
			if run.Status != agent.DecisionDone || run.Cycle != 1 {
				t.Fatalf("status=%s cycle=%d stop=%q", run.Status, run.Cycle, run.StopReason)
			}
			if got := run.Criteria[0].Status; got != agent.CriterionSatisfied {
				t.Fatalf("criterion status = %s, want satisfied by observed read coverage", got)
			}
			if len(prov.requests) != 3 {
				t.Fatalf("requests = %d, want no semantic verifier (coverage proved the criterion)", len(prov.requests))
			}
		})
	}
}

// TestCoverageHighWaterIgnoresLostAndRereadCoverage is the regression for
// P2-5's progress half: coverage lost to retention and then re-read changes
// the coverage digest, which used to count as progress and reset the
// no-progress nudge budget. Only a strict increase over the episode's best
// contiguous coverage counts now.
func TestCoverageHighWaterIgnoresLostAndRereadCoverage(t *testing.T) {
	total := int64(1000)
	window := func(end int64) []agent.ReadObservation {
		return []agent.ReadObservation{{Target: "big.txt", StartLine: 1, EndLine: end, TotalLines: &total}}
	}
	obligations := []agent.ExactReadObligation{{CriterionID: "c1", Target: "big.txt"}}
	checkpoint := &agent.EpisodeCheckpoint{PolicyVersion: 1}

	if !advanceCoverageHighWater(checkpoint, obligations, window(300)) {
		t.Fatal("first coverage did not count as progress")
	}
	if advanceCoverageHighWater(checkpoint, obligations, nil) {
		t.Fatal("lost coverage counted as progress")
	}
	if advanceCoverageHighWater(checkpoint, obligations, window(300)) {
		t.Fatal("re-reading already covered lines counted as progress")
	}
	if !advanceCoverageHighWater(checkpoint, obligations, window(600)) {
		t.Fatal("genuinely new coverage did not count as progress")
	}
	if got := checkpoint.CoverageHighWater["c1"]; got != 600 {
		t.Fatalf("high-water = %d, want 600", got)
	}
}
