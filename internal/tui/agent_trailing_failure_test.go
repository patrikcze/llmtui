package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestObservationalReadToolNamesMatchToolConstants keeps internal/agent's
// duplicated read-only tool names (it must not import internal/tools) equal
// to the real tool constants.
func TestObservationalReadToolNamesMatchToolConstants(t *testing.T) {
	got := agent.ObservationalReadToolNames()
	slices.Sort(got)
	want := []string{tools.ToolGlob, tools.ToolGrep, tools.ToolListDir, tools.ToolReadFile}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("observational read tools = %v, want %v", got, want)
	}
}

// TestVerifiedAgentMissingFileAnswerReachesVerifier is the adaptive-mode
// regression for audit P1-1's negative-answer case: the executor reads a
// file that does not exist and correctly reports that. The typed not_found
// outcome must be recorded on the receipt and the cycle must reach the
// semantic verifier, instead of being concluded as a failed run.
func TestVerifiedAgentMissingFileAnswerReachesVerifier(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolReadFile, Arguments: `{"path":"config.yaml"}`}}},
		agentScriptStep{text: "config.yaml does not exist in the workspace."},
		agentScriptStep{text: verifierJSON("passed", "the executor correctly reported the file is absent", "", false, false)},
	)
	m.cfg.Agent.Verifier.Mode = "adaptive"
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("check whether config.yaml exists", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone || run.Cycle != 1 {
		t.Fatalf("run status=%s cycle=%d stop=%q, want done in one cycle", run.Status, run.Cycle, run.StopReason)
	}
	if len(prov.requests) != 4 {
		t.Fatalf("provider requests = %d, want contract + executor + tool continuation + semantic verifier", len(prov.requests))
	}
	calls := run.LatestCycle().Execution.ToolCalls
	if len(calls) != 1 || calls[0].Succeeded || calls[0].ErrorCode != "not_found" {
		t.Fatalf("tool receipts = %+v, want one failed read_file with error_code not_found", calls)
	}
}

// TestVerifiedAgentTrailingFailureEarnsOneRecoveryCycle covers the other
// half of P1-1 in adaptive mode: a non-observational trailing failure is
// still a deterministic failure, but it now earns one bounded recovery cycle
// whose objective names the failed call, and the run completes when that
// cycle recovers.
func TestVerifiedAgentTrailingFailureEarnsOneRecoveryCycle(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, prov := configureAgentTestModel(t,
		// Cycle 1: reading a directory as a file is a typed, non-observational
		// failure; the executor then answers without recovering.
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolReadFile, Arguments: `{"path":"docs"}`}}},
		agentScriptStep{text: "I could not read the notes."},
		// Cycle 2 (recovery): a correct read, then a verified answer.
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-2", Name: tools.ToolReadFile, Arguments: `{"path":"notes.txt"}`}}},
		agentScriptStep{text: "notes.txt says hello."},
		agentScriptStep{text: verifierJSON("passed", "the notes were read and reported", "", false, false)},
	)
	m.cfg.Agent.Verifier.Mode = "adaptive"
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(root, 64)
	driveAgentCommands(t, m, m.startVerifiedRun("tell me what the notes say", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone || run.Cycle != 2 {
		t.Fatalf("run status=%s cycle=%d stop=%q, want done after one recovery cycle", run.Status, run.Cycle, run.StopReason)
	}
	first := run.Cycles[0]
	if first.Execution == nil || len(first.Execution.ToolCalls) != 1 || first.Execution.ToolCalls[0].Succeeded {
		t.Fatalf("cycle 1 execution = %+v, want the failed read", first.Execution)
	}
	if !strings.HasPrefix(run.Cycles[1].Objective, `Recover from the failed read_file("docs") call`) {
		t.Fatalf("recovery objective = %q", run.Cycles[1].Objective)
	}
	if len(prov.requests) != 6 {
		t.Fatalf("provider requests = %d, want contract + 2 executor rounds per cycle + one semantic verifier", len(prov.requests))
	}
}
