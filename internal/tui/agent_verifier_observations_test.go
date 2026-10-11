package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestVerifierSeesWhatTheCycleObserved: the semantic verifier judged pinned
// criteria from tool names, success flags and the executor's own claim —
// "Observations" was empty unless a criterion carried a local-read
// assessment — so it could not tell a supported answer from an unsupported
// one. A cycle's complete, successful read observations now fill the same
// bounded projection.
func TestVerifierSeesWhatTheCycleObserved(t *testing.T) {
	m, prov := callersRunModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "g1", Name: tools.ToolGrep, Arguments: `{"pattern":"parseConfig"}`}}},
		agentScriptStep{text: "parseConfig is called from main.go only."},
		agentScriptStep{text: verifierJSON("failed", "server.go also calls parseConfig", "", true, false)},
		agentScriptStep{text: "parseConfig is called from main.go and server.go."},
		agentScriptStep{text: verifierJSONSatisfying("both callers listed", "c1")},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("Find every caller of parseConfig in this project.", nil))

	var verifier string
	for _, req := range prov.requests {
		if text := requestText(req); strings.Contains(text, "You are an independent verifier") {
			verifier = text
			break
		}
	}
	if verifier == "" {
		t.Fatal("no verifier request was sent")
	}
	observations := verifier[strings.Index(verifier, `"Observations":[`):]
	for _, want := range []string{`source=\"grep(parseConfig)\"`, "server.go:2:func serve() { parseConfig() }"} {
		if !strings.Contains(observations, want) {
			t.Fatalf("verifier observations are missing %q:\n%s", want, observations)
		}
	}
}
