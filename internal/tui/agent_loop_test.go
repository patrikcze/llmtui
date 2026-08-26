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

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/memoryindex"
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

// verifierJSON builds a complete verifier envelope: every field
// verifierJSONSchema declares required, plus atomic_task, present. Parse
// rejects any envelope missing a required field, so this must stay
// exhaustive even though most call sites only care about a few of these
// values. atomic_task is fixed true because these tests are not exercising
// criteria decomposition (a "passed" first-cycle verdict must otherwise
// either propose criteria or declare the task atomic — see REL-002); tests
// that specifically exercise proposed_criteria build their own literal
// envelope instead of using this helper.
func verifierJSON(verdict, summary, next string, retryable, changed bool) string {
	return `{"verdict":"` + verdict + `","summary":"` + summary + `","evidence":[],"failed_criteria":[],` +
		`"remaining_criteria":[],"recommended_next":"` + next + `","retryable":` +
		map[bool]string{true: "true", false: "false"}[retryable] + `,"confidence":0.9,"new_evidence":false,` +
		`"strategy_changed":` + map[bool]string{true: "true", false: "false"}[changed] +
		`,"transient_failure":false,"needs_user_input":false,"user_options":[],"criteria":[],` +
		`"proposed_criteria":[],"atomic_task":true}`
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
	// This harness builds config.Config directly rather than through viper,
	// so viper's SetDefault("agent.verifier.max_attempts", 2) never applies;
	// mirror that production default explicitly here, matching the pattern
	// already used for Timeout/MaxTokens above. Individual tests override it
	// when they need a different bound.
	m.cfg.Agent.Verifier.MaxAttempts = 2
	m.cfg.Agent.Persist = false
	m.agentLoop.store = nil
	return m, prov
}

func TestAgentProspectiveBudgetRejectsBeforeExecutorDispatch(t *testing.T) {
	m, prov := configureAgentTestModel(t, agentScriptStep{text: "must not run"})
	m.cfg.Agent.EnforceBudgetsLive = true
	m.cfg.Agent.MaxTokens = 1
	m.cfg.Chat.MaxTokens = 1

	_ = m.startVerifiedRun("do a task", nil)
	if len(prov.requests) != 0 {
		t.Fatalf("provider requests = %d, want 0", len(prov.requests))
	}
	if m.agentLoop.run.Status != agent.DecisionBudgetExhausted {
		t.Fatalf("status = %s, want %s", m.agentLoop.run.Status, agent.DecisionBudgetExhausted)
	}
	if !strings.Contains(m.agentLoop.run.StopReason, "admission rejected executor request") {
		t.Fatalf("stop reason = %q", m.agentLoop.run.StopReason)
	}
}

func TestAgentProspectiveBudgetUsesActualUsageForContinuation(t *testing.T) {
	m, prov := configureAgentTestModel(t, agentScriptStep{text: "must not run"})
	m.cfg.Agent.EnforceBudgetsLive = true
	m.cfg.Chat.MaxTokens = 1
	run, err := agent.NewRun("run-budget-continuation", "task", agent.Limits{
		MaxCycles: 2, MaxToolCalls: 2, MaxTokens: 20, MaxElapsed: time.Minute, MaxRepeatedFailures: 2,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle("task", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	run.RecordUsage(19, 0, time.Now())
	m.agentLoop.run = run
	m.resetAgentContext()
	t.Cleanup(m.releaseAgentContext)

	_ = m.continueChat()
	if len(prov.requests) != 0 {
		t.Fatalf("provider requests = %d, want 0", len(prov.requests))
	}
	if run.Status != agent.DecisionBudgetExhausted {
		t.Fatalf("status = %s, want %s", run.Status, agent.DecisionBudgetExhausted)
	}
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
	if m.pickerKind != pickerNone || m.overlayOpen {
		t.Fatalf("memory-off completion opened promotion picker: kind=%v overlay=%v", m.pickerKind, m.overlayOpen)
	}
}

func TestVerifiedAgentMemoryOffSuppressesPromotion(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	cmdMemory(m, "on")
	cmdMemory(m, "off")
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	if m.pickerKind != pickerNone || m.overlayOpen {
		t.Fatalf("memory-off completion opened promotion picker: kind=%v overlay=%v", m.pickerKind, m.overlayOpen)
	}
	if err := m.promoteAgentOutcome("decision"); err == nil || !strings.Contains(err.Error(), "memory is disabled") {
		t.Fatalf("promotion while memory is off returned %v", err)
	}
	records, err := m.projectStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("memory-off promotion wrote records: %+v", records)
	}
}

func TestVerifiedAgentPromotionRequiresExplicitSelection(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Implemented the bounded change."},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	m.memEnabled = true
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

	records, err := m.projectStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("completion auto-promoted records: %+v", records)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	records, err = m.projectStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("promoted records = %+v", records)
	}
	record := records[0]
	if record.Kind != memoryindex.KindProjectDecision || record.Review != memoryindex.ReviewApproved || record.Trust != memoryindex.TrustModelProposed {
		t.Fatalf("promoted record = %+v", record)
	}
	if record.SourceRunID != m.agentLoop.run.ID || record.SourceCycle != m.agentLoop.run.Cycle {
		t.Fatalf("promotion provenance = %+v", record)
	}
	if !strings.Contains(record.Text, "observable criteria passed") {
		t.Fatalf("promotion omitted passed verification: %q", record.Text)
	}
	inspect, err := m.memoryInspectOverlay(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inspect, record.SourceRunID) || !strings.Contains(inspect, "source cycle") {
		t.Fatalf("promotion inspect omitted provenance:\n%s", inspect)
	}
}

func TestAgentPromotionSkipAndEscapeWriteNothing(t *testing.T) {
	for _, action := range []string{"skip", "escape"} {
		t.Run(action, func(t *testing.T) {
			m, _ := configureAgentTestModel(t,
				agentScriptStep{text: "done"},
				agentScriptStep{text: verifierJSON("passed", "passed", "", false, false)},
			)
			m.memEnabled = true
			driveAgentCommands(t, m, m.startVerifiedRun("task", nil))
			key := tea.KeyPressMsg{Code: tea.KeyEnter}
			if action == "escape" {
				key = tea.KeyPressMsg{Code: tea.KeyEsc}
			}
			m.Update(key)
			records, err := m.projectStore.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 0 {
				t.Fatalf("%s wrote records: %+v", action, records)
			}
		})
	}
}

func TestVerifiedAgentVerifierModelSelectionCompatibility(t *testing.T) {
	tests := []struct {
		name          string
		verifierModel string
		wantModel     string
	}{
		{name: "empty reuses executor", verifierModel: "", wantModel: "openai/gpt-oss-20b"},
		{name: "explicit same model", verifierModel: "openai/gpt-oss-20b", wantModel: "openai/gpt-oss-20b"},
		{name: "separate model", verifierModel: "google/gemma-4-e4b", wantModel: "google/gemma-4-e4b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, prov := configureAgentTestModel(t,
				agentScriptStep{text: "Completed the bounded task with observable evidence."},
				agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
			)
			m.model = "openai/gpt-oss-20b"
			m.cfg.Agent.Verifier.Model = tt.verifierModel

			driveAgentCommands(t, m, m.startVerifiedRun("perform one bounded check", nil))

			if m.agentLoop.run.Status != agent.DecisionDone {
				t.Fatalf("run status = %s, want done", m.agentLoop.run.Status)
			}
			if len(prov.requests) != 2 {
				t.Fatalf("requests = %d, want executor + verifier", len(prov.requests))
			}
			if got := prov.requests[0].Model; got != "openai/gpt-oss-20b" {
				t.Fatalf("executor model = %q, want openai/gpt-oss-20b", got)
			}
			verifyReq := prov.requests[1]
			if verifyReq.Model != tt.wantModel {
				t.Fatalf("verifier model = %q, want %q", verifyReq.Model, tt.wantModel)
			}
			if len(verifyReq.Messages) != 2 || len(verifyReq.Tools) != 0 || verifyReq.Stream ||
				verifyReq.Reasoning != "off" || verifyReq.Temperature != 0 || verifyReq.TopP != 1 {
				t.Fatalf("verifier request lost isolated settings: %+v", verifyReq)
			}
		})
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
	for _, stale := range []string{agentContinueDirective, "old-benchmark-output"} {
		if strings.Contains(firstContext.String(), stale) {
			t.Fatalf("new run leaked completed agent machinery %q:\n%s", stale, firstContext.String())
		}
	}
	for _, conversational := range []string{"OLD BENCHMARK INSTRUCTIONS", "OLD BENCHMARK COMPLETE", "OLD BENCHMARK SUMMARY"} {
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

func TestVerifiedAgentStartContextIsBoundedAndProtocolFree(t *testing.T) {
	m := newTestModel(t)
	m.summary = "summary-only fact: the deployment is blue"
	m.session.AddUser("ordinary prior question")
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolListDir}}})
	m.session.AddMessage(provider.Message{Role: provider.RoleTool, ToolCallID: "call-1", Content: "private protocol output"})
	m.session.AddUser(agentContinueDirective)
	m.session.AddAssistant("ordinary prior final answer")

	run, err := agent.NewRun("run-start-snapshot", "next task", m.agentLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run.StartContextCaptured = true
	run.StartSummary = truncateAgentText(m.summary, maxAgentStartSummaryBytes)
	run.StartTurns = snapshotAgentStartTurns(m.session.Messages)
	if err := run.BeginCycle("next task", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	m.agentLoop.run = run
	m.agentLoop.historyStart = len(m.session.Messages)
	m.resetAgentContext()
	t.Cleanup(m.releaseAgentContext)

	messages, summary, scoped := m.requestHistory()
	if !scoped || summary != m.summary {
		t.Fatalf("snapshot = scoped:%v summary:%q", scoped, summary)
	}
	var content strings.Builder
	for _, message := range messages {
		content.WriteString(message.Content)
		content.WriteByte('\n')
	}
	for _, want := range []string{"ordinary prior question", "ordinary prior final answer"} {
		if !strings.Contains(content.String(), want) {
			t.Fatalf("snapshot lost %q: %s", want, content.String())
		}
	}
	for _, forbidden := range []string{"private protocol output", agentContinueDirective} {
		if strings.Contains(content.String(), forbidden) {
			t.Fatalf("snapshot retained protocol %q: %s", forbidden, content.String())
		}
	}

	run.Cycle = 2
	_, laterSummary, _ := m.requestHistory()
	if laterSummary != "" {
		t.Fatalf("cycle two summary = %q, want isolated run memory only", laterSummary)
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

// TestVerifiedAgentVerifierParseFailureRepeatedStops is the audit's smoking
// gun for REL-001 part 2: two consecutive verifier failures for the SAME
// cycle (each already exhausting agentverify.Verify's own internal one-shot
// malformed-JSON repair) must exhaust the default MaxAttempts=2 verifier
// budget and park the run as agent.DecisionVerificationUnavailable, without
// ever scheduling a second executor cycle. The script deliberately offers no
// second executor reply — if handleAgentVerification regressed to the old
// "treat a verifier failure as a retryable cycle result and restart the
// executor" behavior, driveAgentCommands would fail with "script exhausted"
// instead of silently passing, so an accidental executor retry cannot go
// unnoticed here.
func TestVerifiedAgentVerifierParseFailureRepeatedStops(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Attempt one."},
		agentScriptStep{text: "not a json object at all"},
		agentScriptStep{text: "still not a json object"},
		agentScriptStep{text: "not a json object at all"},
		agentScriptStep{text: "still not a json object"},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("fix repeated failure", nil))

	if m.agentLoop.run.Status != agent.DecisionVerificationUnavailable {
		t.Fatalf("status = %q, want verification_unavailable: run = %+v", m.agentLoop.run.Status, m.agentLoop.run)
	}
	if len(m.agentLoop.run.Cycles) != 1 {
		t.Fatalf("cycles = %+v, want exactly one (no second executor cycle)", m.agentLoop.run.Cycles)
	}
	cycle := m.agentLoop.run.Cycles[0]
	if cycle.Execution == nil || cycle.Execution.Summary != "Attempt one." {
		t.Fatalf("cycle.Execution = %+v, want the executor's result preserved", cycle.Execution)
	}
	if cycle.Verification != nil {
		t.Fatalf("cycle.Verification = %+v, want nil: verification never completed for this cycle", cycle.Verification)
	}
	// Two verifier requests (the malformed reply plus agentverify's own
	// internal repair attempt, which also fails) are consumed for the FIRST
	// outer verifier attempt, then two more for the SECOND (and, with the
	// default MaxAttempts=2, final) outer attempt — one executor request
	// plus two independent malformed+repair pairs.
	if len(prov.requests) != 5 {
		t.Fatalf("provider requests = %d, want 5 (executor + 2x(malformed+repair))", len(prov.requests))
	}
}

// TestVerifiedAgentVerifierParseFailureThenValidSucceedsWithoutExecutorRepeat
// proves the retry budget doesn't block a legitimate eventual success: with
// MaxAttempts=3, two malformed verifier replies (each already exhausting the
// internal repair) followed by a valid verdict on the third attempt must
// reach the verdict-driven outcome with exactly one executor cycle.
func TestVerifiedAgentVerifierParseFailureThenValidSucceedsWithoutExecutorRepeat(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Attempt one."},
		agentScriptStep{text: "not a json object at all"},
		agentScriptStep{text: "still not a json object"},
		agentScriptStep{text: "not a json object at all"},
		agentScriptStep{text: "still not a json object"},
		agentScriptStep{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
	)
	m.cfg.Agent.Verifier.MaxAttempts = 3
	driveAgentCommands(t, m, m.startVerifiedRun("fix repeated failure", nil))

	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("status = %q, want done: run = %+v", m.agentLoop.run.Status, m.agentLoop.run)
	}
	if len(m.agentLoop.run.Cycles) != 1 {
		t.Fatalf("cycles = %+v, want exactly one executor cycle", m.agentLoop.run.Cycles)
	}
	// executor(1) + attempt1(malformed+repair, 2) + attempt2(malformed+repair, 2) + attempt3(valid, 1) = 6.
	if len(prov.requests) != 6 {
		t.Fatalf("provider requests = %d, want 6", len(prov.requests))
	}
}

// TestVerifiedAgentVerifierTimeoutExhaustsToVerificationUnavailable and
// TestVerifiedAgentVerifierProviderErrorExhaustsToVerificationUnavailable
// cover the other two verifier-infrastructure failure kinds the audit
// called out (timeout, provider error) alongside the malformed-JSON case
// above: each must produce the identical one-executor-cycle outcome.
func TestVerifiedAgentVerifierTimeoutExhaustsToVerificationUnavailable(t *testing.T) {
	timeoutErr := agent.NewError(agent.ErrorTimeout, "verify", context.DeadlineExceeded)
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Attempt one."},
		agentScriptStep{err: timeoutErr},
		agentScriptStep{err: timeoutErr},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("fix repeated failure", nil))

	if m.agentLoop.run.Status != agent.DecisionVerificationUnavailable {
		t.Fatalf("status = %q, want verification_unavailable: run = %+v", m.agentLoop.run.Status, m.agentLoop.run)
	}
	if len(m.agentLoop.run.Cycles) != 1 {
		t.Fatalf("cycles = %+v, want exactly one", m.agentLoop.run.Cycles)
	}
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3 (executor + 2 verifier attempts)", len(prov.requests))
	}
}

func TestVerifiedAgentVerifierProviderErrorExhaustsToVerificationUnavailable(t *testing.T) {
	providerErr := agent.NewError(agent.ErrorProvider, "verify", errors.New("connection refused"))
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Attempt one."},
		agentScriptStep{err: providerErr},
		agentScriptStep{err: providerErr},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("fix repeated failure", nil))

	if m.agentLoop.run.Status != agent.DecisionVerificationUnavailable {
		t.Fatalf("status = %q, want verification_unavailable: run = %+v", m.agentLoop.run.Status, m.agentLoop.run)
	}
	if len(m.agentLoop.run.Cycles) != 1 {
		t.Fatalf("cycles = %+v, want exactly one", m.agentLoop.run.Cycles)
	}
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3 (executor + 2 verifier attempts)", len(prov.requests))
	}
}

// executorThenBlockingProvider answers the first Chat call (the executor
// turn) immediately with scripted text, then blocks every later call (the
// verifier turn) until its context is cancelled — letting a test hold a
// verifier request genuinely in flight to exercise real concurrent
// cancellation, the same shape as blockingAgentProvider but only for calls
// after the first.
type executorThenBlockingProvider struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
}

func (p *executorThenBlockingProvider) Name() string                      { return "executor-then-blocking" }
func (p *executorThenBlockingProvider) HealthCheck(context.Context) error { return nil }
func (p *executorThenBlockingProvider) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: "test-model"}}, nil
}
func (p *executorThenBlockingProvider) Chat(ctx context.Context, _ provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 {
		events := make(chan provider.ChatEvent, 2)
		events <- provider.ChatEvent{Type: provider.EventDelta, Delta: "Attempt one."}
		events <- provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
		close(events)
		return events, nil
	}
	close(p.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestVerifiedAgentCancellationWhileVerifyingEndsCancelled locks in that a
// cancellation racing a genuinely in-flight verifier attempt still ends the
// run DecisionCancelled, never DecisionVerificationUnavailable —
// cancellation is not an infra failure to retry. cancelVerifiedRun bumps
// m.agentLoop.verifyGen synchronously before cancelling the verify context,
// so the ErrorCancelled result the blocked call eventually returns carries a
// stale gen and is discarded by the guard at the top of
// handleAgentVerification before the new retry-budget logic ever runs.
func TestVerifiedAgentCancellationWhileVerifyingEndsCancelled(t *testing.T) {
	m := newTestModel(t)
	prov := &executorThenBlockingProvider{started: make(chan struct{})}
	m.prov = prov
	m.model = "test-model"
	m.agentOn = true
	m.cfg.Agent.Verifier.Enabled = true
	m.cfg.Agent.Verifier.Mode = "always"
	m.cfg.Agent.Verifier.Timeout = "5s"
	m.cfg.Agent.Verifier.MaxTokens = 256
	m.cfg.Agent.Persist = false
	m.agentLoop.store = nil

	// Drive commands synchronously up to (but not through) the verifier
	// dispatch: everything before it is fast and non-blocking, so the normal
	// driver loop works until the verifying stage is reached. app.go's
	// EventDone handler returns m.startAgentVerification()'s Cmd directly
	// (not wrapped in a batch), and m.agentLoop.verifying is already true by
	// the time that Update call returns, so `next` right after that call is
	// exactly the verify Cmd — captured here instead of executed.
	queue := []tea.Cmd{m.startVerifiedRun("cancel during verification", nil)}
	var verifyCmd tea.Cmd
	for len(queue) > 0 {
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
		if m.agentVerifying() {
			verifyCmd = next
			break
		}
		if next != nil {
			queue = append(queue, next)
		}
	}
	if verifyCmd == nil || !m.agentVerifying() {
		t.Fatal("run did not reach the verifying stage")
	}
	if m.agentLoop.verifierModel != "test-model" || m.agentLoop.verifierStartedAt.IsZero() || m.verifierActivityHeight() != 1 {
		t.Fatalf("verifier activity = model %q started %v height %d", m.agentLoop.verifierModel, m.agentLoop.verifierStartedAt, m.verifierActivityHeight())
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- verifyCmd() }()
	select {
	case <-prov.started:
	case <-time.After(time.Second):
		t.Fatal("verifier provider did not start")
	}

	m.cancelVerifiedRun("cancelled by test")
	m.endAgentRun()
	if m.agentLoop.verifierModel != "" || !m.agentLoop.verifierStartedAt.IsZero() || m.verifierActivityHeight() != 0 {
		t.Fatalf("cancel left verifier activity: model %q started %v height %d", m.agentLoop.verifierModel, m.agentLoop.verifierStartedAt, m.verifierActivityHeight())
	}

	select {
	case msg := <-result:
		// The stale-gen guard must discard this without changing run state.
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("verifier command did not unblock after cancellation")
	}

	if m.agentLoop.run.Status != agent.DecisionCancelled {
		t.Fatalf("status = %q, want cancelled", m.agentLoop.run.Status)
	}
}

func TestVerifierActivityAdvancesOnRetry(t *testing.T) {
	m := newTestModel(t)
	m.model = "openai/gpt-oss-20b"
	m.cfg.Agent.Verifier.Model = "google/gemma-4-e4b"
	m.cfg.Agent.Verifier.MaxAttempts = 2
	m.agentLoop.run = &agent.AgentRun{
		ID: "run-retry-activity", Cycle: 3, Stage: agent.StageVerifier, Status: agent.DecisionRunning,
		Limits: agent.Limits{MaxCycles: 8},
	}
	m.agentLoop.verifying = true
	m.agentLoop.verifyGen = 1
	m.beginVerifierActivity("google/gemma-4-e4b")
	firstStartedAt := m.agentLoop.verifierStartedAt

	_, retry := m.handleAgentVerification(agentVerificationMsg{
		runID: "run-retry-activity", cycle: 3, gen: 1, err: errors.New("temporary verifier failure"),
	})
	if retry == nil || !m.agentLoop.verifying {
		t.Fatal("verifier failure did not schedule the bounded retry")
	}
	if m.agentLoop.verifierAttempts != 1 || m.agentLoop.verifyGen != 2 {
		t.Fatalf("retry state = attempts %d gen %d", m.agentLoop.verifierAttempts, m.agentLoop.verifyGen)
	}
	if m.agentLoop.verifierModel != "google/gemma-4-e4b" || m.agentLoop.verifierStartedAt.Before(firstStartedAt) {
		t.Fatalf("retry activity = model %q started %v; first started %v", m.agentLoop.verifierModel, m.agentLoop.verifierStartedAt, firstStartedAt)
	}
	if view := m.render(); !strings.Contains(view, "attempt 2/2") {
		t.Fatalf("retry activity missing attempt 2/2: %s", view)
	}
	m.cancelVerifiedRun("test cleanup")
}

func TestStaleVerifierResultDoesNotClearCurrentActivity(t *testing.T) {
	m := newTestModel(t)
	m.agentLoop.run = &agent.AgentRun{
		ID: "run-stale-activity", Cycle: 2, Stage: agent.StageVerifier, Status: agent.DecisionRunning,
		Limits: agent.Limits{MaxCycles: 8},
	}
	m.agentLoop.verifying = true
	m.agentLoop.verifyGen = 2
	m.beginVerifierActivity("google/gemma-4-e4b")
	startedAt := m.agentLoop.verifierStartedAt

	_, cmd := m.handleAgentVerification(agentVerificationMsg{
		runID: "run-stale-activity", cycle: 2, gen: 1,
	})
	if cmd != nil {
		t.Fatal("stale verifier result scheduled work")
	}
	if !m.agentLoop.verifying || m.agentLoop.verifierModel != "google/gemma-4-e4b" ||
		!m.agentLoop.verifierStartedAt.Equal(startedAt) || m.verifierActivityHeight() != 1 {
		t.Fatalf("stale result changed current activity: verifying=%v model=%q started=%v height=%d",
			m.agentLoop.verifying, m.agentLoop.verifierModel, m.agentLoop.verifierStartedAt, m.verifierActivityHeight())
	}
	m.cancelVerifiedRun("test cleanup")
}

func TestVerifiedAgentRepairsVerifierFormatWithoutRepeatingExecutor(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "Completed the requested work."},
		agentScriptStep{text: `{"verdict":"passed","summary":"checked","evidence":[],"failed_criteria":[],"remaining_criteria":[],` +
			`"recommended_next":"","retryable":false,"confidence":0.9,"new_evidence":false,"strategy_changed":false,` +
			`"transient_failure":false,"needs_user_input":false,"user_options":[],"criteria":[],"atomic_task":false,` +
			`"proposed_criteria":[{"criterion":"complete the work"}]}`},
		agentScriptStep{text: `{"verdict":"passed","summary":"checked","evidence":[],"failed_criteria":[],"remaining_criteria":[],` +
			`"recommended_next":"","retryable":false,"confidence":0.9,"new_evidence":false,"strategy_changed":false,` +
			`"transient_failure":false,"needs_user_input":false,"user_options":[],"atomic_task":false,` +
			`"proposed_criteria":["complete the work"],"criteria":[{"id":"c1","status":"satisfied","note":""}]}`},
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
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: string('n')})
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
	verifierReply := `{"verdict":"inconclusive","summary":"` + question + `","evidence":[],"failed_criteria":[],` +
		`"remaining_criteria":[],"recommended_next":"","retryable":true,"confidence":0.8,"new_evidence":false,` +
		`"strategy_changed":false,"transient_failure":false,"needs_user_input":true,"user_options":[],` +
		`"criteria":[],"proposed_criteria":[],"atomic_task":false}`
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
	verifierReply := `{"verdict":"inconclusive","summary":"` + question + `","evidence":[],"failed_criteria":[],` +
		`"remaining_criteria":[],"recommended_next":"","retryable":true,"confidence":0.8,"new_evidence":false,` +
		`"strategy_changed":false,"transient_failure":false,` +
		`"needs_user_input":true,"user_options":["Prague","Humpolec","Brno"],"criteria":[],` +
		`"proposed_criteria":[],"atomic_task":false}`
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: question},
		agentScriptStep{text: verifierReply},
		agentScriptStep{text: "Checked Humpolec's weather."},
		agentScriptStep{text: verifierJSON("passed", "weather reported", "", false, false)},
	)
	m.memEnabled = true
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
	_, downCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	driveAgentCommands(t, m, downCmd)
	if m.pickerIdx != 1 {
		t.Fatalf("pickerIdx = %d, want 1 (Humpolec) after one down-arrow", m.pickerIdx)
	}
	_, enterCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	driveAgentCommands(t, m, enterCmd)

	if m.pickerKind != pickerAgentPromotion {
		t.Fatalf("pickerKind = %v, want pickerAgentPromotion after completed resume", m.pickerKind)
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
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
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
	verifierPass := `{"verdict":"passed","summary":"workspace inspected as requested","evidence":[],` +
		`"failed_criteria":[],"remaining_criteria":[],"recommended_next":"","retryable":false,"confidence":0.9,` +
		`"new_evidence":false,"strategy_changed":false,"transient_failure":false,"needs_user_input":false,` +
		`"user_options":[],"atomic_task":false,` +
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

func newAgentVerificationTestRun(t *testing.T, m *Model, request string, criteria []agent.CriterionSpec, execution agent.ExecutionResult) *agent.AgentRun {
	t.Helper()
	run, err := agent.NewRun("verification-test", request, agent.DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginCycle(request, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	run.PinTypedCriteria(criteria)
	m.agentLoop.run = run
	m.agentLoop.execution = execution
	m.agentOn = true
	m.cfg.Agent.Verifier.Mode = "adaptive"
	m.resetAgentContext()
	t.Cleanup(m.releaseAgentContext)
	return run
}

func TestAdaptiveDeterministicTaskCompletesWithoutSemanticVerifier(t *testing.T) {
	m, prov := configureAgentTestModel(t, agentScriptStep{text: "must not be requested"})
	run := newAgentVerificationTestRun(t, m, "run tests", nil, agent.ExecutionResult{
		TestsRun:    []agent.TestResult{{Name: "go test ./...", Passed: true}},
		NewEvidence: true,
	})
	driveAgentCommands(t, m, m.startAgentVerification())
	if run.Status != agent.DecisionDone {
		t.Fatalf("status = %q, want done", run.Status)
	}
	if len(prov.requests) != 0 {
		t.Fatalf("provider requests = %d, want no semantic verifier", len(prov.requests))
	}
}

func TestAdaptiveMixedCriteriaVerifiesOnlySemanticRemainder(t *testing.T) {
	minimalPass := `{"verdict":"passed","summary":"report is complete","recommended_next":"","retryable":false,` +
		`"needs_user_input":false,"user_options":[],"criteria":[{"id":"c2","status":"satisfied","note":"observed"}],` +
		`"proposed_criteria":[],"atomic_task":false}`
	m, prov := configureAgentTestModel(t, agentScriptStep{text: minimalPass})
	run := newAgentVerificationTestRun(t, m, "run tests and explain the result", []agent.CriterionSpec{
		{Text: "tests pass", Kind: agent.CriterionTestResult, Target: "go test ./..."},
		{Text: "result is explained", Kind: agent.CriterionSemantic},
	}, agent.ExecutionResult{
		TestsRun:    []agent.TestResult{{Name: "go test ./...", Passed: true}},
		NewEvidence: true,
	})
	driveAgentCommands(t, m, m.startAgentVerification())
	if run.Status != agent.DecisionDone {
		t.Fatalf("status = %q, want done", run.Status)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("provider requests = %d, want one semantic verifier", len(prov.requests))
	}
	evidence := prov.requests[0].Messages[1].Content
	if !strings.Contains(evidence, "result is explained") || strings.Contains(evidence, `\"Text\":\"tests pass\"`) {
		t.Fatalf("verifier received more than the unresolved semantic criterion: %s", evidence)
	}
}

func TestAdaptiveUserCriterionStopsWithoutSemanticVerifier(t *testing.T) {
	m, prov := configureAgentTestModel(t, agentScriptStep{text: "must not be requested"})
	run := newAgentVerificationTestRun(t, m, "ask which target to use", []agent.CriterionSpec{
		{Text: "Choose a deployment target", Kind: agent.CriterionUserInput},
	}, agent.ExecutionResult{Summary: "target is required"})
	driveAgentCommands(t, m, m.startAgentVerification())
	if run.Status != agent.DecisionNeedsUserInput {
		t.Fatalf("status = %q, want needs_user_input", run.Status)
	}
	if len(prov.requests) != 0 {
		t.Fatalf("provider requests = %d, want no semantic verifier", len(prov.requests))
	}
}

func TestAgentDirectiveTokenSnapshots(t *testing.T) {
	tests := []struct {
		name      string
		populate  func(*agent.AgentRun)
		maxTokens int
	}{
		{name: "small_local_compact_state", maxTokens: 220},
		{
			name: "large_model_maximum_bounded_state", maxTokens: 3100,
			populate: func(run *agent.AgentRun) {
				criteria := make([]agent.CriterionSpec, agent.MaxCriteria)
				for i := range criteria {
					criteria[i] = agent.CriterionSpec{Text: fmt.Sprintf("criterion %02d %s", i, strings.Repeat("x", 180)), Kind: agent.CriterionSemantic}
				}
				run.PinTypedCriteria(criteria)
				for i := 0; i < agent.MaxEvidence; i++ {
					run.AppendEvidence([]agent.EvidenceItem{{Source: fmt.Sprintf("source-%02d", i), Summary: strings.Repeat("e", 220), Success: i%2 == 0}})
				}
				for cycle := 1; cycle <= 4; cycle++ {
					calls := make([]string, 32)
					for i := range calls {
						calls[i] = fmt.Sprintf("operation-%d-%02d %s", cycle, i, strings.Repeat("o", 120))
					}
					run.Memory = append(run.Memory, agent.MemoryEntry{Cycle: cycle, ToolCalls: calls})
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t)
			run, err := agent.NewRun("prompt-snapshot", "complete the bounded task", agent.DefaultLimits(), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if err := run.BeginCycle("complete the bounded task", nil, time.Now()); err != nil {
				t.Fatal(err)
			}
			if tt.populate != nil {
				tt.populate(run)
			}
			m.agentOn = true
			m.agentLoop.run = run
			directive := m.agentDirective()
			if tokens := provider.EstimateTokens(directive); tokens > tt.maxTokens {
				t.Fatalf("directive token snapshot = %d, want <= %d", tokens, tt.maxTokens)
			}
			for _, controllerOnly := range []string{"Run ID", "failure fingerprint", "Stage:"} {
				if strings.Contains(directive, controllerOnly) {
					t.Fatalf("directive leaked controller field %q", controllerOnly)
				}
			}
		})
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
	proposeAndFail := `{"verdict":"failed","summary":"report criterion unresolved","evidence":[],` +
		`"failed_criteria":[],"remaining_criteria":[],` +
		`"recommended_next":"produce the report","retryable":true,"strategy_changed":true,"confidence":0.8,` +
		`"new_evidence":false,"transient_failure":false,"needs_user_input":false,"user_options":[],"atomic_task":false,` +
		`"proposed_criteria":["gather data","produce report"],` +
		`"criteria":[{"id":"c1","status":"satisfied","note":"data gathered"}]}`
	satisfyRest := `{"verdict":"passed","summary":"report produced","evidence":[],"failed_criteria":[],` +
		`"remaining_criteria":[],"recommended_next":"","retryable":false,"confidence":0.9,"new_evidence":false,` +
		`"strategy_changed":false,"transient_failure":false,"needs_user_input":false,"user_options":[],` +
		`"proposed_criteria":[],"atomic_task":false,` +
		`"criteria":[{"id":"c2","status":"satisfied","note":""}]}`
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
