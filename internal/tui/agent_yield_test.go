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

// requestsContain reports whether any message in req mentions substr —
// used to find the composed AgentDirective section without depending on
// exactly which message index/role internal/prompt/compose.go places it in.
func requestContains(req provider.ChatRequest, substr string) bool {
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, substr) {
			return true
		}
	}
	return false
}

// TestAgentYieldContinuesSameCycleForMissingExactReadCriterion is the
// harness plan's §19 Test Case A ("Early yield"): a contract pins two exact
// -read criteria; the model reads the first target, then answers
// prematurely without reading the second. With agent.yield.enabled, the
// same StageExecutor episode continues with a bounded directive naming the
// still-missing criterion instead of jumping straight to verification —
// no new cycle, no verifier dispatch, no new user-facing message.
func TestAgentYieldContinuesSameCycleForMissingExactReadCriterion(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "read-a", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt"}`}}},
		agentScriptStep{text: "a.txt says hello."},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "read-b", Name: tools.ToolReadFile, Arguments: `{"path":"b.txt"}`}}},
		agentScriptStep{text: "a.txt says hello, and b.txt says world."},
	)
	prov.contractReplies = []string{`{"criteria":["Read the file a.txt","Read the file b.txt"],"needs_user_input":false,"question":"","user_options":[]}`}
	root := t.TempDir()
	if err := os.WriteFile(root+"/a.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/b.txt", []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests = 64
	m.cfg.Agent.Yield.MaxNudgesWithoutProgress = 2

	driveAgentCommands(t, m, m.startVerifiedRun("Read a.txt and b.txt and report their contents.", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone || run.Cycle != 1 {
		t.Fatalf("run = %+v, want same-cycle completion (no BeginCycle for the missing-criterion nudge)", run)
	}
	// contract + read-a + premature-answer + (yield-forced) read-b + final-answer.
	if len(prov.requests) != 5 {
		t.Fatalf("requests = %d, want 5 (contract, read-a, premature answer, forced read-b, final answer)", len(prov.requests))
	}
	forcedContinuation := prov.requests[3]
	if !requestContains(forcedContinuation, "Runtime execution state") || !requestContains(forcedContinuation, "b.txt") {
		t.Fatalf("continuation request did not carry the missing-criterion directive naming b.txt: %+v", forcedContinuation.Messages)
	}
	userTurns := 0
	for _, msg := range m.session.Messages {
		if msg.Role == provider.RoleUser {
			userTurns++
		}
	}
	if userTurns != 1 {
		t.Fatalf("session has %d user messages, want exactly 1 — a yield continuation must never be a new user-sent message", userTurns)
	}
	for _, c := range run.Criteria {
		if c.Status != agent.CriterionSatisfied {
			t.Fatalf("criterion %+v, want satisfied — both exact reads were eventually observed", c)
		}
	}
}

// TestAgentYieldStopsDeterministicallyWithoutProgress is Test Case C: a
// model that repeats an unhelpful text-only answer without ever satisfying
// the outstanding exact-read criterion must not be nudged forever. After
// MaxNudgesWithoutProgress continuations produced no new evidence, the
// episode stops as DecisionNoProgress instead of looping until some other
// budget accidentally catches it.
func TestAgentYieldStopsDeterministicallyWithoutProgress(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "still working on it"},
		agentScriptStep{text: "still working on it"},
		agentScriptStep{text: "still working on it"},
		agentScriptStep{text: "still working on it"},
	)
	prov.contractReplies = []string{`{"criteria":["Read the file a.txt"],"needs_user_input":false,"question":"","user_options":[]}`}
	root := t.TempDir()
	if err := os.WriteFile(root+"/a.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests = 64
	m.cfg.Agent.Yield.MaxNudgesWithoutProgress = 2

	driveAgentCommands(t, m, m.startVerifiedRun("Read a.txt and report its contents.", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionNoProgress {
		t.Fatalf("run status = %q, want %q", run.Status, agent.DecisionNoProgress)
	}
	if run.Cycle != 1 {
		t.Fatalf("cycle = %d, want 1: stopping on a stalled nudge budget must not start a fresh episode to reset the cap", run.Cycle)
	}
	// contract + 4 repeated premature answers (2 free continues beyond the
	// first discovery, then the 4th evaluation blocks instead of a 5th).
	if len(prov.requests) != 5 {
		t.Fatalf("requests = %d, want exactly 5 — the nudge budget must stop the episode deterministically, not loop", len(prov.requests))
	}
	if !strings.Contains(run.StopReason, "no relevant progress") {
		t.Fatalf("stop reason = %q, want an explicit no-progress explanation", run.StopReason)
	}
}

// TestAgentYieldContinuationNamesPreciseOffsetForPartialCoverage is the
// harness plan's §19 Test Case D ("Read recovery") adapted to Phase 4's
// coverage-aware proof: a model that reads only the first 100 of 220 lines
// of the criterion's target, then answers prematurely, must not have that
// partial read mechanically satisfy "Read the file big.log" (Phase 2 alone
// would have wrongly accepted it — see harness plan §4 finding #3). The
// forced same-cycle continuation must name the exact remaining
// offset/limit (agent.NextReadOffset), not a vague "use the tool again", and
// once the model reads the rest, coverage is complete and the run finishes
// in one cycle.
func TestAgentYieldContinuationNamesPreciseOffsetForPartialCoverage(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "read-partial", Name: tools.ToolReadFile, Arguments: `{"path":"big.log","offset":1,"limit":100}`}}},
		agentScriptStep{text: "big.log starts with line 1."},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "read-rest", Name: tools.ToolReadFile, Arguments: `{"path":"big.log","offset":101,"limit":120}`}}},
		agentScriptStep{text: "big.log has 220 lines in total."},
	)
	prov.contractReplies = []string{`{"criteria":["Read the file big.log"],"needs_user_input":false,"question":"","user_options":[]}`}
	root := t.TempDir()
	var content strings.Builder
	for i := 1; i <= 220; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}
	if err := os.WriteFile(root+"/big.log", []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests = 64
	m.cfg.Agent.Yield.MaxNudgesWithoutProgress = 2

	driveAgentCommands(t, m, m.startVerifiedRun("Read big.log and summarize it.", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone || run.Cycle != 1 {
		t.Fatalf("run = %+v, want same-cycle completion", run)
	}
	if len(prov.requests) != 5 {
		t.Fatalf("requests = %d, want 5 (contract, partial read, premature answer, forced precise-offset read, final answer)", len(prov.requests))
	}
	forced := prov.requests[3]
	if !requestContains(forced, `"path":"big.log","offset":101,"limit":120`) {
		t.Fatalf("continuation directive did not name the exact remaining window (offset 101, limit 120): %+v", forced.Messages)
	}
	for _, c := range run.Criteria {
		if c.Status != agent.CriterionSatisfied {
			t.Fatalf("criterion %+v, want satisfied — the union of both reads covers all 220 lines", c)
		}
	}
}

// TestAgentYieldDisabledPreservesExistingBehavior is Test Case O
// (mode compatibility): with agent.yield.enabled left at its default
// (false), a scenario that would otherwise trigger a same-cycle nudge must
// behave exactly like the pre-Phase-2 code path — straight to verification,
// using the existing verifier to notice the unresolved criterion, never a
// same-cycle continuation.
func TestAgentYieldDisabledPreservesExistingBehavior(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "a.txt says hello (never actually read)."},
		agentScriptStep{text: verifierJSON("failed", "a.txt was never read", "read a.txt as required", true, false)},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "read-a", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt"}`}}},
		agentScriptStep{text: "a.txt says hello."},
		agentScriptStep{text: verifierJSON("passed", "a.txt now observed", "", false, false)},
	)
	prov.contractReplies = []string{`{"criteria":["Read the file a.txt"],"needs_user_input":false,"question":"","user_options":[]}`}
	m.cfg.Agent.Verifier.Mode = "adaptive"
	root := t.TempDir()
	if err := os.WriteFile(root+"/a.txt", []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	// agent.yield.enabled is left at its zero value (false) deliberately.

	driveAgentCommands(t, m, m.startVerifiedRun("Read a.txt and report its contents.", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v, want eventual completion via the existing verifier-driven retry cycle", run)
	}
	if run.Cycle != 2 {
		t.Fatalf("cycle = %d, want 2: with yield disabled, the missing criterion must be caught by a fresh verifier-driven cycle, not a same-cycle nudge", run.Cycle)
	}
	if len(prov.requests) != 5 {
		t.Fatalf("requests = %d, want contract + premature answer + verifier(failed) + retry read + verifier(passed)", len(prov.requests))
	}
	for _, req := range prov.requests {
		if requestContains(req, "Runtime execution state") {
			t.Fatal("a yield directive must never appear when agent.yield.enabled is false")
		}
	}
}
