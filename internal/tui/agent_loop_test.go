package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	providermock "github.com/patrikcze/llmtui/internal/provider/mock"
	"github.com/patrikcze/llmtui/internal/tools"
)

type agentScriptStep struct {
	text      string
	toolCalls []provider.ToolCall
	err       error
	truncated bool
}

type scriptedAgentProvider struct {
	mu       sync.Mutex
	steps    []agentScriptStep
	requests []provider.ChatRequest
}

func (p *scriptedAgentProvider) Name() string { return "scripted-agent" }

func (p *scriptedAgentProvider) HealthCheck(context.Context) error { return nil }

func (p *scriptedAgentProvider) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: "test-model"}}, nil
}

func (p *scriptedAgentProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	if len(p.steps) == 0 {
		p.mu.Unlock()
		return nil, errors.New("script exhausted")
	}
	step := p.steps[0]
	p.steps = p.steps[1:]
	p.mu.Unlock()
	if step.err != nil {
		return nil, step.err
	}
	events := make(chan provider.ChatEvent, 2)
	if step.text != "" {
		events <- provider.ChatEvent{Type: provider.EventDelta, Delta: step.text}
	}
	events <- provider.ChatEvent{Type: provider.EventDone, ToolCalls: step.toolCalls, Truncated: step.truncated, Usage: &provider.Usage{
		PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
	}}
	close(events)
	return events, nil
}

func verifierJSON(verdict, summary, next string, retryable, changed bool) string {
	return `{"verdict":"` + verdict + `","summary":"` + summary + `","recommended_next":"` + next + `","retryable":` +
		map[bool]string{true: "true", false: "false"}[retryable] + `,"strategy_changed":` +
		map[bool]string{true: "true", false: "false"}[changed] + `,"confidence":0.9}`
}

func configureAgentTestModel(t *testing.T, steps ...agentScriptStep) (*Model, *scriptedAgentProvider) {
	t.Helper()
	m := newTestModel(t)
	prov := &scriptedAgentProvider{steps: append([]agentScriptStep(nil), steps...)}
	m.prov = prov
	m.model = "test-model"
	m.agentOn = true
	m.cfg.Agent.Verifier.Enabled = true
	m.cfg.Agent.Verifier.Timeout = "1s"
	m.cfg.Agent.Verifier.MaxTokens = 256
	m.cfg.Agent.Persist = false
	m.agentLoop.store = nil
	return m, prov
}

func driveAgentCommands(t *testing.T, m *Model, first tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{first}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 200 {
			t.Fatal("agent command driver exceeded 200 messages")
		}
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		_, next := m.Update(msg)
		if next != nil {
			queue = append(queue, next)
		}
	}
}

func TestVerifiedAgentOneCycleAndFreshVerifier(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 1 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("provider requests = %d, want executor + verifier", len(prov.requests))
	}
	verifyReq := prov.requests[1]
	if len(verifyReq.Messages) != 2 || len(verifyReq.Tools) != 0 || verifyReq.Stream {
		t.Fatalf("verifier request is not isolated: %+v", verifyReq)
	}
	if strings.Contains(verifyReq.Messages[1].Content, "You are a helpful local assistant") {
		t.Fatal("verifier received executor conversation history")
	}
	want := []string{"run_started", "rules_loaded", "objective_selected", "execution_started", "execution_completed", "verification_started", "verification_completed", "memory_written", "run_done"}
	if len(m.agentLoop.run.Events) != len(want) {
		t.Fatalf("events = %+v", m.agentLoop.run.Events)
	}
	for i, kind := range want {
		if m.agentLoop.run.Events[i].Kind != kind {
			t.Fatalf("event %d = %q, want %q", i, m.agentLoop.run.Events[i].Kind, kind)
		}
	}
}

// TestVerifiedAgentVerificationCarriesAvailableToolNames guards a real
// observed failure: a run asking for capability llmtui doesn't have (a live
// weather/events lookup, with only web_search/web_fetch available) got
// marked retryable forever because the verifier had no way to know that
// capability was never offered — it kept recommending a "weather API" that
// doesn't exist, spinning cycles until agent.max_cycles. The verifier
// request must carry the executor's actual tool names so it can tell
// "try differently" apart from "this system cannot do that at all".
func TestVerifiedAgentVerificationCarriesAvailableToolNames(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)

	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if len(prov.requests) != 2 {
		t.Fatalf("provider requests = %d, want executor + verifier", len(prov.requests))
	}
	verifyReq := prov.requests[1]
	if len(verifyReq.Messages) != 2 {
		t.Fatalf("verifier request = %+v", verifyReq)
	}
	evidence := verifyReq.Messages[1].Content
	for _, want := range []string{tools.ToolListDir, tools.ToolReadFile, tools.ToolWriteFile, tools.ToolRunCommand} {
		if !strings.Contains(evidence, `"`+want+`"`) {
			t.Errorf("verifier evidence missing tool name %q: %s", want, evidence)
		}
	}
}

// TestAgentContinueDirectiveDoesNotRenderAsUserMessage guards a display-only
// fix: some models (observed live on Qwen 3.6) can get stuck re-emitting
// tool-call syntax without ever completing a bounded objective, so the
// controller's canned "continue" turn ends up repeated verbatim many times.
// Rendered under the "you" label, that looked exactly like the human was
// stuck typing the same sentence over and over, which is misleading — the
// human never wrote it. The directive must still reach the model completely
// unchanged (it's what actually drives the next cycle); only its rendering
// in the transcript changes.
func TestAgentContinueDirectiveDoesNotRenderAsUserMessage(t *testing.T) {
	m := newTestModel(t)
	m.session.AddUser(agentContinueDirective)
	m.refreshViewport()
	view := m.viewport.View()
	if strings.Contains(view, agentContinueDirective) {
		t.Errorf("agent continue directive text leaked into the transcript: %s", view)
	}
	if !strings.Contains(view, "continuing to the next bounded objective") {
		t.Errorf("agent continue directive missing a controller status line: %s", view)
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleUser || last.Content != agentContinueDirective {
		t.Fatalf("message actually sent to the model must be unchanged: %+v", last)
	}
}

// TestRetryAfterAgentRunGoesThroughAgentLoop guards a real bug: retryLast()
// used to call the bare dispatch() path unconditionally, bypassing /agent
// on's run/cycle-stage machinery entirely. A retry sent while agentOn was
// still active never started a new verified run, so agentDirective() (which
// requires an active run in StageExecutor) had nothing to attach — matching
// the "that specific state and payload were not provided" confusion
// observed live after a completed run was retried.
func TestRetryAfterAgentRunGoesThroughAgentLoop(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
		// The retried run.
		agentScriptStep{text: "Implemented the bounded change again and observed success."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))
	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("first run = %+v", m.agentLoop.run)
	}
	firstRunID := m.agentLoop.run.ID
	m.lastUserMsg = "make the bounded change"

	cmd := m.retryLast()
	if cmd == nil {
		t.Fatal("retry should dispatch a request")
	}
	driveAgentCommands(t, m, cmd)

	if m.agentLoop.run.ID == firstRunID {
		t.Fatal("retry did not start a new verified run — it reused/ignored the ended run")
	}
	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("retried run = %+v", m.agentLoop.run)
	}
	if len(prov.requests) != 4 {
		t.Fatalf("provider requests = %d, want executor+verifier for each of two runs", len(prov.requests))
	}
	// The retried run's executor request must carry agentDirective's bounded
	// objective text — proof it went through startVerifiedRun/BeginCycle
	// rather than a bare dispatch that never attaches one.
	executorReq := prov.requests[2]
	if len(executorReq.Messages) == 0 || !strings.Contains(executorReq.Messages[0].Content, "Current bounded objective") {
		t.Fatalf("retried executor request missing agent directive: %+v", executorReq.Messages)
	}
}

func TestVerifiedAgentToolExecutionThenVerifierSuccess(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolListDir, Arguments: `{}`}}},
		agentScriptStep{text: "Listed the workspace and completed the objective."},
		agentScriptStep{text: verifierJSON("passed", "tool evidence supports completion", "", false, false)},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("inspect the workspace", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.ToolCalls != 1 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want executor + tool continuation + verifier", len(prov.requests))
	}
	cycle := m.agentLoop.run.LatestCycle()
	if cycle.Execution == nil || len(cycle.Execution.ToolCalls) != 1 || !cycle.Execution.ToolCalls[0].Succeeded {
		t.Fatalf("execution = %+v", cycle.Execution)
	}
}

// TestVerifiedAgentTruncatedExecutorReplyForcesRetry guards the wiring that
// treats a truncated executor turn as deterministic evidence: even when the
// verifier's own (possibly fooled) read of a garbled/incomplete reply claims
// "passed", ApplyDeterministicEvidence must force a retryable failure so the
// run doesn't accept a cut-off answer as done.
func TestVerifiedAgentTruncatedExecutorReplyForcesRetry(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "partial write attempt", truncated: true},
		agentScriptStep{text: verifierJSON("passed", "looks complete", "", false, false)},
		agentScriptStep{text: "completed the write this time"},
		agentScriptStep{text: verifierJSON("passed", "complete", "", false, false)},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("write the file", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 2 {
		t.Fatalf("run = %+v, want a forced retry cycle after the truncated reply", m.agentLoop.run)
	}
	first := m.agentLoop.run.Cycles[0]
	if first.Execution == nil || len(first.Execution.Errors) != 1 || first.Execution.Errors[0].Kind != agent.ErrorTruncated {
		t.Fatalf("first cycle execution errors = %+v, want one ErrorTruncated entry", first.Execution)
	}
	if first.Verification == nil || first.Verification.Verdict != agent.VerificationFailed || !first.Verification.Retryable {
		t.Fatalf("first cycle verification = %+v, want deterministic failed/retryable despite the verifier saying passed", first.Verification)
	}
	if len(prov.requests) != 4 {
		t.Fatalf("provider requests = %d, want executor+verifier for two cycles", len(prov.requests))
	}
}

func TestVerifiedAgentFailureChangesRetryObjective(t *testing.T) {
	next := "inspect the failing parser edge case and rerun its focused test"
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "First attempt completed."},
		agentScriptStep{text: verifierJSON("failed", "focused test still fails", next, true, true)},
		agentScriptStep{text: "Applied the changed strategy and the test now passes."},
		agentScriptStep{text: verifierJSON("passed", "focused test passes", "", false, false)},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("fix the parser", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 2 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if got := m.agentLoop.run.Cycles[1].Objective; got != next {
		t.Fatalf("retry objective = %q, want %q", got, next)
	}
	if len(prov.requests) != 4 || !strings.Contains(prov.requests[2].Messages[0].Content, next) {
		t.Fatal("changed retry objective was not loaded into the next executor context")
	}
}

func TestVerifiedAgentRepeatedFailureStops(t *testing.T) {
	next := "inspect a different deterministic edge case"
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Attempt one."},
		agentScriptStep{text: verifierJSON("failed", "same failure", next, true, true)},
		agentScriptStep{text: "Attempt two."},
		agentScriptStep{text: verifierJSON("failed", "same failure", next, true, true)},
	)
	m.cfg.Agent.MaxRepeatedFailures = 2
	driveAgentCommands(t, m, m.startVerifiedRun("fix repeated failure", nil))

	if m.agentLoop.run.Status != agent.DecisionFailed || m.agentLoop.run.RepeatedFailures != 2 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
}

// TestVerifiedAgentVerifierParseFailureRepeatedStops guards against a
// regression where verificationFailureResult nested a new
// "Retry the bounded objective..." prefix onto the objective every time the
// verifier's own response failed to parse. That growth made RecommendedNext
// (and therefore agent.failureKey) different on every cycle, so the
// repeated-failure dedup never fired and the run looped until an unrelated
// budget (cycles/elapsed/tokens) finally stopped it.
func TestVerifiedAgentVerifierParseFailureRepeatedStops(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Attempt one."},
		agentScriptStep{text: "not a json object at all"},
		agentScriptStep{text: "Attempt two."},
		agentScriptStep{text: "not a json object at all"},
	)
	m.cfg.Agent.MaxRepeatedFailures = 2
	driveAgentCommands(t, m, m.startVerifiedRun("fix repeated failure", nil))

	if m.agentLoop.run.Status != agent.DecisionFailed || m.agentLoop.run.RepeatedFailures != 2 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if len(m.agentLoop.run.Cycles) != 2 {
		t.Fatalf("cycles = %+v", m.agentLoop.run.Cycles)
	}
	first := m.agentLoop.run.Cycles[0].Verification
	second := m.agentLoop.run.Cycles[1].Verification
	if first == nil || second == nil {
		t.Fatalf("missing verification: first=%+v second=%+v", first, second)
	}
	if first.RecommendedNext != second.RecommendedNext {
		t.Fatalf("recommended_next grew across cycles: first=%q second=%q", first.RecommendedNext, second.RecommendedNext)
	}
	if n := strings.Count(second.RecommendedNext, "Retry the bounded objective"); n != 1 {
		t.Fatalf("recommended_next nested the retry prefix %d times: %q", n, second.RecommendedNext)
	}
}

func TestVerifiedAgentPermissionDenialStopsForUser(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "write-1", Name: tools.ToolWriteFile, Arguments: `{"path":"x.txt","content":"x"}`}}},
		agentScriptStep{text: "The write was denied; I cannot complete it."},
		agentScriptStep{text: verifierJSON("passed", "looks complete", "", false, false)},
		agentScriptStep{text: "Used the user's alternative and completed without the denied write."},
		agentScriptStep{text: verifierJSON("passed", "alternative satisfies the request", "", false, false)},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("write x.txt", nil))
	if len(m.pendingCalls) != 1 {
		t.Fatalf("pending calls = %d, want 1", len(m.pendingCalls))
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	driveAgentCommands(t, m, cmd)

	if m.agentLoop.run.Status != agent.DecisionNeedsUserInput {
		t.Fatalf("status = %q, want needs_user_input", m.agentLoop.run.Status)
	}
	cycle := m.agentLoop.run.LatestCycle()
	if cycle.Verification.Verdict != agent.VerificationBlocked {
		t.Fatalf("verdict = %+v", cycle.Verification)
	}
	runID := m.agentLoop.run.ID
	m.input.SetValue("skip the write and provide the content inline")
	driveAgentCommands(t, m, m.send())
	if m.agentLoop.run.ID != runID || m.agentLoop.run.Cycle != 2 || m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("user input did not resume the same run: %+v", m.agentLoop.run)
	}
}

func TestNonAgentChatPathRemainsUnchanged(t *testing.T) {
	m := newTestModel(t)
	prov := &scriptedAgentProvider{steps: []agentScriptStep{{text: "ordinary answer"}}}
	m.prov = prov
	m.agentOn = false
	m.input.SetValue("hello")
	driveAgentCommands(t, m, m.send())

	if m.agentLoop.run != nil {
		t.Fatal("ordinary chat unexpectedly created an agent run")
	}
	if len(prov.requests) != 1 {
		t.Fatalf("requests = %d, want one ordinary completion", len(prov.requests))
	}
	if strings.Contains(prov.requests[0].Messages[0].Content, "agent-cycle") {
		t.Fatal("ordinary chat received agent-cycle instructions")
	}
	if got := m.session.Messages[len(m.session.Messages)-1].Content; got != "ordinary answer" {
		t.Fatalf("answer = %q", got)
	}
}

func TestVerifiedAgentCompatibleWithExistingProviderMock(t *testing.T) {
	m := newTestModel(t)
	prov := providermock.New()
	prov.Delay = 0
	m.prov = prov
	m.agentOn = true
	m.cfg.Agent.Verifier.Enabled = false
	m.cfg.Agent.Persist = false
	m.agentLoop.store = nil
	driveAgentCommands(t, m, m.startVerifiedRun("exercise the offline provider", nil))
	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("status = %q, want done", m.agentLoop.run.Status)
	}
}

func TestToolSafetyFailureIsClassifiedForEscalation(t *testing.T) {
	result := tools.Result{Call: tools.Call{Tool: tools.ToolReadFile}, Err: errors.New(`path "../secret" is outside the workspace`)}
	if got := classifyToolError(result, false); got != agent.ErrorSafety {
		t.Fatalf("kind = %q, want safety constraint", got)
	}
}

type blockingAgentProvider struct {
	started chan struct{}
}

func (p *blockingAgentProvider) Name() string                      { return "blocking-agent" }
func (p *blockingAgentProvider) HealthCheck(context.Context) error { return nil }
func (p *blockingAgentProvider) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *blockingAgentProvider) Chat(ctx context.Context, _ provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAgentLifecycleCommandIsNonBlockingAndCancellationResponsive(t *testing.T) {
	m := newTestModel(t)
	prov := &blockingAgentProvider{started: make(chan struct{})}
	m.prov = prov
	m.agentOn = true
	m.cfg.Agent.Verifier.Enabled = true

	started := time.Now()
	cmd := m.startVerifiedRun("wait for provider", nil)
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("starting an agent run blocked the UI for %s", elapsed)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-prov.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("provider command did not unblock after cancellation")
	}
	if m.agentLoop.run.Status != agent.DecisionCancelled {
		t.Fatalf("status = %q, want cancelled", m.agentLoop.run.Status)
	}
}

func TestAgentElapsedBudgetCancelsExecution(t *testing.T) {
	m := newTestModel(t)
	m.prov = &blockingAgentProvider{started: make(chan struct{})}
	m.agentOn = true
	m.cfg.Agent.MaxElapsed = "20ms"
	driveAgentCommands(t, m, m.startVerifiedRun("wait beyond the run deadline", nil))
	if m.agentLoop.run.Status != agent.DecisionBudgetExhausted {
		t.Fatalf("status = %q, want budget_exhausted", m.agentLoop.run.Status)
	}
}

// TestVerifiedAgentLiveToolBudgetStopsExecutionBeforeCycleBoundary guards
// the fix for docs/architecture/v1-audit.md §4.2: agent.Decide()'s hard
// tool-call budget was previously only checked at a cycle boundary reached
// when the executor produces a turn with no tool calls. An executor that
// keeps requesting tools every turn never reached that boundary, so the
// documented run-level max_tool_calls ceiling (docs/agent-loop.md: "Agent
// mode adds a total run-level tool-call limit... further calls are
// rejected") could be exceeded well past the configured limit. This
// scripts an executor that always returns a tool call and asserts real
// tool execution stops at the limit itself, not merely that the run
// eventually reports budget_exhausted at some later cycle boundary.
func TestVerifiedAgentLiveToolBudgetStopsExecutionBeforeCycleBoundary(t *testing.T) {
	steps := make([]agentScriptStep, 0, 10)
	for i := 0; i < 10; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{
			{ID: fmt.Sprintf("call-%d", i), Name: tools.ToolListDir, Arguments: `{}`},
		}})
	}
	m, prov := configureAgentTestModel(t, steps...)
	m.cfg.Agent.MaxToolCalls = 3
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("keep listing the workspace", nil))

	if m.toolOK != 3 {
		t.Fatalf("toolOK = %d, want exactly 3 real executions (the live budget check should cap it)", m.toolOK)
	}
	if m.agentLoop.run.Status == agent.DecisionDone {
		t.Fatal("run should not report done: the executor never produced a final answer")
	}
	if m.agentLoop.run.Cycle > 1 {
		t.Fatalf("cycle = %d, want the live check to fire within cycle 1, before any cycle boundary is ever reached", m.agentLoop.run.Cycle)
	}
	if len(prov.requests) >= len(steps)+1 {
		t.Fatalf("provider requests = %d: all %d scripted steps were consumed without the live budget check ever intervening", len(prov.requests), len(steps))
	}
}

// TestLiveToolBudgetEnforcementCanBeDisabledViaConfig proves
// agent.enforce_budgets_live actually reverts to the pre-v1
// cycle-boundary-only check, per the rollback story in
// docs/architecture/v1-migration-plan.md: the same scenario
// TestVerifiedAgentLiveToolBudgetStopsExecutionBeforeCycleBoundary proves
// gets capped at the limit must run past it once the toggle is off,
// reproducing the original confirmed defect (v1-audit.md §4.2) on demand.
func TestLiveToolBudgetEnforcementCanBeDisabledViaConfig(t *testing.T) {
	steps := make([]agentScriptStep, 0, 10)
	for i := 0; i < 10; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{
			{ID: fmt.Sprintf("call-%d", i), Name: tools.ToolListDir, Arguments: `{}`},
		}})
	}
	m, _ := configureAgentTestModel(t, steps...)
	m.cfg.Agent.MaxToolCalls = 3
	m.cfg.Agent.EnforceBudgetsLive = false
	// Isolate this test to the live-budget toggle: the progress ledger
	// would independently block this same identical-call pattern after
	// its own threshold, which would mask what this test is checking.
	m.cfg.Tools.NoProgress.Enabled = false
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("keep listing the workspace", nil))

	if m.toolOK != 10 {
		t.Fatalf("toolOK = %d, want all 10 scripted calls to execute (live check disabled, only the cycle boundary would apply — and it's never reached here)", m.toolOK)
	}
}

// TestVerifiedAgentTruncatedToolCallIsNotExecuted is the /agent on
// counterpart to TestTruncatedNativeToolCallIsNotExecuted. Before this
// fix, recordAgentTruncation only recorded truncation as evidence for the
// *next* verification step — it did not itself stop the truncated call
// from being executed first. A write_file call cut off mid-arguments
// could already have written incomplete content to disk before the
// verifier ever saw the truncation evidence.
func TestVerifiedAgentTruncatedToolCallIsNotExecuted(t *testing.T) {
	m, prov := configureAgentTestModel(t, agentScriptStep{
		toolCalls: []provider.ToolCall{{ID: "call_1", Name: tools.ToolWriteFile, Arguments: `{"path":"out.txt","content":"cut off mid-conte`}},
		truncated: true,
	})
	root := t.TempDir()
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(root, 64)
	driveAgentCommands(t, m, m.startVerifiedRun("write the file", nil))

	if _, err := os.Stat(root + "/out.txt"); err == nil {
		t.Fatal("truncated write_file call must not have executed")
	}
	if m.agentLoop.run.Status == agent.DecisionDone {
		t.Fatal("run must not report done from a truncated tool call")
	}
	if len(prov.requests) != 1 {
		t.Fatalf("provider requests = %d, want exactly 1 (the run should stop, not retry blindly)", len(prov.requests))
	}
}

func TestAgentCancelCommandFinalizesActiveStream(t *testing.T) {
	m := newTestModel(t)
	m.prov = &blockingAgentProvider{started: make(chan struct{})}
	m.agentOn = true
	_ = m.startVerifiedRun("cancel this run", nil)
	if !m.thinking {
		t.Fatal("agent executor did not enter streaming state")
	}
	cmd := cmdAgent(m, "cancel")
	if cmd != nil {
		_ = cmd()
	}
	if m.thinking || m.agentLoop.run.Status != agent.DecisionCancelled {
		t.Fatalf("thinking=%v status=%q", m.thinking, m.agentLoop.run.Status)
	}
}

func TestQuitPersistsCancelledAgentRun(t *testing.T) {
	m := newTestModel(t)
	store := agent.NewMemoryStore()
	m.agentLoop.store = store
	m.agentOn = true
	_ = m.startVerifiedRun("persist on shutdown", nil)
	runID := m.agentLoop.run.ID
	if _, ok := m.quit()().(quitDoneMsg); !ok {
		t.Fatal("quit did not complete")
	}
	loaded, err := store.Load(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != agent.DecisionCancelled {
		t.Fatalf("persisted status = %q, want cancelled", loaded.Status)
	}
}
