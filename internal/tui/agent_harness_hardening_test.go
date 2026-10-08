package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestAgentPartialReadRejectsOptimisticVerifier(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "read", Name: tools.ToolReadFile, Arguments: `{"path":"large.txt","offset":1,"limit":400}`}}},
		agentScriptStep{text: "I read the entire file."},
		agentScriptStep{text: verifierJSONSatisfying("entire file read", "c1")},
	)
	prov.contractReplies = []string{`{"criteria":["Read the file large.txt"],"needs_user_input":false,"question":"","user_options":[]}`}
	root := t.TempDir()
	var content strings.Builder
	for line := 1; line <= 1500; line++ {
		fmt.Fprintf(&content, "line %d\n", line)
	}
	if err := os.WriteFile(root+"/large.txt", []byte(content.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 256)
	m.cfg.Agent.Yield.Enabled = false
	m.cfg.Agent.MaxCycles = 1
	driveAgentCommands(t, m, m.startVerifiedRun("Read all 1500 lines of large.txt.", nil))
	if m.agentLoop.run.Status == agent.DecisionDone {
		t.Fatal("partial 400-line read falsely completed a 1500-line task")
	}
	if m.agentLoop.run.Criteria[0].Status == agent.CriterionSatisfied {
		t.Fatal("optimistic verifier overrode missing full-file coverage")
	}
}

func TestAgentRequiredReadCannotPassWithoutTools(t *testing.T) {
	for _, mode := range []string{"off", "deterministic", "adaptive", "always"} {
		t.Run(mode, func(t *testing.T) {
			m, prov := configureAgentTestModel(t,
				agentScriptStep{text: "I read the whole file."},
				agentScriptStep{text: verifierJSONSatisfying("read complete", "c1")},
			)
			prov.contractReplies = []string{`{"criteria":["Read the file missing.txt"],"needs_user_input":false,"question":"","user_options":[]}`}
			m.cfg.Agent.Verifier.Mode = mode
			m.cfg.Agent.Yield.Enabled = true
			m.cfg.Agent.MaxCycles = 1
			driveAgentCommands(t, m, m.startVerifiedRun("Read missing.txt.", nil))
			if m.agentLoop.run.Status == agent.DecisionDone || m.agentLoop.run.Criteria[0].Status == agent.CriterionSatisfied {
				t.Fatalf("%s accepted an unobserved read: status=%s criteria=%+v", mode, m.agentLoop.run.Status, m.agentLoop.run.Criteria)
			}
		})
	}
}

func TestAgentEpisodeCeilingBoundsPreStreamAttempts(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"transport", context.DeadlineExceeded},
		{"native_fallback", fmt.Errorf("model does not support tools")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, prov := configureAgentTestModel(t,
				agentScriptStep{err: tc.err},
				agentScriptStep{text: "must not be requested"},
			)
			m.cfg.Agent.Yield.Enabled = true
			m.cfg.Agent.Yield.MaxEpisodeRequests = 1
			m.cfg.Network.Retry.Enabled = true
			m.cfg.Network.Retry.MaxAttempts = 3
			m.cfg.Network.Retry.Backoff = "1ns"
			m.toolsOn, m.toolsNative = true, true
			m.toolRunner = tools.NewRunner(t.TempDir(), 64)
			driveAgentCommands(t, m, m.startVerifiedRun("Inspect the workspace.", nil))
			// The independent oracle is actual provider invocations, including
			// rejected calls, not the checkpoint's after-the-fact counters.
			if len(prov.requests) != 2 {
				t.Fatalf("provider requests = %d, want contract + one executor attempt", len(prov.requests))
			}
			if m.agentLoop.run.Status != agent.DecisionBudgetExhausted {
				t.Fatalf("status = %q, want budget_exhausted", m.agentLoop.run.Status)
			}
			if cp := m.agentLoop.run.LatestCycle().Episode; cp.ExecutorRequests != 1 {
				t.Fatalf("attempts charged = %d, want 1", cp.ExecutorRequests)
			}
		})
	}
}

func TestAgentTerminalPreStreamFailureAccountsEveryAttempt(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{err: fmt.Errorf("model does not support tools")},
		agentScriptStep{err: context.DeadlineExceeded},
		agentScriptStep{err: fmt.Errorf("invalid model")},
	)
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests = 8
	m.cfg.Network.Retry.Enabled = true
	m.cfg.Network.Retry.MaxAttempts = 3
	m.cfg.Network.Retry.Backoff = "1ns"
	m.toolsOn, m.toolsNative = true, true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("Inspect the workspace.", nil))
	if len(prov.requests) != 4 {
		t.Fatalf("requests = %d, want contract + 3 failed attempts without replay", len(prov.requests))
	}
	if cp := m.agentLoop.run.LatestCycle().Episode; cp.ExecutorRequests != 3 {
		t.Fatalf("attempts charged = %d, want 3", cp.ExecutorRequests)
	}
	if m.agentLoop.run.Status != agent.DecisionFailed {
		t.Fatalf("status = %q, want failed", m.agentLoop.run.Status)
	}
}

func TestOrdinaryChatPreStreamRecoveryPreserved(t *testing.T) {
	for _, firstError := range []error{context.DeadlineExceeded, fmt.Errorf("model does not support tools")} {
		t.Run(firstError.Error(), func(t *testing.T) {
			m, prov := configureAgentTestModel(t,
				agentScriptStep{err: firstError}, agentScriptStep{text: "hello"},
			)
			m.agentOn = false
			m.cfg.Agent.Yield.Enabled = true
			m.cfg.Agent.Yield.MaxEpisodeRequests = 1
			m.cfg.Network.Retry.Enabled = true
			m.cfg.Network.Retry.MaxAttempts = 3
			m.cfg.Network.Retry.Backoff = "1ns"
			m.toolsOn, m.toolsNative = true, true
			m.toolRunner = tools.NewRunner(t.TempDir(), 64)
			driveAgentCommands(t, m, m.dispatch("Say hello.", nil))
			if len(prov.requests) != 2 || finalAnswer(m) != "hello" || m.errText != "" {
				t.Fatalf("ordinary recovery: requests=%d answer=%q error=%q", len(prov.requests), finalAnswer(m), m.errText)
			}
		})
	}
}

func TestAgentHarnessFullReadMatrix(t *testing.T) {
	fixture := fullReadFixture()
	for trial := 1; trial <= 5; trial++ {
		t.Run(fmt.Sprintf("trial_%d", trial), func(t *testing.T) {
			var steps []agentScriptStep
			for offset := 1; offset <= 1500; offset += 400 {
				steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{{
					ID: fmt.Sprintf("page-%d", offset), Name: tools.ToolReadFile,
					Arguments: fmt.Sprintf(`{"path":"large.txt","offset":%d,"limit":400}`, offset),
				}}}, agentScriptStep{text: "fixture-line-1500"})
			}
			steps = append(steps, agentScriptStep{text: verifierJSONSatisfying("last line matches the delivered result", "c2")})
			m, prov := configureAgentTestModel(t, steps...)
			prov.contractReplies = []string{`{"criteria":["Read the file large.txt","Report its exact last line"],"needs_user_input":false,"question":"","user_options":[]}`}
			root := t.TempDir()
			if err := os.WriteFile(root+"/large.txt", []byte(fixture.seedFiles["large.txt"]), 0o600); err != nil {
				t.Fatal(err)
			}
			m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
			m.toolRunner = tools.NewRunner(root, 256)
			m.cfg.Agent.Yield.Enabled = true
			m.cfg.Agent.Yield.MaxEpisodeRequests = 12
			m.cfg.Agent.Yield.MaxNudgesWithoutProgress = 2
			m.cfg.Context.MaxContextTokens = 65536
			started := time.Now()
			driveAgentCommands(t, m, m.startVerifiedRun(fixture.request, nil))
			row := liveAgentTrial(fixture, trial, m, len(prov.requests), time.Since(started), false)
			if !row.PostconditionPassed || row.FalseSuccess || row.FinalResult != string(agent.DecisionDone) {
				t.Fatalf("full-read oracle passed=%t status=%s reason=%q requests=%d criteria=%+v", row.PostconditionPassed, row.FinalResult, m.agentLoop.run.StopReason, len(prov.requests), m.agentLoop.run.Criteria)
			}
			if row.ProviderRequests != 10 || row.ToolExecuted != 4 || row.Cycles != 1 {
				t.Fatalf("unexpected inference/tool overhead: %+v", row)
			}
			t.Logf("trial=%d completion=true false_success=%t requests=%d tools=%d tokens=%d elapsed=%s", trial, row.FalseSuccess, row.ProviderRequests, row.ToolExecuted, row.PromptTokens+row.CompletionTokens, row.Elapsed)
		})
	}
}
