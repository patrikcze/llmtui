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

const callersCriterion = "every caller of parseConfig in the workspace is listed"

// callersRunModel is an adaptive-mode /agent run over a two-caller workspace
// whose contract pins one semantic criterion.
func callersRunModel(t *testing.T, steps ...agentScriptStep) (*Model, *scriptedAgentProvider) {
	t.Helper()
	m, prov := configureAgentTestModel(t, steps...)
	m.cfg.Agent.Verifier.Mode = "adaptive"
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests = 64
	root := t.TempDir()
	for name, body := range map[string]string{
		"main.go":   "package main\nfunc main() { parseConfig() }\n",
		"server.go": "package main\nfunc serve() { parseConfig() }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	prov.contractReplies = []string{`{"criteria":["` + callersCriterion + `"],"needs_user_input":false,"question":"","user_options":[]}`}
	return m, prov
}

func requestText(req provider.ChatRequest) string {
	var b strings.Builder
	for _, msg := range req.Messages {
		b.WriteString(msg.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// TestRejectedAnswerWithoutNextStepGetsCorrectiveCycle is the end-to-end
// regression for runs that stopped "failed" after one cycle: the executor
// answered without using a tool, the verifier rejected the answer but left
// recommended_next empty, and the retry gate ended the run. The unresolved
// criterion now drives one corrective cycle, and that cycle's executor
// request carries both the criterion as the objective and the verifier's
// reason, so the model can do the missing work.
func TestRejectedAnswerWithoutNextStepGetsCorrectiveCycle(t *testing.T) {
	m, prov := callersRunModel(t,
		agentScriptStep{text: "parseConfig is only called from main.go. Done."},
		agentScriptStep{text: verifierJSON("failed", "no search was performed; the callers were not listed", "", true, false)},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "g1", Name: tools.ToolGrep, Arguments: `{"pattern":"parseConfig"}`}}},
		agentScriptStep{text: "parseConfig is called from main.go and server.go."},
		agentScriptStep{text: verifierJSONSatisfying("both callers were found by grep", "c1")},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("Find every caller of parseConfig in this project.", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone || run.Cycle != 2 {
		t.Fatalf("status = %s after %d cycle(s) (%q), want done after a corrective second cycle", run.Status, run.Cycle, run.StopReason)
	}
	var cycle2 *provider.ChatRequest
	for i := range prov.requests {
		if strings.Contains(requestText(prov.requests[i]), agentContinueDirective) {
			cycle2 = &prov.requests[i]
			break
		}
	}
	if cycle2 == nil {
		t.Fatal("no cycle-2 executor request was sent")
	}
	text := requestText(*cycle2)
	for _, want := range []string{
		`Current bounded objective: "Address the next unresolved acceptance criterion: ` + callersCriterion + `"`,
		"verifier: failed: no search was performed; the callers were not listed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("cycle-2 executor request is missing %q", want)
		}
	}
	if len(cycle2.Tools) == 0 {
		t.Error("cycle-2 executor request offers no tools")
	}
	if run.ToolCalls != 1 {
		t.Errorf("tool calls = %d, want the corrective grep", run.ToolCalls)
	}
}

// TestRepeatedRejectionWithoutNextStepStopsHonestly: the corrective cycle is
// bounded. The same rejection in it ends the run as failed, naming the
// verifier's reason, instead of retrying again.
func TestRepeatedRejectionWithoutNextStepStopsHonestly(t *testing.T) {
	reject := verifierJSON("failed", "no search was performed; the callers were not listed", "", true, false)
	m, prov := callersRunModel(t,
		agentScriptStep{text: "parseConfig is only called from main.go. Done."},
		agentScriptStep{text: reject},
		agentScriptStep{text: "As I said, only main.go calls it."},
		agentScriptStep{text: reject},
		agentScriptStep{text: "must never be requested"},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("Find every caller of parseConfig in this project.", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionFailed || run.Cycle != 2 {
		t.Fatalf("status = %s after %d cycle(s), want failed after two", run.Status, run.Cycle)
	}
	if !strings.Contains(run.StopReason, "no search was performed") {
		t.Errorf("stop reason %q does not name the verifier's reason", run.StopReason)
	}
	for _, req := range prov.requests {
		if strings.Contains(requestText(req), "must never be requested") {
			t.Fatal("a third cycle was requested")
		}
	}
}
