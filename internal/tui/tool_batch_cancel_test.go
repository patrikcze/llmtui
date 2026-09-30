package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// driveWithCancelBeforeResults drives commands like driveAgentCommands, but
// presses Ctrl+C once, after a tool batch has finished its work and before
// its results message is delivered — the moment a completed mutation used
// to be lost.
func driveWithCancelBeforeResults(t *testing.T, m *Model, first tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{first}
	cancelled := false
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 200 {
			t.Fatal("command driver exceeded 200 messages")
		}
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		batchRunning := m.mcpBatchCancel != nil
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		if _, isResults := msg.(mcpToolResultsMsg); isResults && batchRunning && !cancelled {
			_, save := m.handleCtrlC()
			queue = append(queue, save)
			cancelled = true
		}
		_, next := m.Update(msg)
		if next != nil {
			queue = append(queue, next)
		}
	}
	if !cancelled {
		t.Fatal("no tool batch ran, so nothing was cancelled")
	}
}

// TestCancelledAgentBatchKeepsCompletedMutation is scenario S7 of the agent
// improvement plan as a regular test: an edit_file that completed before the
// user's Ctrl+C is kept in the session as a call/result pair and recorded in
// the cancelled run's partial execution, instead of being dropped.
func TestCancelledAgentBatchKeepsCompletedMutation(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "e1", Name: tools.ToolEditFile, Arguments: `{"path":"greeting.txt","old_text":"hello","new_text":"goodbye"}`}}},
		agentScriptStep{text: "must not run: the batch was cancelled"},
	)
	prov.contractReplies = []string{`{"criteria":["greeting.txt says goodbye"],"needs_user_input":false,"question":"","user_options":[]}`}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "greeting.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)

	driveWithCancelBeforeResults(t, m, m.startVerifiedRun("Change greeting.txt to say goodbye.", nil))

	if data, _ := os.ReadFile(filepath.Join(root, "greeting.txt")); strings.TrimSpace(string(data)) != "goodbye" {
		t.Fatalf("greeting.txt = %q, want the edit applied before the cancel", data)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (contract, executor): a cancelled batch must not continue", len(prov.requests))
	}
	run := m.agentLoop.run
	if run.Status != agent.DecisionCancelled || run.StopReason != "tool batch cancelled by the user" {
		t.Fatalf("run = {status:%s reason:%q}, want cancelled by the user", run.Status, run.StopReason)
	}
	if m.agentRunActive() || m.agentLoop.pendingBatchCancel != "" {
		t.Fatal("the run must be finalized once the cancelled batch's results arrive")
	}
	cycle := run.LatestCycle()
	if cycle == nil || cycle.Execution == nil || !cycle.Execution.Partial {
		t.Fatalf("latest cycle = %+v, want a partial execution record", cycle)
	}
	if got := cycle.Execution.ChangedFiles; len(got) != 1 || got[0] != "greeting.txt" {
		t.Fatalf("partial ChangedFiles = %v, want [greeting.txt]", got)
	}
	if got := agent.ExecutedToolCalls(cycle.Execution.ToolCalls); got != 1 || run.ToolCalls != 1 {
		t.Fatalf("executed calls = %d, run.ToolCalls = %d, want 1 and 1", got, run.ToolCalls)
	}

	callAt, resultAt := -1, -1
	for i, msg := range m.session.Messages {
		for _, call := range msg.ToolCalls {
			if call.ID == "e1" {
				callAt = i
			}
		}
		if msg.Role == provider.RoleTool && msg.ToolCallID == "e1" {
			resultAt = i
			if strings.Contains(msg.Content, "not executed") {
				t.Fatalf("result = %q, want the real edit result, not a synthetic one", msg.Content)
			}
		}
	}
	if callAt < 0 || resultAt != callAt+1 {
		t.Fatalf("call at %d, result at %d: want the edit call directly followed by its result", callAt, resultAt)
	}
}

// TestCancelledBatchMarksUnstartedCallNotExecuted cancels a two-call batch
// after call 1 completed and before call 2 started. Call 2 gets a synthetic
// not-executed result, so history stays paired, and the run ledger counts
// only call 1: the synthetic slot is not an executed call, not evidence, and
// never read coverage.
func TestCancelledBatchMarksUnstartedCallNotExecuted(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{
			{ID: "m1", Name: tools.JoinMCPToolName("tracker", "log_work"), Arguments: `{}`},
			{ID: "r2", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt"}`},
		}},
		agentScriptStep{text: "must not run: the batch was cancelled"},
	)
	prov.contractReplies = []string{`{"criteria":["Read the file a.txt"],"needs_user_input":false,"question":"","user_options":[]}`}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests, m.cfg.Agent.Yield.MaxNudgesWithoutProgress = 64, 2
	var mcpCalls int
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, CannedTools: []mcp.Tool{{Name: "log_work"}},
			CallFunc: func(string, json.RawMessage) (mcp.Result, error) {
				// The user presses Ctrl+C while call 1 is finishing.
				mcpCalls++
				m.handleCtrlC()
				return mcp.Result{Content: "logged"}, nil
			}}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "tracker", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Approve: "auto", Timeout: 5 * time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "tracker"); err != nil {
		t.Fatalf("connect: %v", err)
	}

	driveAgentCommands(t, m, m.startVerifiedRun("Log work, then read a.txt.", nil))

	if mcpCalls != 1 {
		t.Fatalf("MCP calls = %d, want 1", mcpCalls)
	}
	var r2 *provider.Message
	for i := range m.session.Messages {
		if msg := m.session.Messages[i]; msg.Role == provider.RoleTool && msg.ToolCallID == "r2" {
			r2 = &m.session.Messages[i]
		}
	}
	if r2 == nil || !strings.Contains(r2.Content, "not executed") || strings.Contains(r2.Content, "alpha") {
		t.Fatalf("r2 result = %+v, want a synthetic not-executed result that read nothing", r2)
	}
	run := m.agentLoop.run
	if run.Status != agent.DecisionCancelled {
		t.Fatalf("status = %s, want cancelled", run.Status)
	}
	execution := run.LatestCycle().Execution
	if execution == nil || !execution.Partial {
		t.Fatalf("execution = %+v, want a partial record", execution)
	}
	if got := agent.ExecutedToolCalls(execution.ToolCalls); got != 1 || run.ToolCalls != 1 {
		t.Fatalf("executed calls = %d, run.ToolCalls = %d, want only the completed call counted", got, run.ToolCalls)
	}
	foundR2 := false
	for _, record := range execution.ToolCalls {
		if record.ID != "r2" {
			continue
		}
		foundR2 = true
		if record.Status != agent.ActionBlocked || record.Succeeded {
			t.Fatalf("r2 record = %+v, want blocked and not succeeded", record)
		}
	}
	if !foundR2 {
		t.Fatalf("tool records = %+v, want a record for the not-started r2", execution.ToolCalls)
	}
	if !execution.NewEvidence {
		t.Fatal("the completed MCP call is new evidence; the partial record should say so")
	}
	if len(execution.ReadObservations) != 0 {
		t.Fatalf("read observations = %+v, want none: a call that never ran proves no coverage", execution.ReadObservations)
	}
	if ep := run.LatestCycle().Episode; ep != nil && len(ep.CoverageHighWater) != 0 {
		t.Fatalf("CoverageHighWater = %v, want untouched", ep.CoverageHighWater)
	}
}

// TestNotStartedResultsAreNeverObservedOrExecuted pins the classification
// runPlannedToolBatch applies after mergeResults.
func TestNotStartedResultsAreNeverObservedOrExecuted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	calls := []tools.Call{{ID: "w1", Tool: tools.ToolWriteFile, Path: "out.txt", Body: "x"}}
	msg := runPlannedToolBatch(ctx, tools.NewRunner(root, 64), nil, newToolBatchPlan(calls))().(mcpToolResultsMsg)
	if len(msg.results) != 1 || !isNotStartedResult(msg.results[0]) {
		t.Fatalf("results = %+v, want one not-started result", msg.results)
	}
	if msg.results[0].Meta.Effect != tools.EffectNone || msg.results[0].Meta.Outcome != tools.OutcomeCancelled {
		t.Fatalf("meta = %+v, want cancelled with no effect", msg.results[0].Meta)
	}
	if len(msg.observed) != 0 || msg.statuses[0] != agent.ActionBlocked {
		t.Fatalf("observed=%d status=%s, want unobserved and blocked", len(msg.observed), msg.statuses[0])
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); !os.IsNotExist(err) {
		t.Fatalf("out.txt exists (err=%v): a call must not start after cancellation", err)
	}
}

// TestPlainChatCancelledBatchVisibleToNextRequest covers the plain-chat half:
// after a cancelled batch, the next request carries the completed call and
// its real result, paired.
func TestPlainChatCancelledBatchVisibleToNextRequest(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "e1", Name: tools.ToolEditFile, Arguments: `{"path":"greeting.txt","old_text":"hello","new_text":"goodbye"}`}}},
		agentScriptStep{text: "must not run: the batch was cancelled"},
		agentScriptStep{text: "greeting.txt already says goodbye."},
	)
	m.agentOn = false
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "greeting.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)

	driveWithCancelBeforeResults(t, m, m.dispatch("Change greeting.txt to say goodbye.", nil))
	if len(prov.requests) != 1 {
		t.Fatalf("requests = %d, want 1: a cancelled batch must not continue", len(prov.requests))
	}
	driveAgentCommands(t, m, m.dispatch("Continue.", nil))

	next := prov.requests[len(prov.requests)-1]
	answered := map[string]bool{}
	callVisible := false
	for _, msg := range next.Messages {
		if msg.Role == provider.RoleTool {
			answered[msg.ToolCallID] = true
		}
		for _, call := range msg.ToolCalls {
			if call.ID == "e1" && strings.Contains(call.Arguments, `"new_text":"goodbye"`) {
				callVisible = true
			}
		}
	}
	if !callVisible || !answered["e1"] {
		t.Fatalf("next request: edit call visible=%t, result visible=%t; want both", callVisible, answered["e1"])
	}
}

// startHeldEditBatch starts an agent run whose executor emits one edit_file
// call and drives it until that tool batch is launched, returning the
// batch's command unexecuted so a test can choose when its results arrive.
func startHeldEditBatch(t *testing.T) (*Model, string, tea.Cmd) {
	t.Helper()
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "e1", Name: tools.ToolEditFile, Arguments: `{"path":"greeting.txt","old_text":"hello","new_text":"goodbye"}`}}},
	)
	prov.contractReplies = []string{`{"criteria":["greeting.txt says goodbye"],"needs_user_input":false,"question":"","user_options":[]}`}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "greeting.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	queue := []tea.Cmd{m.startVerifiedRun("Change greeting.txt to say goodbye.", nil)}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 200 {
			t.Fatal("batch never started")
		}
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, b...)
			continue
		}
		_, next := m.Update(msg)
		if next != nil && m.mcpBatchCancel != nil {
			return m, root, next
		}
		queue = append(queue, next)
	}
	t.Fatal("batch never started")
	return nil, "", nil
}

func sessionHasToolResult(m *Model, from int, id string) bool {
	for _, msg := range m.session.Messages[from:] {
		if msg.Role == provider.RoleTool && msg.ToolCallID == id {
			return true
		}
	}
	return false
}

// TestNewSubmissionSupersedesCancelledBatch keeps the superseded path: when
// the user submits again before a cancelled batch reports back, its late
// results are dropped as stale, and the cancelled run is finalized at once.
func TestNewSubmissionSupersedesCancelledBatch(t *testing.T) {
	m, _, batch := startHeldEditBatch(t)
	firstRun := m.agentLoop.run
	m.handleCtrlC()
	if !m.agentRunActive() || m.agentLoop.pendingBatchCancel == "" {
		t.Fatal("the run should wait for the cancelled batch's results")
	}

	m.agentOn = false // the new submission is ordinary chat
	m.input.SetValue("never mind")
	m.send()
	if firstRun.Status != agent.DecisionCancelled || m.agentLoop.pendingBatchCancel != "" {
		t.Fatalf("first run status = %s, want cancelled as soon as it is superseded", firstRun.Status)
	}
	before := len(m.session.Messages)
	if _, cmd := m.Update(batch()); cmd != nil {
		t.Error("stale results must not dispatch anything")
	}
	if sessionHasToolResult(m, before, "e1") {
		t.Fatal("a superseded batch's results must stay stale, not be appended")
	}
}

// TestAgentCancelCommandAfterCtrlCEndsRunAndKeepsResults: /agent cancel while
// a Ctrl+C'd batch is still finishing ends the run immediately (with the
// user's original reason), and the batch's late results are still kept in
// the session, paired, because nothing new superseded the batch.
func TestAgentCancelCommandAfterCtrlCEndsRunAndKeepsResults(t *testing.T) {
	m, _, batch := startHeldEditBatch(t)
	run := m.agentLoop.run
	m.handleCtrlC()
	cmdAgent(m, "cancel")
	if m.notice != "agent run cancelled" {
		t.Fatalf("notice = %q, want the run reported cancelled", m.notice)
	}
	if run.Status != agent.DecisionCancelled || run.StopReason != "tool batch cancelled by the user" || m.agentRunActive() {
		t.Fatalf("run = {status:%s reason:%q}, want cancelled with the Ctrl+C reason", run.Status, run.StopReason)
	}
	before := len(m.session.Messages)
	if _, cmd := m.Update(batch()); cmd != nil {
		t.Error("the cancelled batch's results must not dispatch anything")
	}
	if !sessionHasToolResult(m, before, "e1") {
		t.Fatal("the completed edit's result should be kept in the session")
	}
}

// cancelEditThenContinue runs S7 — an edit_file that completes before the
// user's Ctrl+C — and returns the model and provider ready for a next run.
func cancelEditThenContinue(t *testing.T, next ...agentScriptStep) (*Model, *scriptedAgentProvider) {
	t.Helper()
	steps := append([]agentScriptStep{
		{toolCalls: []provider.ToolCall{{ID: "e1", Name: tools.ToolEditFile, Arguments: `{"path":"greeting.txt","old_text":"hello","new_text":"goodbye"}`}}},
	}, next...)
	m, prov := configureAgentTestModel(t, steps...)
	prov.contractReplies = []string{
		`{"criteria":["greeting.txt says goodbye"],"needs_user_input":false,"question":"","user_options":[]}`,
		`{"criteria":["greeting.txt says goodbye"],"needs_user_input":false,"question":"","user_options":[]}`,
		`{"criteria":["report greeting.txt"],"needs_user_input":false,"question":"","user_options":[]}`,
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "greeting.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	driveWithCancelBeforeResults(t, m, m.startVerifiedRun("Change greeting.txt to say goodbye.", nil))
	return m, prov
}

// executorRequestAfter returns the first request after index from whose
// system prompt is not a contract or verifier prompt.
func executorRequestAfter(t *testing.T, prov *scriptedAgentProvider, from int) provider.ChatRequest {
	t.Helper()
	for _, req := range prov.requests[from:] {
		if len(req.Messages) > 0 && !strings.Contains(req.Messages[0].Content, "You establish a task contract") && len(req.Tools) > 0 {
			return req
		}
	}
	t.Fatalf("no executor request after %d of %d", from, len(prov.requests))
	return provider.ChatRequest{}
}

// TestNextAgentRunCarriesCancelledExchange is the S7 continuation: the run
// after a cancel sees the completed edit_file call and its real result as a
// native pair, followed by a text receipt that is also persisted in the
// run's start turns. The carry lasts exactly one run.
func TestNextAgentRunCarriesCancelledExchange(t *testing.T) {
	m, prov := cancelEditThenContinue(t,
		agentScriptStep{text: "greeting.txt already says goodbye."},
		agentScriptStep{text: verifierJSON("passed", "criteria satisfied", "", false, false)},
		agentScriptStep{text: "It says goodbye."},
		agentScriptStep{text: verifierJSON("passed", "criteria satisfied", "", false, false)},
	)
	before := len(prov.requests)
	driveAgentCommands(t, m, m.startVerifiedRun("Continue the previous task.", nil))
	req := executorRequestAfter(t, prov, before)

	callAt, resultAt, receiptAt := -1, -1, -1
	for i, msg := range req.Messages {
		for _, call := range msg.ToolCalls {
			if call.ID == "e1" && strings.Contains(call.Arguments, `"new_text":"goodbye"`) {
				callAt = i
			}
		}
		if msg.Role == provider.RoleTool && msg.ToolCallID == "e1" {
			resultAt = i
		}
		if msg.Role == provider.RoleAssistant && strings.HasPrefix(msg.Content, cancelledReceiptPrefix) {
			receiptAt = i
			if !strings.Contains(msg.Content, "edit_file greeting.txt: completed, changed the workspace") {
				t.Fatalf("receipt = %q, want the edit's outcome", msg.Content)
			}
		}
	}
	if callAt < 0 || resultAt != callAt+1 || receiptAt != resultAt+1 {
		t.Fatalf("call at %d, result at %d, receipt at %d: want call, result, receipt in order", callAt, resultAt, receiptAt)
	}
	turns := m.agentLoop.run.StartTurns
	if n := len(turns); n == 0 || !strings.HasPrefix(turns[n-1].Content, cancelledReceiptPrefix) {
		t.Fatalf("start turns = %+v, want the receipt persisted as the newest turn", turns)
	}

	before = len(prov.requests)
	driveAgentCommands(t, m, m.startVerifiedRun("What does greeting.txt say?", nil))
	for _, msg := range executorRequestAfter(t, prov, before).Messages {
		if msg.ToolCallID == "e1" || strings.HasPrefix(msg.Content, cancelledReceiptPrefix) {
			t.Fatalf("a later run still carries the cancelled exchange: %+v", msg)
		}
	}
}

// TestClearedSessionDropsCancelledExchange: after /history clear, the next
// run carries neither the native exchange nor the receipt.
func TestClearedSessionDropsCancelledExchange(t *testing.T) {
	m, prov := cancelEditThenContinue(t,
		agentScriptStep{text: "Done."},
		agentScriptStep{text: verifierJSON("passed", "criteria satisfied", "", false, false)},
	)
	m.session.Clear()
	before := len(prov.requests)
	driveAgentCommands(t, m, m.startVerifiedRun("Continue the previous task.", nil))
	for _, msg := range executorRequestAfter(t, prov, before).Messages {
		if msg.ToolCallID == "e1" || strings.HasPrefix(msg.Content, cancelledReceiptPrefix) {
			t.Fatalf("a cleared conversation still carries the cancelled exchange: %+v", msg)
		}
	}
}
