package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestNativeArgumentCoercionIsRecordedAndExecutes covers the TUI half of
// audit P2-11: a numeric-string argument runs with the coerced value and
// leaves a content-free arguments_coerced diagnostic for /debug last.
func TestNativeArgumentCoercionIsRecordedAndExecutes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, _ := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolReadFile, Arguments: `{"path":"notes.txt","offset":"1","limit":"2"}`}}},
		agentScriptStep{text: "The notes start with one and two."},
		agentScriptStep{text: verifierJSON("passed", "read and reported", "", false, false)},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(root, 64)
	driveAgentCommands(t, m, m.startVerifiedRun("what do the notes start with?", nil))

	run := m.agentLoop.run
	calls := run.LatestCycle().Execution.ToolCalls
	if run.Status != agent.DecisionDone || len(calls) != 1 || !calls[0].Succeeded {
		t.Fatalf("status=%s calls=%+v, want one successful read", run.Status, calls)
	}
	var found bool
	for _, d := range m.toolCallDiagnostics {
		if d.Classification == provider.ToolCallArgumentsCoerced {
			found = true
			if d.Detail != "limit=integer,offset=integer" {
				t.Errorf("coercion detail = %q", d.Detail)
			}
		}
	}
	if !found {
		t.Fatal("no arguments_coerced diagnostic recorded")
	}
}

// TestNativeUnknownArgumentIsRejectedThenCorrected checks the recovery
// path: an undeclared key reaches the model as an actionable tool error
// naming the accepted keys, and a corrected call then proceeds.
func TestNativeUnknownArgumentIsRejectedThenCorrected(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolReadFile, Arguments: `{"filename":"notes.txt"}`}}},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-2", Name: tools.ToolReadFile, Arguments: `{"path":"notes.txt"}`}}},
		agentScriptStep{text: "notes.txt says hello."},
		agentScriptStep{text: verifierJSON("passed", "read and reported", "", false, false)},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(root, 64)
	driveAgentCommands(t, m, m.startVerifiedRun("what do the notes say?", nil))

	if status := m.agentLoop.run.Status; status != agent.DecisionDone {
		t.Fatalf("run status = %s (%q)", status, m.agentLoop.run.StopReason)
	}
	// The request after the rejected call must carry the actionable error.
	var toolResult string
	for _, message := range prov.requests[2].Messages {
		if message.Role == provider.RoleTool {
			toolResult = message.Content
		}
	}
	for _, want := range []string{`unknown argument(s) "filename"`, "read_file accepts only:", "path"} {
		if !strings.Contains(toolResult, want) {
			t.Errorf("tool result %q is missing %q", toolResult, want)
		}
	}
}

// TestNativeFilePathAliasExecutesAndIsRecorded covers the TUI half of the
// file_path alias: the call runs as a normal read, with no rejected round,
// and leaves a content-free arguments_coerced diagnostic.
func TestNativeFilePathAliasExecutesAndIsRecorded(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolReadFile, Arguments: `{"file_path":"notes.txt"}`}}},
		agentScriptStep{text: "notes.txt says hello."},
		agentScriptStep{text: verifierJSON("passed", "read and reported", "", false, false)},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(root, 64)
	driveAgentCommands(t, m, m.startVerifiedRun("what do the notes say?", nil))

	run := m.agentLoop.run
	calls := run.LatestCycle().Execution.ToolCalls
	if run.Status != agent.DecisionDone || len(calls) != 1 || !calls[0].Succeeded {
		t.Fatalf("status=%s calls=%+v, want one successful read", run.Status, calls)
	}
	if len(prov.requests) != 4 {
		t.Fatalf("requests = %d, want 4 (contract, read, answer, verifier) with no rejected round", len(prov.requests))
	}
	var detail string
	for _, d := range m.toolCallDiagnostics {
		if d.Classification == provider.ToolCallArgumentsCoerced {
			detail = d.Detail
		}
	}
	if detail != "file_path=path" {
		t.Fatalf("arguments_coerced detail = %q, want file_path=path", detail)
	}
}
