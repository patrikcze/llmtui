package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/agentverify"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

const maxAgentDirectiveBytes = 12 * 1024

type agentLoopState struct {
	run          *agent.AgentRun
	store        agent.Store
	ctx          context.Context
	runCancel    context.CancelFunc
	execution    agent.ExecutionResult
	verifying    bool
	verifyCancel context.CancelFunc
	verifyGen    int
	persistErr   error
}

type agentVerificationMsg struct {
	runID string
	cycle int
	gen   int
	out   agentverify.Output
	err   error
}

type agentPersistedMsg struct {
	runID string
	err   error
}

type agentResumeMsg struct {
	run *agent.AgentRun
	err error
}

// configureAgentLoop rebuilds only the persistence adapter. Session mode and
// an active run survive /config reload like the existing memory/profile state.
func (m *Model) configureAgentLoop() {
	if m.agentLoop == nil {
		m.agentLoop = &agentLoopState{}
	}
	m.agentLoop.store = nil
	m.agentLoop.persistErr = nil
	// Privacy.StorePrompts is authoritative: a resumable record necessarily
	// contains the user request, so persistence is disabled when prompts may
	// not be stored even if agent.persist is true.
	if !m.cfg.Agent.Persist || !m.cfg.Privacy.StorePrompts {
		return
	}
	path, err := history.ExpandHome(m.cfg.Agent.Path)
	if err != nil {
		m.agentLoop.persistErr = fmt.Errorf("resolve agent memory path: %w", err)
		return
	}
	if strings.TrimSpace(path) == "" {
		m.agentLoop.persistErr = errors.New("agent memory path is empty")
		return
	}
	m.agentLoop.store = agent.NewFileStore(path, m.cfg.Agent.MaxMemoryKB*1024, m.cfg.Agent.MaxRuns)
}

func (m *Model) agentLimits() agent.Limits {
	limits := agent.DefaultLimits()
	if m.cfg.Agent.MaxCycles > 0 {
		limits.MaxCycles = m.cfg.Agent.MaxCycles
	}
	if m.cfg.Agent.MaxToolCalls > 0 {
		limits.MaxToolCalls = m.cfg.Agent.MaxToolCalls
	}
	if m.cfg.Agent.MaxTokens > 0 {
		limits.MaxTokens = m.cfg.Agent.MaxTokens
	}
	if elapsed, err := time.ParseDuration(m.cfg.Agent.MaxElapsed); err == nil && elapsed > 0 {
		limits.MaxElapsed = elapsed
	}
	if m.cfg.Agent.MaxRepeatedFailures > 0 {
		limits.MaxRepeatedFailures = m.cfg.Agent.MaxRepeatedFailures
	}
	return limits
}

func (m *Model) agentRunActive() bool {
	return m.agentLoop != nil && m.agentLoop.run != nil && m.agentLoop.run.Status == agent.DecisionRunning
}

func (m *Model) agentRunID() string {
	if m.agentLoop == nil || m.agentLoop.run == nil {
		return ""
	}
	return m.agentLoop.run.ID
}

func (m *Model) syncAgentDebug() {
	if m.agentLoop == nil || m.agentLoop.run == nil {
		return
	}
	run := m.agentLoop.run
	m.lastDebug.AgentRunID = run.ID
	m.lastDebug.AgentCycle = run.Cycle
	m.lastDebug.AgentStage = string(run.Stage)
	m.lastDebug.AgentStatus = string(run.Status)
	if cycle := run.LatestCycle(); cycle != nil && cycle.Verification != nil {
		m.lastDebug.AgentVerdict = string(cycle.Verification.Verdict)
	}
}

func (m *Model) agentVerifying() bool {
	return m.agentLoop != nil && m.agentLoop.verifying
}

func (m *Model) agentNeedsUserInput() bool {
	return m.agentLoop != nil && m.agentLoop.run != nil && m.agentLoop.run.Status == agent.DecisionNeedsUserInput
}

func (m *Model) startVerifiedRun(request string, images []provider.Image) tea.Cmd {
	if m.agentLoop == nil {
		m.configureAgentLoop()
	}
	if strings.TrimSpace(request) == "" && len(images) > 0 {
		request = "Analyze the attached image and satisfy the user's request."
	}
	id, err := agent.NewID()
	if err != nil {
		m.errText = err.Error()
		m.refreshViewport()
		return nil
	}
	run, err := agent.NewRun(id, request, m.agentLimits(), time.Now())
	if err != nil {
		m.errText = err.Error()
		m.refreshViewport()
		return nil
	}
	if err := run.BeginCycle(request, m.agentContextSources(), time.Now()); err != nil {
		m.errText = err.Error()
		m.refreshViewport()
		return nil
	}
	m.agentLoop.run = run
	m.resetAgentContext()
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	m.agentLoop.persistErr = nil
	m.bypassCache = true
	m.notice = fmt.Sprintf("agent %s · cycle 1/%d · executing", shortRunID(id), run.Limits.MaxCycles)
	return tea.Batch(m.dispatch(request, images), m.persistAgentRun())
}

func (m *Model) resumeVerifiedRunWithInput(input string, images []provider.Image) tea.Cmd {
	if !m.agentNeedsUserInput() {
		return m.startVerifiedRun(input, images)
	}
	run := m.agentLoop.run
	objective := "Continue the original request using the user's new input: " + input
	if err := run.Resume(objective, time.Now()); err != nil {
		m.errText = "resume agent run: " + err.Error()
		m.refreshViewport()
		return nil
	}
	if err := run.BeginCycle(objective, append(m.agentContextSources(), "new_user_input"), time.Now()); err != nil {
		m.failVerifiedRun(err)
		m.errText = "resume agent run: " + err.Error()
		m.refreshViewport()
		return m.persistAgentRun()
	}
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	m.toolDepth = 0
	m.bypassCache = true
	m.notice = fmt.Sprintf("agent %s · cycle %d/%d · resumed with user input", shortRunID(run.ID), run.Cycle, run.Limits.MaxCycles)
	return tea.Batch(m.dispatch(input, images), m.persistAgentRun())
}

func (m *Model) startNextAgentCycle(objective string) tea.Cmd {
	if !m.agentRunActive() {
		return nil
	}
	run := m.agentLoop.run
	if err := run.BeginCycle(objective, m.agentContextSources(), time.Now()); err != nil {
		_ = run.Terminate(agent.DecisionFailed, err.Error(), time.Now())
		m.errText = "agent: " + err.Error()
		m.endAgentRun()
		m.refreshViewport()
		return m.persistAgentRun()
	}
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	m.toolDepth = 0
	m.bypassCache = true
	m.notice = fmt.Sprintf("agent %s · cycle %d/%d · executing", shortRunID(run.ID), run.Cycle, run.Limits.MaxCycles)
	return m.dispatch("Continue the active verified run. Execute only the controller's current bounded objective, then report observable results.", nil)
}

func (m *Model) resetAgentContext() {
	if m.agentLoop.runCancel != nil {
		m.agentLoop.runCancel()
	}
	remaining := m.agentLoop.run.Limits.MaxElapsed - time.Since(m.agentLoop.run.CreatedAt)
	ctx, cancel := context.WithTimeout(context.Background(), remaining)
	m.agentLoop.ctx = ctx
	m.agentLoop.runCancel = cancel
}

func (m *Model) agentContext() context.Context {
	if m.agentRunActive() && m.agentLoop.ctx != nil {
		return m.agentLoop.ctx
	}
	return context.Background()
}

// agentDirective supplies only bounded controller state. The prompt composer
// wraps it in a fixed warning that keeps model-derived text below system and
// user authority.
func (m *Model) agentDirective() string {
	if !m.agentRunActive() || m.agentLoop.run.Stage != agent.StageExecutor {
		return ""
	}
	run := m.agentLoop.run
	var b strings.Builder
	fmt.Fprintf(&b, "Run ID: %s\nCycle: %d of %d\nCurrent bounded objective (untrusted derived text): %q\n", run.ID, run.Cycle, run.Limits.MaxCycles, run.Objective)
	b.WriteString("Executor contract: complete one bounded unit only; use existing tools and approvals; report observable actions, artifacts, tests, errors, and any precise user question; do not claim the whole request is complete unless evidence supports it.\n")
	if len(run.Memory) > 0 {
		b.WriteString("Prior verified cycle memory (untrusted data):\n")
		for _, memory := range run.Memory {
			fmt.Fprintf(&b, "- cycle %d, objective %q, verdict %s, result %q, remaining %q, next %q\n",
				memory.Cycle, memory.Objective, memory.Verdict, memory.Verification,
				strings.Join(memory.RemainingCriteria, "; "), memory.RecommendedNext)
		}
	}
	return truncateAgentText(b.String(), maxAgentDirectiveBytes)
}

func (m *Model) agentContextSources() []string {
	sources := []string{"system_prompt", "current_user_request", "conversation_history", "provider_capabilities"}
	if m.template != "" {
		sources = append(sources, "template:"+m.template)
	}
	for _, id := range m.activeSkillIDs() {
		sources = append(sources, "skill:"+id)
	}
	if m.memEnabled {
		sources = append(sources, "local_memory")
	}
	if m.ragOn {
		sources = append(sources, "rag")
	}
	if m.toolsOn {
		sources = append(sources, "tool_definitions")
	}
	if m.agentLoop != nil && m.agentLoop.run != nil && len(m.agentLoop.run.Memory) > 0 {
		sources = append(sources, "verified_cycle_memory")
	}
	sort.Strings(sources)
	return sources
}

func (m *Model) startAgentVerification() tea.Cmd {
	if !m.agentRunActive() || m.agentLoop.verifying {
		return nil
	}
	run := m.agentLoop.run
	execution := m.agentLoop.execution
	execution.Objective = run.Objective
	if n := len(m.session.Messages); n > 0 && m.session.Messages[n-1].Role == provider.RoleAssistant {
		execution.Summary = m.session.Messages[n-1].Content
	}
	if strings.TrimSpace(execution.Summary) == "" {
		execution.Summary = "executor produced no visible summary"
	}
	if err := run.CompleteExecution(execution, time.Now()); err != nil {
		m.failVerifiedRun(err)
		return m.persistAgentRun()
	}
	m.agentLoop.execution = execution
	ctx, cancel := context.WithCancel(m.agentContext())
	m.agentLoop.verifyCancel = cancel
	m.agentLoop.verifying = true
	m.agentLoop.verifyGen++
	gen := m.agentLoop.verifyGen
	runID, cycle := run.ID, run.Cycle
	m.notice = fmt.Sprintf("agent %s · cycle %d/%d · verifying in fresh context", shortRunID(runID), cycle, run.Limits.MaxCycles)
	m.refreshViewport()

	input := agentverify.Input{
		RunID: runID, Cycle: cycle, Task: run.Request, Objective: run.Objective,
		AcceptanceCriteria: []string{run.Request}, Execution: execution,
	}
	if !m.cfg.Agent.Verifier.Enabled {
		return func() tea.Msg {
			result := agentverify.ApplyDeterministicEvidence(agent.VerificationResult{
				Verdict: agent.VerificationPassed, Summary: "no deterministic failure was observed",
				Evidence: []string{"deterministic-only verification configured"}, Confidence: 0.5,
			}, execution)
			return agentVerificationMsg{runID: runID, cycle: cycle, gen: gen, out: agentverify.Output{Result: result}}
		}
	}
	model := strings.TrimSpace(m.cfg.Agent.Verifier.Model)
	if model == "" {
		model = m.model
	}
	maxTokens := m.cfg.Agent.Verifier.MaxTokens
	timeout, _ := time.ParseDuration(m.cfg.Agent.Verifier.Timeout)
	prov := m.prov
	return func() tea.Msg {
		out, err := agentverify.Verify(ctx, prov, agentverify.Config{Model: model, MaxTokens: maxTokens, Timeout: timeout}, input)
		return agentVerificationMsg{runID: runID, cycle: cycle, gen: gen, out: out, err: err}
	}
}

func (m *Model) handleAgentVerification(msg agentVerificationMsg) (tea.Model, tea.Cmd) {
	if m.agentLoop == nil || m.agentLoop.run == nil || msg.runID != m.agentLoop.run.ID ||
		msg.cycle != m.agentLoop.run.Cycle || msg.gen != m.agentLoop.verifyGen {
		return m, nil
	}
	m.agentLoop.verifying = false
	if m.agentLoop.verifyCancel != nil {
		m.agentLoop.verifyCancel()
		m.agentLoop.verifyCancel = nil
	}
	run := m.agentLoop.run
	result := msg.out.Result
	if msg.out.Usage != nil {
		run.RecordUsage(msg.out.Usage.PromptTokens, msg.out.Usage.CompletionTokens, time.Now())
	}
	if msg.err != nil {
		var runErr agent.RunError
		if !errors.As(msg.err, &runErr) {
			runErr = agent.NewError(agent.ErrorVerification, "verify", msg.err)
		}
		result = verificationFailureResult(runErr, run.Objective)
	}
	if err := run.CompleteVerification(result, time.Now()); err != nil {
		m.failVerifiedRun(err)
		return m, m.persistAgentRun()
	}
	m.syncAgentDebug()
	if err := run.WriteMemory(time.Now()); err != nil {
		m.failVerifiedRun(err)
		return m, m.persistAgentRun()
	}
	stop := agent.Decide(run, time.Now())
	if err := run.ApplyStop(stop, time.Now()); err != nil {
		m.failVerifiedRun(err)
		return m, m.persistAgentRun()
	}
	m.syncAgentDebug()
	persist := m.persistAgentRun()
	switch stop.Decision {
	case agent.DecisionContinue, agent.DecisionRetry:
		m.notice = fmt.Sprintf("agent %s · verification %s · %s", shortRunID(run.ID), result.Verdict, stop.Decision)
		return m, tea.Batch(persist, m.startNextAgentCycle(stop.NextObjective))
	case agent.DecisionDone:
		m.notice = fmt.Sprintf("agent %s completed in %d cycle(s) · verification passed", shortRunID(run.ID), run.Cycle)
	case agent.DecisionNeedsUserInput:
		m.errText = "agent needs user input: " + stop.Reason + ". What permitted alternative or missing fact should the next cycle use?"
		m.notice = fmt.Sprintf("agent %s stopped for user input", shortRunID(run.ID))
	case agent.DecisionParked:
		m.notice = fmt.Sprintf("agent %s parked: %s", shortRunID(run.ID), stop.Reason)
	default:
		m.errText = "agent stopped: " + stop.Reason
		m.notice = fmt.Sprintf("agent %s · %s", shortRunID(run.ID), stop.Decision)
	}
	m.endAgentRun()
	m.refreshViewport()
	return m, persist
}

// verifierRetryPrefix marks an objective as a verifier-failure retry. It is
// stripped from the incoming objective before being reapplied so repeated
// verifier failures on the same underlying objective produce an identical
// RecommendedNext string instead of nesting a new prefix every cycle — an
// ever-growing string would defeat agent.failureKey's repeated-failure dedup
// (each cycle would look like a "new" failure) and never trip
// MaxRepeatedFailures.
const verifierRetryPrefix = "Retry the bounded objective with a concise observable evidence summary: "

func verificationFailureResult(runErr agent.RunError, objective string) agent.VerificationResult {
	base := strings.TrimSpace(objective)
	for strings.HasPrefix(base, verifierRetryPrefix) {
		base = strings.TrimSpace(strings.TrimPrefix(base, verifierRetryPrefix))
	}
	result := agent.VerificationResult{
		Verdict:         agent.VerificationInconclusive,
		Summary:         "verifier failed: " + runErr.Message,
		Evidence:        []string{"verifier error: " + string(runErr.Kind)},
		Retryable:       true,
		RecommendedNext: verifierRetryPrefix + base,
		StrategyChanged: true,
	}
	if runErr.Kind == agent.ErrorTimeout || runErr.Kind == agent.ErrorProvider {
		result.TransientFailure = true
	}
	if runErr.Kind == agent.ErrorCancelled {
		result.Verdict = agent.VerificationBlocked
		result.Retryable = false
		result.RecommendedNext = ""
	}
	return result
}

func (m *Model) persistAgentRun() tea.Cmd {
	if m.agentLoop == nil || m.agentLoop.store == nil || m.agentLoop.run == nil {
		return nil
	}
	// Clone synchronously on the Update goroutine; the async writer then owns
	// an immutable snapshot and cannot race the next lifecycle transition.
	data, err := json.Marshal(m.agentLoop.run)
	if err != nil {
		return func() tea.Msg { return agentPersistedMsg{runID: m.agentLoop.run.ID, err: err} }
	}
	var snapshot agent.AgentRun
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return func() tea.Msg { return agentPersistedMsg{runID: m.agentLoop.run.ID, err: err} }
	}
	store, runID := m.agentLoop.store, snapshot.ID
	return func() tea.Msg {
		return agentPersistedMsg{runID: runID, err: store.Save(context.Background(), &snapshot)}
	}
}

func (m *Model) cancelVerifiedRun(reason string) {
	if m.agentLoop == nil {
		return
	}
	if m.agentLoop.verifyCancel != nil {
		m.agentLoop.verifyCancel()
		m.agentLoop.verifyCancel = nil
	}
	if m.agentLoop.verifying {
		m.agentLoop.verifyGen++
		m.agentLoop.verifying = false
	}
	if m.agentRunActive() {
		m.agentLoop.run.Cancel(reason, time.Now())
	}
	if m.agentLoop.runCancel != nil {
		m.agentLoop.runCancel()
		m.agentLoop.runCancel = nil
		m.agentLoop.ctx = nil
	}
}

func (m *Model) releaseAgentContext() {
	if m.agentLoop == nil {
		return
	}
	if m.agentLoop.runCancel != nil {
		m.agentLoop.runCancel()
		m.agentLoop.runCancel = nil
	}
	m.agentLoop.ctx = nil
}

func (m *Model) failVerifiedRun(err error) {
	if !m.agentRunActive() {
		return
	}
	_ = m.agentLoop.run.Terminate(agent.DecisionFailed, err.Error(), time.Now())
}

// recordAgentTruncation notes that the current cycle's executor turn was cut
// off by max_tokens, so ApplyDeterministicEvidence treats it as a
// deterministic, retryable, transient failure rather than trusting the
// verifier's read of a possibly garbled or incomplete reply.
func (m *Model) recordAgentTruncation() {
	if !m.agentRunActive() {
		return
	}
	m.agentLoop.execution.Errors = append(m.agentLoop.execution.Errors,
		agent.NewError(agent.ErrorTruncated, "executor", errors.New("response was cut off by max_tokens")))
	m.agentLoop.execution.NewEvidence = true
}

func (m *Model) recordAgentToolResults(results []tools.Result, denied bool) {
	if !m.agentRunActive() {
		return
	}
	for _, result := range results {
		kind := agent.ErrorKind("")
		if result.Err != nil {
			kind = classifyToolError(result, denied)
		}
		record := agent.ToolCallRecord{
			ID: result.Call.ID, Name: result.Call.Tool, Succeeded: result.Err == nil,
			ErrorKind: kind, Summary: map[bool]string{true: "completed", false: "failed"}[result.Err == nil],
		}
		m.agentLoop.execution.ToolCalls = append(m.agentLoop.execution.ToolCalls, record)
		if result.Err != nil {
			m.agentLoop.execution.Errors = append(m.agentLoop.execution.Errors, agent.NewError(kind, result.Call.Tool, result.Err))
		}
		if result.Err == nil && result.Call.Tool == tools.ToolWriteFile && strings.TrimSpace(result.Call.Path) != "" {
			m.agentLoop.execution.ChangedFiles = append(m.agentLoop.execution.ChangedFiles, result.Call.Path)
			m.agentLoop.execution.Artifacts = append(m.agentLoop.execution.Artifacts, result.Call.Path)
		}
		if result.Call.Tool == tools.ToolRunCommand && looksLikeTestCommand(result.Call.Body) {
			m.agentLoop.execution.TestsRun = append(m.agentLoop.execution.TestsRun, agent.TestResult{
				Name: truncateAgentText(strings.TrimSpace(result.Call.Body), 256), Passed: result.Err == nil,
				Summary: map[bool]string{true: "command passed", false: "command failed"}[result.Err == nil],
			})
		}
	}
	if denied {
		m.agentLoop.execution.NeedsUserInput = true
	}
	m.agentLoop.execution.NewEvidence = true
}

func classifyToolError(result tools.Result, denied bool) agent.ErrorKind {
	errorText := ""
	if result.Err != nil {
		errorText = strings.ToLower(result.Err.Error())
	}
	switch {
	case denied || errors.Is(result.Err, tools.ErrDenied):
		return agent.ErrorPermissionDenied
	case result.Call.InputErr != "":
		return agent.ErrorToolValidation
	case errors.Is(result.Err, context.Canceled):
		return agent.ErrorCancelled
	case errors.Is(result.Err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(result.Err.Error()), "timed out"):
		return agent.ErrorTimeout
	case strings.Contains(errorText, "outside the workspace") || strings.Contains(errorText, " is not allowed"):
		return agent.ErrorSafety
	default:
		return agent.ErrorToolExecution
	}
}

func looksLikeTestCommand(command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	return strings.HasPrefix(command, "go test") || strings.HasPrefix(command, "go vet") ||
		strings.HasPrefix(command, "make test") || strings.HasPrefix(command, "npm test") ||
		strings.HasPrefix(command, "pytest") || strings.HasPrefix(command, "cargo test")
}

func (m *Model) agentToolBudgetExceeded(incoming int) bool {
	return m.agentRunActive() && len(m.agentLoop.execution.ToolCalls)+incoming > m.agentLoop.run.Limits.MaxToolCalls
}

func (m *Model) agentToolLimitResults(calls []tools.Call) []tools.Result {
	limit := m.agentLoop.run.Limits.MaxToolCalls
	err := fmt.Errorf("agent tool-call budget exhausted (maximum %d); this call was not executed. Stop requesting tools and report the observable state", limit)
	results := make([]tools.Result, len(calls))
	for i, call := range calls {
		results[i] = tools.Result{Call: call, Err: err}
	}
	return results
}

func cmdAgent(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	switch sub {
	case "", "status":
		if m.agentLoop != nil && m.agentLoop.run != nil {
			run := m.agentLoop.run
			m.notice = fmt.Sprintf("agent mode %s · run %s · cycle %d/%d · %s/%s", onOff(m.agentOn), shortRunID(run.ID), run.Cycle, run.Limits.MaxCycles, run.Stage, run.Status)
		} else {
			m.notice = "agent mode " + onOff(m.agentOn) + " · no run"
		}
		return nil
	case "on":
		m.agentOn = true
		m.notice = "agent mode on — the next message starts a bounded verified run"
		return nil
	case "off":
		if m.agentRunActive() || m.agentVerifying() {
			return m.fail("an agent run is active; use /agent cancel before turning agent mode off")
		}
		m.agentOn = false
		m.notice = "agent mode off — ordinary chat behavior restored"
		return nil
	case "cancel":
		if !m.agentRunActive() && !m.agentVerifying() {
			return m.fail("no active agent run")
		}
		if m.thinking && m.cancelStream != nil {
			m.cancelStream()
			m.finishStream(nil, false)
		}
		if m.mcpBatchCancel != nil {
			m.mcpBatchCancel()
			m.mcpBatchCancel = nil
			m.mcpBatchGen++
			m.activity = nil
			m.relayout()
		}
		m.cancelVerifiedRun("cancelled by /agent cancel")
		m.endAgentRun()
		m.notice = "agent run cancelled"
		return m.persistAgentRun()
	case "resume":
		if m.busy() {
			return m.fail("work is already in progress; cancel it before resuming another run")
		}
		if m.agentLoop == nil || m.agentLoop.store == nil {
			return m.fail("agent persistence is unavailable (check agent.persist, agent.path, and privacy.store_prompts)")
		}
		store := m.agentLoop.store
		id := strings.TrimSpace(rest)
		return func() tea.Msg {
			var run *agent.AgentRun
			var err error
			if id == "" || id == "latest" {
				run, err = store.Latest(context.Background())
			} else {
				run, err = store.Load(context.Background(), id)
			}
			return agentResumeMsg{run: run, err: err}
		}
	default:
		return m.fail("usage: /agent [on|off|status|cancel|resume [run-id]]")
	}
}

func (m *Model) handleAgentResume(msg agentResumeMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.errText = "resume agent run: " + msg.err.Error()
		m.refreshViewport()
		return m, nil
	}
	if msg.run == nil {
		m.errText = "resume agent run: empty state"
		m.refreshViewport()
		return m, nil
	}
	next := msg.run.Objective
	if n := len(msg.run.Memory); n > 0 && strings.TrimSpace(msg.run.Memory[n-1].RecommendedNext) != "" {
		next = msg.run.Memory[n-1].RecommendedNext
	}
	if err := msg.run.Resume(next, time.Now()); err != nil {
		m.errText = "resume agent run: " + err.Error()
		m.refreshViewport()
		return m, nil
	}
	m.agentLoop.run = msg.run
	m.resetAgentContext()
	m.agentOn = true
	return m, tea.Batch(m.persistAgentRun(), m.startNextAgentCycle(next))
}

func shortRunID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func truncateAgentText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes] + "…"
}
