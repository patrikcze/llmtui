package tui

import (
	"context"
	"errors"
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
	events <- provider.ChatEvent{Type: provider.EventDone, ToolCalls: step.toolCalls, Usage: &provider.Usage{
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
