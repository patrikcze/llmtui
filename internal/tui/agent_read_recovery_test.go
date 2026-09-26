package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestAgentYieldGrowingCoverageResetsNudgeBudget(t *testing.T) {
	var steps []agentScriptStep
	for i := range 4 {
		steps = append(steps,
			agentScriptStep{toolCalls: []provider.ToolCall{{ID: fmt.Sprintf("r%d", i), Name: tools.ToolReadFile, Arguments: fmt.Sprintf(`{"path":"big.log","offset":%d,"limit":100}`, i*100+1)}}},
			agentScriptStep{text: "I have already finished reading everything."},
		)
	}
	m, prov := configureAgentTestModel(t, steps...)
	prov.contractReplies = []string{`{"criteria":["Read the file big.log"],"needs_user_input":false,"question":"","user_options":[]}`}
	root := t.TempDir()
	var content strings.Builder
	for i := 1; i <= 400; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}
	if err := os.WriteFile(root+"/big.log", []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests = 64
	m.cfg.Agent.Yield.MaxNudgesWithoutProgress = 1
	driveAgentCommands(t, m, m.startVerifiedRun("Read big.log in full.", nil))
	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("status=%s, reads=%d; useful coverage must reset the nudge budget before evaluation", m.agentLoop.run.Status, len(m.agentLoop.execution.ReadObservations))
	}
	for i := range 3 {
		want := fmt.Sprintf(`"offset":%d,"limit":%d`, (i+1)*100+1, (3-i)*100)
		if !requestContains(prov.requests[i*2+2], want) {
			t.Fatalf("page %d result did not advance the next request hint to %s", i+1, want)
		}
	}
	for _, req := range prov.requests[3:] {
		for _, msg := range req.Messages {
			if msg.Role == provider.RoleAssistant && strings.Contains(msg.Content, "already finished") {
				t.Fatal("rejected completion claim reinforced in executor history")
			}
		}
	}
	visibleClaims := 0
	for _, msg := range m.session.Messages {
		if strings.Contains(msg.Content, "already finished") {
			visibleClaims++
		}
	}
	if visibleClaims != 4 {
		t.Fatalf("visible claims = %d; original transcript must be preserved", visibleClaims)
	}
}

func TestAgentYieldReadHintFitsToolContract(t *testing.T) {
	total := int64(1500)
	obs := []agent.ReadObservation{
		{Target: "a.txt", StartLine: 1, EndLine: 200, TotalLines: &total},
		{Target: "a.txt", StartLine: 801, EndLine: 1300, TotalLines: &total},
	}
	directive := buildAgentYieldDirective(agent.YieldDecision{Action: agent.YieldContinue, CriterionIDs: []string{"c1"}}, []agent.ExactReadObligation{{CriterionID: "c1", Target: "a.txt"}}, obs)
	if !strings.Contains(directive, `"offset":201,"limit":500`) {
		t.Fatalf("invalid read hint: %s", directive)
	}
}
