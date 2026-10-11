package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestFixedAndRerunTestsDoNotFailTheCycle replays a live run: go test failed,
// the executor fixed the source with edit_file, go test passed, and it
// reported the result. The first, expected red run made the controller judge
// the cycle "deterministic test failure" and start a second cycle for work
// that was already done. The cycle now reaches the semantic verifier and the
// run completes in one cycle.
func TestFixedAndRerunTestsDoNotFailTheCycle(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "t1", Name: tools.ToolRunCommand, Arguments: `{"command":"go test ./..."}`}}},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "e1", Name: tools.ToolEditFile, Arguments: `{"path":"mathx.go","old_text":"return a - b","new_text":"return a + b"}`}}},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "t2", Name: tools.ToolRunCommand, Arguments: `{"command":"go test ./..."}`}}},
		agentScriptStep{text: "Add subtracted instead of adding; fixed mathx.go and go test ./... now passes."},
		agentScriptStep{text: verifierJSONSatisfying("the rerun passed after the fix", "c1")},
		agentScriptStep{text: "must never be requested"},
	)
	m.cfg.Agent.Verifier.Mode = "adaptive"
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests = 64
	root := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":        "module livetest\n\ngo 1.22\n",
		"mathx.go":      "package main\n\nfunc Add(a, b int) int { return a - b }\n\nfunc main() {}\n",
		"mathx_test.go": "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"want 5\")\n\t}\n}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	prov.contractReplies = []string{`{"criteria":["the tests pass after the source bug is fixed"],"needs_user_input":false,"question":"","user_options":[]}`}

	driveAgentCommands(t, m, m.startVerifiedRun("Run the Go tests, fix the bug in the source code, then rerun the tests.", nil))

	run := m.agentLoop.run
	runs := run.Cycles[0].Execution.TestsRun
	if len(runs) != 2 || runs[0].Passed || !runs[1].Passed {
		t.Fatalf("test runs = %+v, want a failed run then a passing rerun (is go test usable here?)", runs)
	}
	if run.Status != agent.DecisionDone || run.Cycle != 1 {
		t.Fatalf("status = %s after %d cycle(s) (%q), want done in one cycle", run.Status, run.Cycle, run.StopReason)
	}
	if v := run.Cycles[0].Verification; v == nil || strings.Contains(v.Summary, "deterministic test failure") {
		t.Fatalf("cycle 1 verification = %+v, want the semantic verifier's verdict", v)
	}
}
