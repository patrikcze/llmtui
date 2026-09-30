package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
)

func TestTrivialContractRequest(t *testing.T) {
	for _, tc := range []struct {
		request string
		images  bool
		want    bool
	}{
		{request: "Say hello in one word.", want: true},
		{request: "What is the capital of France?", want: true},
		{request: "Explain what a mutex is", want: true},
		{request: "", want: false},
		{request: "Say hello.", images: true, want: false},
		{request: "What is the last line of big.log?", want: false},
		{request: "Read the README", want: false},
		{request: "Run the tests", want: false},
		{request: "Fix the bug in parser", want: false},
		{request: "Look at ./cmd/main", want: false},
		{request: "What does `go vet` do?", want: false},
		{request: "Check https://example.com for me", want: false},
		{request: "Say hi. Then tell me a joke.", want: false},
		{request: "Say hi\nand a joke", want: false},
		{request: "Name a color and a fruit", want: false},
		{request: strings.Repeat("why ", 50) + "?", want: false},
	} {
		if got := trivialContractRequest(tc.request, tc.images); got != tc.want {
			t.Errorf("trivialContractRequest(%q, images=%t) = %t, want %t", tc.request, tc.images, got, tc.want)
		}
	}
}

func TestSkipTrivialContractPinsOneCriterionWithoutContractRequest(t *testing.T) {
	for _, tc := range []struct {
		name         string
		enabled      bool
		request      string
		wantRequests int
		wantContract bool
	}{
		{name: "enabled, trivial", enabled: true, request: "Say hello in one word.", wantRequests: 2},
		{name: "disabled, trivial", enabled: false, request: "Say hello in one word.", wantRequests: 3, wantContract: true},
		{name: "enabled, tool intent", enabled: true, request: "Read the README and summarize it.", wantRequests: 3, wantContract: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, prov := configureAgentTestModel(t,
				agentScriptStep{text: "Hello."},
				agentScriptStep{text: verifierJSONSatisfying("answered", "c1")},
			)
			m.cfg.Agent.SkipTrivialContract = tc.enabled
			driveAgentCommands(t, m, m.startVerifiedRun(tc.request, nil))

			run := m.agentLoop.run
			if run.Status != agent.DecisionDone {
				t.Fatalf("status = %s (%s)", run.Status, run.StopReason)
			}
			if len(prov.requests) != tc.wantRequests {
				t.Fatalf("provider requests = %d, want %d", len(prov.requests), tc.wantRequests)
			}
			sawContract := false
			for _, req := range prov.requests {
				if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "You establish a task contract") {
					sawContract = true
				}
			}
			if sawContract != tc.wantContract {
				t.Fatalf("contract request sent = %t, want %t", sawContract, tc.wantContract)
			}
			if !tc.wantContract {
				if len(run.Criteria) != 1 || run.Criteria[0].Text != trivialContractCriterion {
					t.Fatalf("criteria = %+v, want the single local criterion", run.Criteria)
				}
				verifier := prov.requests[len(prov.requests)-1].Messages[1].Content
				if !strings.Contains(verifier, trivialContractCriterion) {
					t.Fatal("verifier did not receive the locally pinned criterion")
				}
			}
		})
	}
}
