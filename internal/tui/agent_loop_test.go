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
	// These tests exercise the semantic-verify flow explicitly, so pin the
	// legacy always-verify policy; adaptive-mode behavior has its own tests.
	m.cfg.Agent.Verifier.Mode = "always"
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

// A retry belongs to the current verified run, not to an older completed run
// that happens to remain visible in the transcript. This guards the LM Studio
// failure where a weather retry received an earlier benchmark's controller
// turn and summary, then started executing the benchmark again.
func TestVerifiedAgentRetryExcludesPriorRunContext(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Weather answer without sufficient evidence."},
		agentScriptStep{text: verifierJSON("failed", "weather evidence is missing", "use an alternative weather API", true, true)},
		agentScriptStep{text: "Weather answer backed by the alternative API."},
		agentScriptStep{text: verifierJSON("passed", "weather evidence verified", "", false, false)},
	)
	m.session.AddUser("OLD BENCHMARK INSTRUCTIONS")
	m.session.AddUser(agentContinueDirective)
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: "old-call", Name: tools.ToolListDir, Arguments: `{}`,
		}},
	})
	m.session.AddMessage(provider.Message{
		Role: provider.RoleTool, ToolCallID: "old-call", ToolName: tools.ToolListDir,
		Content: "old-benchmark-output",
	})
	m.session.AddAssistant("OLD BENCHMARK COMPLETE")
	m.summary = "OLD BENCHMARK SUMMARY"

	driveAgentCommands(t, m, m.startVerifiedRun("get current weather", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 2 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if len(prov.requests) != 4 {
		t.Fatalf("provider requests = %d, want executor/verifier twice", len(prov.requests))
	}
	first := prov.requests[0]
	var firstContext strings.Builder
	for _, message := range first.Messages {
		firstContext.WriteString(message.Content)
		firstContext.WriteByte('\n')
	}
	for _, stale := range []string{agentContinueDirective, "old-benchmark-output", "OLD BENCHMARK SUMMARY"} {
		if strings.Contains(firstContext.String(), stale) {
			t.Fatalf("new run leaked completed agent machinery %q:\n%s", stale, firstContext.String())
		}
	}
	for _, conversational := range []string{"OLD BENCHMARK INSTRUCTIONS", "OLD BENCHMARK COMPLETE"} {
		if !strings.Contains(firstContext.String(), conversational) {
			t.Fatalf("new run lost conversational history %q:\n%s", conversational, firstContext.String())
		}
	}
	retry := prov.requests[2]
	var context strings.Builder
	for _, message := range retry.Messages {
		context.WriteString(message.Content)
		context.WriteByte('\n')
	}
	got := context.String()
	for _, stale := range []string{
		"OLD BENCHMARK INSTRUCTIONS", "OLD BENCHMARK COMPLETE", "OLD BENCHMARK SUMMARY", "old-benchmark-output",
	} {
		if strings.Contains(got, stale) {
			t.Fatalf("retry leaked prior-run context %q:\n%s", stale, got)
		}
	}
	for _, current := range []string{"get current weather", "use an alternative weather API"} {
		if !strings.Contains(got, current) {
			t.Fatalf("retry lost current-run context %q:\n%s", current, got)
		}
	}
}

// A completed cycle's own raw tool-call/tool-result exchange must not be
// resent verbatim once a later cycle's executor request is built — the
// executor already has that cycle's outcome via the bounded run.Memory
// recap in its system prompt (agentDirective). Resending it anyway only
// grows context every cycle without adding information, which is exactly
// what large web_fetch/read_file/run_command outputs from an earlier cycle
// would otherwise keep doing across every subsequent cycle of a multi-cycle
// run. The CURRENT cycle's own tool call/result must still be sent in full.
func TestVerifiedAgentProjectsCompletedCycleToolTrafficNotCurrentCycle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/cycle-one-marker.txt", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolListDir, Arguments: `{}`}}},
		agentScriptStep{text: "Listed the workspace but evidence was inconclusive."},
		agentScriptStep{text: verifierJSON("failed", "not enough evidence yet", "try a different approach", true, true)},
		agentScriptStep{text: "Completed using a different approach."},
		agentScriptStep{text: verifierJSON("passed", "objective satisfied", "", false, false)},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(dir, 64)
	driveAgentCommands(t, m, m.startVerifiedRun("inspect the workspace", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 2 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	// Cycle 1: tool call + tool continuation + verifier = 3 requests.
	// Cycle 2: executor + verifier = 2 requests.
	if len(prov.requests) != 5 {
		t.Fatalf("provider requests = %d, want 5", len(prov.requests))
	}
	cycle2Request := prov.requests[3]
	var hasToolMessage, hasToolCallMessage bool
	var got strings.Builder
	for _, message := range cycle2Request.Messages {
		got.WriteString(message.Content)
		got.WriteByte('\n')
		if message.Role == provider.RoleTool {
			hasToolMessage = true
		}
		if len(message.ToolCalls) > 0 {
			hasToolCallMessage = true
		}
	}
	if hasToolMessage || hasToolCallMessage {
		t.Fatalf("cycle 2 request still carries cycle 1's raw tool exchange: %+v", cycle2Request.Messages)
	}
	if strings.Contains(got.String(), "cycle-one-marker.txt") {
		t.Fatalf("cycle 2 request leaked cycle 1's raw tool result content:\n%s", got.String())
	}
	// agentContinueDirective is expected here: it's cycle 2's OWN triggering
	// message (startNextAgentCycle dispatches it to begin cycle 2), not
	// leftover from cycle 1 — cycle 1 was triggered by the real user request.
	for _, current := range []string{"inspect the workspace", "try a different approach", agentContinueDirective} {
		if !strings.Contains(got.String(), current) {
			t.Fatalf("cycle 2 request lost current-run context %q:\n%s", current, got.String())
		}
	}
	// Cycle 1's raw tool exchange is gone (asserted above), but cycle 2 must
	// still be told cycle 1 already ran list_dir and it succeeded — without
	// this, a retry cycle has no way to avoid blindly repeating an action
	// whose outcome it already knows (the real regression this guards:
	// observed as a run re-fetching an already-successful URL and
	// re-hitting an already-failed one across consecutive cycles).
	if !strings.Contains(got.String(), "tried: "+tools.ToolListDir+" succeeded") {
		t.Fatalf("cycle 2 request lost the prior cycle's tool-call recap:\n%s", got.String())
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
		agentScriptStep{text: "still not a json object"},
		agentScriptStep{text: "Attempt two."},
		agentScriptStep{text: "not a json object at all"},
		agentScriptStep{text: "still not a json object"},
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

func TestVerifiedAgentRepairsVerifierFormatWithoutRepeatingExecutor(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Completed the requested work."},
		agentScriptStep{text: `{"verdict":"passed","summary":"checked","retryable":false,"confidence":0.9,"proposed_criteria":[{"criterion":"complete the work"}]}`},
		agentScriptStep{text: `{"verdict":"passed","summary":"checked","retryable":false,"confidence":0.9,"proposed_criteria":["complete the work"],"criteria":[{"id":"c1","status":"satisfied"}]}`},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("complete the work", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || len(m.agentLoop.run.Cycles) != 1 {
		t.Fatalf("run = %+v, want one completed cycle", m.agentLoop.run)
	}
	if len(prov.requests) != 3 {
		t.Fatalf("requests = %d, want executor plus initial and repaired verifier requests", len(prov.requests))
	}
	if !strings.Contains(prov.requests[2].Messages[0].Content, "FORMAT REPAIR") {
		t.Fatalf("third request is not the verifier format repair: %+v", prov.requests[2])
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

// TestVerifiedAgentClarifyingQuestionStopsForUser exercises the verifier's
// own semantic detection of a clarifying question, distinct from
// TestVerifiedAgentPermissionDenialStopsForUser above (which fires only on a
// denied tool approval). Observed in a real run: the executor asked "Which
// source would you like me to check first?" but nothing inspected the
// executor's response text, so the run just ground through retries to
// DecisionFailed instead of surfacing the question. The verifier response is
// written as a raw JSON literal, not through the shared verifierJSON()
// helper, because that helper has several existing call sites and adding a
// needs_user_input parameter would touch all of them for this one test.
func TestVerifiedAgentClarifyingQuestionStopsForUser(t *testing.T) {
	const question = "Which source would you like me to check first: AccuWeather or the Met Office?"
	verifierReply := `{"verdict":"inconclusive","summary":"` + question + `","retryable":true,"confidence":0.8,"needs_user_input":true}`
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: question},
		agentScriptStep{text: verifierReply},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("check the weather", nil))

	if m.agentLoop.run.Status != agent.DecisionNeedsUserInput {
		t.Fatalf("status = %q, want needs_user_input", m.agentLoop.run.Status)
	}
	cycle := m.agentLoop.run.LatestCycle()
	if !cycle.Verification.NeedsUserInput {
		t.Fatalf("verification = %+v, want NeedsUserInput true", cycle.Verification)
	}
	if !strings.Contains(m.errText, question) {
		t.Fatalf("errText = %q, want it to surface the executor's actual question, not a generic message", m.errText)
	}
}

// TestVerifiedAgentQuestionWithOptionsOpensPickerAndResumes extends
// TestVerifiedAgentClarifyingQuestionStopsForUser: when the verifier also
// extracts discrete choices into user_options (matching a real run where
// the executor wrote "1. Prague / 2. Humpolec / 3. Brno" as plain prose),
// the TUI must present them as a pickable overlay instead of only the
// free-text errText path, and selecting one must resume the run exactly
// like typing that same text would.
func TestVerifiedAgentQuestionWithOptionsOpensPickerAndResumes(t *testing.T) {
	const question = "Which city would you like me to check first?"
	verifierReply := `{"verdict":"inconclusive","summary":"` + question + `","retryable":true,"confidence":0.8,` +
		`"needs_user_input":true,"user_options":["Prague","Humpolec","Brno"]}`
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: question},
		agentScriptStep{text: verifierReply},
		agentScriptStep{text: "Checked Humpolec's weather."},
		agentScriptStep{text: verifierJSON("passed", "weather reported", "", false, false)},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("check the weather", nil))

	if m.agentLoop.run.Status != agent.DecisionNeedsUserInput {
		t.Fatalf("status = %q, want needs_user_input", m.agentLoop.run.Status)
	}
	if m.pickerKind != pickerAgentQuestion {
		t.Fatalf("pickerKind = %v, want pickerAgentQuestion", m.pickerKind)
	}
	if m.pickerHeader != question {
		t.Fatalf("pickerHeader = %q, want %q", m.pickerHeader, question)
	}
	want := []string{"Prague", "Humpolec", "Brno"}
	if len(m.pickerItems) != len(want) {
		t.Fatalf("pickerItems = %v, want %v", m.pickerItems, want)
	}
	for i := range want {
		if m.pickerItems[i] != want[i] {
			t.Fatalf("pickerItems[%d] = %q, want %q", i, m.pickerItems[i], want[i])
		}
	}

	runID := m.agentLoop.run.ID
	_, downCmd := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	driveAgentCommands(t, m, downCmd)
	if m.pickerIdx != 1 {
		t.Fatalf("pickerIdx = %d, want 1 (Humpolec) after one down-arrow", m.pickerIdx)
	}
	_, enterCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	driveAgentCommands(t, m, enterCmd)

	if m.pickerKind != pickerNone {
		t.Fatalf("pickerKind = %v, want pickerNone after selection", m.pickerKind)
	}
	if m.agentLoop.run.ID != runID || m.agentLoop.run.Cycle != 2 || m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("selecting an option did not resume the same run: %+v", m.agentLoop.run)
	}
}

// TestPendingToolApprovalOutranksAgentQuestionPicker locks in the
// CLAUDE.md safety invariant that a pending tool approval must always be
// what the next keypress resolves, even if some other input-owning overlay
// state exists at the same time. Decide() cannot structurally produce this
// combination today (a cycle's tool calls always resolve before
// verification runs), but app.go's top-level Update must still fail safe
// if that assumption is ever violated by a future bug — an unrelated
// keypress must never silently resolve the agent's pending tool approval.
func TestPendingToolApprovalOutranksAgentQuestionPicker(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.pendingCalls = []tools.Call{{Tool: tools.ToolListDir, Path: "."}}
	m.openAgentQuestionPicker("pick a city", []string{"Prague", "Humpolec", "Brno"})

	// Enter resolves the tool approval at its default row (approve), which
	// synchronously clears pendingCalls — the point under test is that this
	// is what actually ran, not the picker's Enter-selects-an-option
	// handling. If the picker had won instead, pendingCalls would remain
	// untouched (the picker's Enter branch never looks at it) while
	// pickerKind would already be pickerNone; a resolved approval with the
	// question picker still open shows the approval path ran first.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.pendingCalls) != 0 {
		t.Fatalf("tool approval was not resolved: pendingCalls=%d", len(m.pendingCalls))
	}
	if m.pickerKind != pickerAgentQuestion {
		t.Fatalf("pickerKind = %v, want pickerAgentQuestion untouched by the approval keypress", m.pickerKind)
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

// Adaptive mode, first cycle: even when execution is mechanically complete
// (a tool call ran and succeeded), the run's FIRST cycle must still call the
// semantic verifier. "Every tool I called happened to succeed" is not the
// same claim as "I did everything the request asked for" — skipping
// semantic verification on cycle 1 is exactly how a multi-part request can
// reach "done" having silently dropped a requirement the executor never
// attempted (see .claude/tasks/agent-loop-establish-cycle-criteria-fix.md
// for the observed real-world case: a file-write step that never ran still
// exited via this shortcut with full confidence). The verifier's own
// same-turn establish-and-resolve path (see the "resolve criteria on the
// establishing cycle" fix) is what keeps this a one-cycle UX for a genuinely
// simple objective, not a shortcut around ever checking it.
func TestAdaptiveMechanicallyCompleteOnFirstCycleStillVerifiesSemantically(t *testing.T) {
	verifierPass := `{"verdict":"passed","summary":"workspace inspected as requested","retryable":false,"confidence":0.9,` +
		`"proposed_criteria":["list the workspace contents"],` +
		`"criteria":[{"id":"c1","status":"satisfied","note":"listed"}]}`
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolListDir, Arguments: `{}`}}},
		agentScriptStep{text: "Listed the workspace and completed the objective."},
		agentScriptStep{text: verifierPass},
	)
	m.cfg.Agent.Verifier.Mode = "adaptive"
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("inspect the workspace", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 1 {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	// Tool call + tool continuation + semantic verifier: the first-cycle
	// guard must force the verifier request even though execution alone
	// was already mechanically complete.
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want executor+continuation+verifier on cycle 1", len(prov.requests))
	}
	verification := m.agentLoop.run.LatestCycle().Verification
	if verification == nil || verification.Verdict != agent.VerificationPassed {
		t.Fatalf("verification = %+v", verification)
	}
	if len(m.agentLoop.run.Criteria) != 1 || m.agentLoop.run.Criteria[0].Status != agent.CriterionSatisfied {
		t.Fatalf("criteria = %+v, want the establishing cycle's same-turn resolution to pin and satisfy c1", m.agentLoop.run.Criteria)
	}
}

// Adaptive mode: once a run's first cycle has genuinely had its chance at
// semantic verification — even if that cycle took the deterministic-failure
// shortcut, which never calls the verifier at all — a later mechanically
// complete cycle may still skip semantic verification. The first-cycle
// guard above protects only cycle 1 itself, not every cycle a run happens
// to establish zero criteria in.
func TestAdaptiveMechanicallyCompleteSkipsSemanticVerifierAfterFirstCycle(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "partial write attempt", truncated: true},
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolListDir, Arguments: `{}`}}},
		agentScriptStep{text: "Listed the workspace and completed the objective."},
	)
	m.cfg.Agent.Verifier.Mode = "adaptive"
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("inspect the workspace", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 2 {
		t.Fatalf("run = %+v, want a deterministic-failure cycle 1 then a mechanically complete cycle 2", m.agentLoop.run)
	}
	// Cycle 1: executor only (truncated, deterministic failure, no verifier).
	// Cycle 2: executor + tool continuation, mechanically complete, no
	// verifier call — the fast path still applies once cycle 1 is behind us.
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want executor(x1)+executor+continuation, no verifier call", len(prov.requests))
	}
	second := m.agentLoop.run.Cycles[1]
	if second.Verification == nil || second.Verification.Verdict != agent.VerificationPassed {
		t.Fatalf("second cycle verification = %+v", second.Verification)
	}
	if m.agentLoop.run.HasCriteria() {
		t.Fatalf("criteria = %+v, want none pinned: neither cycle ever called the semantic verifier", m.agentLoop.run.Criteria)
	}
}

// Adaptive mode: a conclusive deterministic failure (truncated executor
// reply) skips the semantic verifier entirely instead of paying for a
// verdict that would be discarded by the deterministic override.
func TestAdaptiveDeterministicFailureSkipsSemanticVerifier(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "partial write attempt", truncated: true},
		agentScriptStep{text: "completed the write this time"},
		agentScriptStep{text: verifierJSON("passed", "complete", "", false, false)},
	)
	m.cfg.Agent.Verifier.Mode = "adaptive"
	driveAgentCommands(t, m, m.startVerifiedRun("write the file", nil))

	if m.agentLoop.run.Status != agent.DecisionDone || m.agentLoop.run.Cycle != 2 {
		t.Fatalf("run = %+v, want a deterministic retry then semantic completion", m.agentLoop.run)
	}
	first := m.agentLoop.run.Cycles[0]
	if first.Verification == nil || first.Verification.Verdict != agent.VerificationFailed || !first.Verification.Retryable {
		t.Fatalf("first cycle verification = %+v", first.Verification)
	}
	// Cycle 1: executor only (deterministic failure, no verifier).
	// Cycle 2: executor + semantic verifier for the prose-only answer.
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3 (no verifier call for the truncated cycle)", len(prov.requests))
	}
}

// Adaptive mode: a prose-only answer has no mechanical evidence, so the
// semantic verifier is still consulted — and its proposed criteria are
// pinned on the run and persist across cycles with stable IDs.
func TestAdaptiveProseAnswerVerifiedAndCriteriaPersistAcrossCycles(t *testing.T) {
	proposeAndFail := `{"verdict":"failed","summary":"report criterion unresolved","retryable":true,` +
		`"recommended_next":"produce the report","strategy_changed":true,"confidence":0.8,` +
		`"proposed_criteria":["gather data","produce report"],` +
		`"criteria":[{"id":"c1","status":"satisfied","note":"data gathered"}]}`
	satisfyRest := `{"verdict":"passed","summary":"report produced","retryable":false,"confidence":0.9,` +
		`"criteria":[{"id":"c2","status":"satisfied"}]}`
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Gathered the data and reported findings."},
		agentScriptStep{text: proposeAndFail},
		agentScriptStep{text: "Produced the report."},
		agentScriptStep{text: satisfyRest},
	)
	m.cfg.Agent.Verifier.Mode = "adaptive"
	driveAgentCommands(t, m, m.startVerifiedRun("gather data and produce a report", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone || run.Cycle != 2 {
		t.Fatalf("run = %+v", run)
	}
	if len(prov.requests) != 4 {
		t.Fatalf("provider requests = %d, want executor+verifier per cycle for prose-only answers", len(prov.requests))
	}
	if len(run.Criteria) != 2 || run.Criteria[0].ID != "c1" || run.Criteria[1].ID != "c2" {
		t.Fatalf("criteria = %+v, want the pinned set stable across cycles", run.Criteria)
	}
	for _, criterion := range run.Criteria {
		if criterion.Status != agent.CriterionSatisfied {
			t.Fatalf("criterion %s = %q, want satisfied at completion", criterion.ID, criterion.Status)
		}
	}
	// The second verification request must carry the pinned criteria and the
	// cumulative ledger, proving the verifier sees cross-cycle state.
	secondVerify := prov.requests[3].Messages[1].Content
	for _, want := range []string{"gather data", "produce report", `"EstablishCriteria":false`} {
		if !strings.Contains(secondVerify, want) {
			t.Fatalf("second verification evidence missing %q: %s", want, secondVerify)
		}
	}
}

// Mode "off" trusts the executor: no verifier request, and the run reports
// done with an explicitly unverified summary.
func TestVerifierModeOffSkipsAllVerification(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Answered directly."},
	)
	m.cfg.Agent.Verifier.Mode = "off"
	driveAgentCommands(t, m, m.startVerifiedRun("answer the question", nil))

	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("run = %+v", m.agentLoop.run)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("provider requests = %d, want executor only", len(prov.requests))
	}
	verification := m.agentLoop.run.Cycles[0].Verification
	if verification == nil || !strings.Contains(verification.Summary, "not verified") {
		t.Fatalf("verification = %+v, want an explicitly unverified summary", verification)
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

// TestToolCallDetailNeverLeaksCommandArgumentsOrMCPArgs guards the
// deliberate asymmetry in what toolCallDetail extracts: URLs/paths/queries
// are safe to echo back to the executor across cycles, but a run_command's
// full command line can carry an inline secret a user or model typed
// directly (e.g. curl -H "Authorization: Bearer ..."), and MCP arguments
// are arbitrary per-server JSON — neither may appear in agent run memory.
func TestToolCallDetailNeverLeaksCommandArgumentsOrMCPArgs(t *testing.T) {
	cases := []struct {
		name string
		call tools.Call
		want string
	}{
		{"web_fetch", tools.Call{Tool: tools.ToolWebFetch, Path: "https://weather.com/cz/prague"}, "https://weather.com/cz/prague"},
		{"web_search", tools.Call{Tool: tools.ToolWebSearch, Body: "current weather Prague"}, "current weather Prague"},
		{"read_file", tools.Call{Tool: tools.ToolReadFile, Path: "internal/config/config.go"}, "internal/config/config.go"},
		{"write_file", tools.Call{Tool: tools.ToolWriteFile, Path: "out.txt", Body: "secret file content, never leaked"}, "out.txt"},
		{"grep", tools.Call{Tool: tools.ToolGrep, Body: "TODO", Path: "internal"}, "TODO in internal"},
		{
			"run_command strips arguments",
			tools.Call{Tool: tools.ToolRunCommand, Body: `curl -H "Authorization: Bearer sk-super-secret" https://api.example.com`},
			"curl",
		},
		{
			"MCP call never exposes raw args",
			tools.Call{Tool: "mcp_tool", MCPServer: "jiraWorklog", MCPTool: "add_comment", MCPArgs: `{"token":"super-secret"}`},
			"jiraWorklog.add_comment",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolCallDetail(tc.call)
			if got != tc.want {
				t.Errorf("toolCallDetail(%+v) = %q, want %q", tc.call, got, tc.want)
			}
			if strings.Contains(got, "secret") {
				t.Errorf("toolCallDetail(%+v) leaked secret material: %q", tc.call, got)
			}
		})
	}
}
