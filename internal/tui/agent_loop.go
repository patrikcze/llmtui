package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/agentverify"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

const maxAgentDirectiveBytes = 12 * 1024

type agentLoopState struct {
	run   *agent.AgentRun
	store agent.Store
	// historyStart is the first session message owned by this run. The first
	// cycle may use prior conversation for natural follow-up requests, but
	// verifier-requested retry cycles are scoped from this index so completed
	// runs and their synthetic controller turns cannot become active work.
	historyStart int
	// cycleBoundaries[i] is the session-message index at which cycle i+2
	// began (cycle 1 always starts at historyStart, so it needs no entry).
	// requestHistory uses these to identify which messages belong to a
	// completed cycle versus the in-progress one, so it can project away a
	// completed cycle's raw tool-call/tool-result exchange (the executor
	// already has that cycle's outcome via the bounded run.Memory recap in
	// its system prompt) while leaving the current cycle's messages, and
	// the current run's tool state, untouched.
	cycleBoundaries []int
	ctx             context.Context
	runCancel       context.CancelFunc
	execution       agent.ExecutionResult
	verifying       bool
	verifyCancel    context.CancelFunc
	verifyGen       int
	// verifierAttempts counts verifier-inference attempts made for the
	// current cycle's verification (transport/format failures only —
	// agentverify.Verify's own internal malformed-JSON repair is a separate,
	// earlier layer and does not increment this). Reset to 0 at the start of
	// each cycle's verification in startAgentVerification. Exhausting
	// m.cfg.Agent.Verifier.MaxAttempts parks the cycle as
	// agent.DecisionVerificationUnavailable instead of restarting the
	// executor.
	verifierAttempts int
	persistErr       error
	// liveToolCalls is the run's true cumulative tool-call count, updated as
	// each round completes. Unlike execution.ToolCalls (reset every cycle by
	// startNextAgentCycle), this never resets for the life of the run, so
	// the live budget check in agentHardBudgetExceeded compares against the
	// same run-level ceiling agent.Decide would eventually enforce at a
	// cycle boundary — without waiting for that boundary to be reached.
	liveToolCalls int
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

// openAgentQuestionPicker presents the executor's own discrete choices (as
// extracted by the verifier into VerificationResult.UserOptions) as a
// pickable overlay, instead of leaving the question as plain errText that
// only a free-typed reply can answer. question is the executor's actual
// question (stop.Reason, which already carries the verifier's summary
// verbatim). Extraction is a small-model output and not guaranteed
// exhaustive or correct, so Esc always falls back to the normal free-text
// input box — this overlay is a convenience, never a hard constraint.
func (m *Model) openAgentQuestionPicker(question string, options []string) {
	m.pickerKind = pickerAgentQuestion
	m.pickerHeader = question
	m.pickerItems = append([]string{}, options...)
	m.pickerIdx = 0
	m.overlayOpen = true
	m.renderPicker()
}

func (m *Model) agentQuestionPickerOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("agent needs your input") + "\n\n")
	b.WriteString(m.theme.UserLabel.Render(m.pickerHeader) + "\n\n")
	for i, option := range m.pickerItems {
		marker := "  "
		label := m.theme.SystemNote.Render(option)
		if i == m.pickerIdx {
			marker = m.theme.BadgeOK.Render("▸ ")
			label = m.theme.BadgeOK.Render(option)
		}
		b.WriteString(zone.Mark(pickerRowZoneID(i), marker+label) + "\n")
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("↑/↓ pick · enter confirm · esc type a custom answer instead"))
	return b.String()
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
	m.agentLoop.historyStart = len(m.session.Messages)
	m.agentLoop.cycleBoundaries = nil
	m.resetAgentContext()
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	m.agentLoop.liveToolCalls = 0
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
	boundary := len(m.session.Messages)
	if err := run.BeginCycle(objective, append(m.agentContextSources(), "new_user_input"), time.Now()); err != nil {
		m.failVerifiedRun(err)
		m.errText = "resume agent run: " + err.Error()
		m.refreshViewport()
		return m.persistAgentRun()
	}
	m.agentLoop.cycleBoundaries = append(m.agentLoop.cycleBoundaries, boundary)
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	m.toolDepth = 0
	m.bypassCache = true
	m.notice = fmt.Sprintf("agent %s · cycle %d/%d · resumed with user input", shortRunID(run.ID), run.Cycle, run.Limits.MaxCycles)
	return tea.Batch(m.dispatch(input, images), m.persistAgentRun())
}

// agentContinueDirective is the synthetic "continue" turn the controller
// sends the executor to drive it into its next bounded-objective cycle. It
// is machinery, not something the user typed — refreshViewport renders it
// as a controller status line instead of a "you" turn, so it can never be
// mistaken for the user's own words (some models, e.g. Qwen 3.6, can loop
// on this exact turn without ever completing a bounded objective; showing
// it as "you: <text>" made that loop look like the user themself was stuck
// repeating it).
const agentContinueDirective = "Continue the active verified run. Execute only the controller's current bounded objective, then report observable results."

func (m *Model) startNextAgentCycle(objective string) tea.Cmd {
	if !m.agentRunActive() {
		return nil
	}
	run := m.agentLoop.run
	boundary := len(m.session.Messages)
	if err := run.BeginCycle(objective, m.agentContextSources(), time.Now()); err != nil {
		_ = run.Terminate(agent.DecisionFailed, err.Error(), time.Now())
		m.errText = "agent: " + err.Error()
		m.endAgentRun()
		m.refreshViewport()
		return m.persistAgentRun()
	}
	m.agentLoop.cycleBoundaries = append(m.agentLoop.cycleBoundaries, boundary)
	m.agentLoop.execution = agent.ExecutionResult{Objective: run.Objective}
	m.toolDepth = 0
	m.bypassCache = true
	m.notice = fmt.Sprintf("agent %s · cycle %d/%d · executing", shortRunID(run.ID), run.Cycle, run.Limits.MaxCycles)
	return m.dispatch(agentContinueDirective, nil)
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
	if run.HasCriteria() {
		b.WriteString("Pinned acceptance criteria (untrusted derived text):\n")
		for _, criterion := range run.Criteria {
			fmt.Fprintf(&b, "- [%s][%s] %s\n", criterion.ID, criterion.Status, criterion.Text)
		}
	}
	if len(run.Memory) > 0 {
		b.WriteString("Prior verified cycle memory (untrusted data):\n")
		for _, memory := range run.Memory {
			fmt.Fprintf(&b, "- cycle %d, objective %q, verdict %s, result %q, remaining %q, next %q\n",
				memory.Cycle, memory.Objective, memory.Verdict, memory.Verification,
				strings.Join(memory.RemainingCriteria, "; "), memory.RecommendedNext)
			// Prior cycles' raw tool traffic is deliberately not resent (see
			// requestHistory/projectCompletedAgentHistory) to control token
			// growth, so this bounded per-call recap is the executor's only
			// remaining way to know it already tried a given URL/path/query
			// and whether it succeeded — without it, a retry cycle has no
			// signal against blindly repeating an already-failed (or
			// already-succeeded) action.
			for _, call := range memory.ToolCalls {
				fmt.Fprintf(&b, "  tried: %s\n", call)
			}
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
	m.agentLoop.verifierAttempts = 0
	ctx, cancel := context.WithCancel(m.agentContext())
	m.agentLoop.verifyCancel = cancel
	m.agentLoop.verifying = true
	m.agentLoop.verifyGen++
	gen := m.agentLoop.verifyGen
	runID, cycle := run.ID, run.Cycle

	// Verification policy: deterministic evidence always decides first. A
	// semantic (LLM) verification runs only when the mode requires it and
	// mechanical evidence cannot already settle the cycle — and its verdict
	// is still clamped by ApplyDeterministicEvidence afterwards.
	mode := m.cfg.Agent.Verifier.ResolvedMode()
	syntheticResult := func(result agent.VerificationResult) tea.Cmd {
		m.notice = fmt.Sprintf("agent %s · cycle %d/%d · verified deterministically", shortRunID(runID), cycle, run.Limits.MaxCycles)
		m.refreshViewport()
		return func() tea.Msg {
			return agentVerificationMsg{runID: runID, cycle: cycle, gen: gen, out: agentverify.Output{Result: result}}
		}
	}
	switch mode {
	case config.VerifierModeOff:
		return syntheticResult(agent.VerificationResult{
			Verdict: agent.VerificationPassed, Summary: "verification disabled; executor output was not verified",
			Evidence: []string{"verification mode off"}, Confidence: 0,
		})
	case config.VerifierModeDeterministic:
		return syntheticResult(agentverify.ApplyDeterministicEvidence(agent.VerificationResult{
			Verdict: agent.VerificationPassed, Summary: "no deterministic failure was observed",
			Evidence: []string{"deterministic-only verification configured"}, Confidence: 0.5,
		}, execution))
	case config.VerifierModeAdaptive:
		// A conclusive mechanical failure/blockage would override any
		// semantic verdict anyway — skip the inference entirely.
		if deterministic, conclusive := agent.EvaluateDeterministic(execution); conclusive {
			return syntheticResult(deterministic)
		}
		if run.HasCriteria() && len(run.UnresolvedCriteria()) == 0 {
			return syntheticResult(agent.VerificationResult{
				Verdict: agent.VerificationPassed, Summary: "all pinned acceptance criteria are satisfied",
				Evidence: []string{"criteria ledger resolved"}, Confidence: 1,
			})
		}
		// Never skip semantic verification on a run's first cycle, even when
		// execution looks mechanically complete: "every tool call I happened
		// to make succeeded" is not the same claim as "I did everything the
		// request asked for" — only a real verifier pass ever decomposes the
		// request into checkable criteria at all. A run whose very first
		// cycle takes this shortcut can reach "done" having never had its
		// content checked against the request, silently dropping requirements
		// a less careful executor didn't attempt (observed: a multi-part
		// request whose file-write step never happened still exited via this
		// path with the same confidence a fully complete run gets). Cycles
		// after the first may still use the fast path once criteria exist.
		if !run.HasCriteria() && run.Cycle != 1 && agent.MechanicallyComplete(execution) {
			return syntheticResult(agent.VerificationResult{
				Verdict: agent.VerificationPassed, Summary: "deterministic evidence is sufficient: all tool calls and tests succeeded",
				Evidence: []string{"mechanically complete cycle"}, Confidence: 0.7,
			})
		}
	case config.VerifierModeAlways:
		// Semantic verification runs after every cycle; deterministic
		// evidence still clamps its verdict via ApplyDeterministicEvidence.
	}
	m.notice = fmt.Sprintf("agent %s · cycle %d/%d · verifying in fresh context", shortRunID(runID), cycle, run.Limits.MaxCycles)
	m.refreshViewport()

	return m.dispatchVerifierAttempt(run, execution, ctx, gen)
}

// dispatchVerifierAttempt builds and sends one fresh-context, tool-free
// verifier inference request for the run's current cycle. ctx and gen must
// already be established by the caller (a new context.WithCancel derived
// from m.agentContext, and the value captured immediately after the
// caller's own m.agentLoop.verifyGen++) so every dispatch — the first
// attempt from startAgentVerification and every bounded retry from
// handleAgentVerification — is gated by the same staleness guard at the top
// of handleAgentVerification.
func (m *Model) dispatchVerifierAttempt(run *agent.AgentRun, execution agent.ExecutionResult, ctx context.Context, gen int) tea.Cmd {
	runID, cycle := run.ID, run.Cycle
	input := agentverify.Input{
		RunID: runID, Cycle: cycle, Task: run.Request, Objective: run.Objective,
		AcceptanceCriteria: []string{run.Request},
		Criteria:           run.Criteria, Evidence: run.Evidence, PriorCycles: run.Memory,
		EstablishCriteria: !run.HasCriteria(),
		Execution:         execution,
		Tools:             activeToolNames(m.activeToolSpecs()),
	}
	model := strings.TrimSpace(m.cfg.Agent.Verifier.Model)
	if model == "" {
		model = m.model
	}
	maxTokens := m.cfg.Agent.Verifier.MaxTokens
	timeout, _ := time.ParseDuration(m.cfg.Agent.Verifier.Timeout)
	prov := m.prov
	admit := m.verifierRequestAdmission(run)
	return func() tea.Msg {
		out, err := agentverify.Verify(ctx, prov, agentverify.Config{
			Model: model, MaxTokens: maxTokens, Timeout: timeout, AdmitRequest: admit,
		}, input)
		return agentVerificationMsg{runID: runID, cycle: cycle, gen: gen, out: out, err: err}
	}
}

// verifierRequestAdmission snapshots accounted usage and reserves each
// verifier request prospectively. The closure is private to one Verify call,
// so it also covers optional fallback and repair requests before dispatch.
func (m *Model) verifierRequestAdmission(run *agent.AgentRun) func(int, int) error {
	if run == nil || !m.cfg.Agent.EnforceBudgetsLive || run.Limits.MaxTokens <= 0 {
		return nil
	}
	used := run.PromptTokens + run.CompletionTokens
	limit := run.Limits.MaxTokens
	reserved := 0
	return func(promptEstimate, maxCompletion int) error {
		cost := max(promptEstimate, 0) + max(maxCompletion, 0)
		remaining := max(limit-used-reserved, 0)
		if cost > remaining {
			return agent.NewError(agent.ErrorBudget, "admit verifier request",
				fmt.Errorf("%w: verifier request needs up to %d tokens, but %d of %d remain",
					agent.ErrBudgetExhausted, cost, remaining, limit))
		}
		reserved += cost
		return nil
	}
}

// retryAgentVerification re-dispatches another verifier attempt for the
// current cycle after the previous attempt failed with a transport/format
// error (provider error, timeout, or agentverify.Verify's own malformed-JSON
// repair exhausted). It never touches executor state or begins a new cycle
// — only the verifier is retried, within the bound
// m.cfg.Agent.Verifier.MaxAttempts enforced by the caller. It mirrors the
// ctx/verifying/verifyGen setup startAgentVerification's first attempt uses
// so the retried call is gated by the exact same staleness guard: a
// cancellation or an even-later stale result cannot corrupt state.
func (m *Model) retryAgentVerification(run *agent.AgentRun) tea.Cmd {
	ctx, cancel := context.WithCancel(m.agentContext())
	m.agentLoop.verifyCancel = cancel
	m.agentLoop.verifying = true
	m.agentLoop.verifyGen++
	gen := m.agentLoop.verifyGen
	return m.dispatchVerifierAttempt(run, m.agentLoop.execution, ctx, gen)
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
		// A verifier transport/format failure (provider error, timeout, or
		// agentverify.Verify's own internal one-shot malformed-JSON repair
		// exhausted) is not the verifier rejecting the executor's work — it
		// is the verifier itself failing to produce a verdict at all.
		// Restarting the executor here would repeat its side effects for no
		// reason related to whether that work was correct, so this cycle's
		// verification gets its own small bounded retry budget instead of
		// being routed through the normal cycle-completion pipeline
		// (CompleteVerification/WriteMemory/Decide/ApplyStop) at all.
		//
		// Cancellation is deliberately not special-cased here: cancelVerifiedRun
		// bumps m.agentLoop.verifyGen synchronously before cancelling the
		// verify context, so a subsequently-arriving ErrorCancelled result
		// for this dispatch always carries a stale gen and is discarded by
		// the guard at the top of this function before reaching this branch
		// at all. See TestVerifiedAgentCancellationWhileVerifyingEndsCancelled.
		var runErr agent.RunError
		if !errors.As(msg.err, &runErr) {
			runErr = agent.NewError(agent.ErrorVerification, "verify", msg.err)
		}
		if runErr.Kind == agent.ErrorBudget {
			reason := runErr.Error()
			_ = run.Terminate(agent.DecisionBudgetExhausted, reason, time.Now())
			m.notice = fmt.Sprintf("agent %s · %s", shortRunID(run.ID), reason)
			m.endAgentRun()
			m.refreshViewport()
			return m, m.persistAgentRun()
		}
		m.agentLoop.verifierAttempts++
		maxAttempts := m.cfg.Agent.Verifier.MaxAttempts
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		if m.agentLoop.verifierAttempts < maxAttempts {
			m.notice = fmt.Sprintf("agent %s · cycle %d/%d · verifier attempt %d/%d failed (%s) — retrying the verifier only",
				shortRunID(run.ID), run.Cycle, run.Limits.MaxCycles, m.agentLoop.verifierAttempts, maxAttempts, runErr.Kind)
			m.refreshViewport()
			return m, m.retryAgentVerification(run)
		}
		reason := fmt.Sprintf("verifier unavailable after %d attempt(s): %s (%s) — the executor's result was preserved but never verified",
			maxAttempts, runErr.Message, runErr.Kind)
		if err := run.Terminate(agent.DecisionVerificationUnavailable, reason, time.Now()); err != nil {
			m.failVerifiedRun(err)
			return m, m.persistAgentRun()
		}
		m.syncAgentDebug()
		persist := m.persistAgentRun()
		m.errText = "agent verification unavailable: " + reason
		m.notice = fmt.Sprintf("agent %s · verification unavailable after %d attempt(s)", shortRunID(run.ID), maxAttempts)
		m.endAgentRun()
		m.refreshViewport()
		return m, persist
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
		if len(result.UserOptions) > 0 {
			m.openAgentQuestionPicker(stop.Reason, result.UserOptions)
		} else {
			m.errText = "agent needs user input: " + stop.Reason + ". What permitted alternative or missing fact should the next cycle use?"
		}
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

// toolCallDetail extracts the one argument most useful for recognizing
// "I already tried this exact thing" across agent cycles — a URL, file
// path, or search pattern. Deliberately narrow: unlike those, a
// run_command's full command line can carry an inline secret a user or
// model typed directly (e.g. curl -H "Authorization: Bearer ..."), so only
// its program name survives, never its arguments. MCP calls get the
// server/tool name only, never the raw per-server argument JSON.
func toolCallDetail(call tools.Call) string {
	switch call.Tool {
	case tools.ToolWebFetch, tools.ToolReadFile, tools.ToolWriteFile, tools.ToolListDir, tools.ToolSkillLoad:
		return call.Path
	case tools.ToolGrep, tools.ToolGlob:
		if call.Path != "" {
			return call.Body + " in " + call.Path
		}
		return call.Body
	case tools.ToolWebSearch:
		return call.Body
	case tools.ToolRunCommand:
		program, _, _ := strings.Cut(strings.TrimSpace(call.Body), " ")
		return program
	default:
		if call.MCPServer != "" {
			return call.MCPServer + "." + call.MCPTool
		}
		return ""
	}
}

// recordAgentToolResultsCount records the complete tool-result-shaped
// evidence while charging only calls that actually executed to the live
// tool-call budget. Synthetic progress blocks, denials, and budget rejections
// must preserve protocol correlation without pretending a side effect ran.
func (m *Model) recordAgentToolResultsCount(results []tools.Result, denied bool, liveCount int) {
	if !m.agentRunActive() {
		return
	}
	if liveCount < 0 {
		liveCount = 0
	}
	if liveCount > len(results) {
		liveCount = len(results)
	}
	m.agentLoop.liveToolCalls += liveCount
	for _, result := range results {
		kind := agent.ErrorKind("")
		if result.Err != nil {
			kind = classifyToolError(result, denied)
		}
		record := agent.ToolCallRecord{
			ID: result.Call.ID, Name: result.Call.Tool, Detail: toolCallDetail(result.Call), Succeeded: result.Err == nil,
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

// agentHardBudgetExceeded reports whether executing incoming more tool
// calls would already cross the run's hard tool-call or token ceiling,
// using true run-level running totals (m.agentLoop.liveToolCalls, and
// run.PromptTokens+CompletionTokens, both updated live every round) rather
// than the per-cycle execution.ToolCalls counter. That per-cycle counter
// resets every cycle, so on its own it cannot catch a tool-calling spree
// that never reaches the cycle-boundary agent.Decide() check — see
// docs/architecture/v1-audit.md §4.2 and
// docs/architecture/decisions/0002-live-progress-ledger-and-budget-enforcement.md.
func (m *Model) agentHardBudgetExceeded(incoming int) (exceeded bool, reason string) {
	if !m.agentRunActive() {
		return false, ""
	}
	if !m.cfg.Agent.EnforceBudgetsLive {
		// Reverted to the pre-v1 behavior: only agent.Decide() at the
		// cycle boundary enforces these budgets. See
		// docs/architecture/v1-audit.md §4.2 for why that alone can
		// under-enforce, and docs/architecture/v1-migration-plan.md for
		// when disabling this is an appropriate rollback.
		return false, ""
	}
	run := m.agentLoop.run
	if m.agentLoop.liveToolCalls+incoming > run.Limits.MaxToolCalls {
		return true, fmt.Sprintf("agent tool-call budget exhausted (maximum %d)", run.Limits.MaxToolCalls)
	}
	if run.Limits.MaxTokens > 0 && run.PromptTokens+run.CompletionTokens >= run.Limits.MaxTokens {
		return true, fmt.Sprintf("agent token budget exhausted (maximum %d)", run.Limits.MaxTokens)
	}
	return false, ""
}

// agentModelRequestBudgetExceeded admits an executor request only when its
// estimated prompt plus maximum completion fits after actual prior usage.
func (m *Model) agentModelRequestBudgetExceeded(kind string, promptEstimate, maxCompletion int) (bool, string) {
	if !m.agentRunActive() || !m.cfg.Agent.EnforceBudgetsLive {
		return false, ""
	}
	run := m.agentLoop.run
	if run.Limits.MaxTokens <= 0 {
		return false, ""
	}
	used := run.PromptTokens + run.CompletionTokens
	cost := max(promptEstimate, 0) + max(maxCompletion, 0)
	remaining := max(run.Limits.MaxTokens-used, 0)
	if cost <= remaining {
		return false, ""
	}
	return true, fmt.Sprintf("agent token budget admission rejected %s request: needs up to %d tokens, but %d of %d remain",
		kind, cost, remaining, run.Limits.MaxTokens)
}

func (m *Model) terminateAgentModelRequestBudget(reason string) tea.Cmd {
	if !m.agentRunActive() {
		return nil
	}
	run := m.agentLoop.run
	_ = run.Terminate(agent.DecisionBudgetExhausted, reason, time.Now())
	m.notice = fmt.Sprintf("agent %s · %s", shortRunID(run.ID), reason)
	m.endAgentRun()
	m.refreshViewport()
	return m.persistAgentRun()
}

// terminateAgentBudget stops the run immediately when a hard tool-call or
// token ceiling is crossed mid-cycle, rather than rejecting the call and
// asking the model to try again. The reject-and-continue shape was tried
// first and rejected: since the executor keeps offering tool calls every
// turn in exactly the failure mode this guards against, rejecting one call
// only invited another rejected attempt next turn, so the loop kept
// consuming provider round-trips (the token-burn part of the reported
// failure) even though no tool was actually executing anymore. Terminating
// outright matches master-prompt §7.1's "maximum tool calls"/"maximum
// tokens" deterministic terminal outcomes.
//
// The rejected calls still get a structured result appended to the
// session so native tool-call/result correlation holds for the persisted
// transcript (master-prompt §7.3: never silently drop a result) — the run
// just doesn't continue past it.
func (m *Model) terminateAgentBudget(calls []tools.Call, reason string) tea.Cmd {
	err := fmt.Errorf("%s; this call was not executed. Stop requesting tools and report the observable state", reason)
	results := make([]tools.Result, len(calls))
	for i, call := range calls {
		results[i] = tools.Result{Call: call, Err: err}
	}
	m.recordAgentToolResultsCount(results, false, 0)
	m.appendTerminalToolResults(results)
	m.toolErr += len(results)
	run := m.agentLoop.run
	_ = run.Terminate(agent.DecisionBudgetExhausted, reason, time.Now())
	m.notice = fmt.Sprintf("agent %s · %s", shortRunID(run.ID), reason)
	m.endAgentRun()
	m.refreshViewport()
	return m.persistAgentRun()
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
	m.agentLoop.historyStart = len(m.session.Messages)
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
