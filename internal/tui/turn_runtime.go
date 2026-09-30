package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// turnState is the shared provider/tool execution state used by ordinary
// chat and Agent mode. UI rendering and conversation policy remain on Model;
// this state machine owns lifecycle, cancellation, and stale-result identity.
type turnState string

const (
	turnIdle              turnState = "idle"
	turnModelStreaming    turnState = "model_streaming"
	turnWaitingApproval   turnState = "waiting_approval"
	turnWaitingUserInput  turnState = "waiting_user_input"
	turnExecutingTools    turnState = "executing_tools"
	turnProcessingResults turnState = "processing_results"
	turnCompleted         turnState = "completed"
	turnCancelled         turnState = "cancelled"
	turnFailed            turnState = "failed"
)

// turnOutcome is the typed boundary between the execution runtime and the
// Chat/Agent policies that decide what to render or schedule next.
type turnOutcome string

const (
	turnOutcomeNone             turnOutcome = ""
	turnOutcomeFinalAnswer      turnOutcome = "final_answer"
	turnOutcomeNeedsApproval    turnOutcome = "needs_approval"
	turnOutcomeNeedsUserInput   turnOutcome = "needs_user_input"
	turnOutcomeToolContinuation turnOutcome = "tool_continuation"
	turnOutcomeExecutionFailure turnOutcome = "execution_failure"
	turnOutcomeCancelled        turnOutcome = "cancelled"
)

type turnTransition struct {
	State   turnState
	Outcome turnOutcome
}

// turnRuntime owns all mutable state whose lifetime is one model/tool turn.
// It deliberately has no Bubble Tea dependency: Model.Update is an adapter
// that applies these transitions to session, rendering, and Agent policy.
type turnRuntime struct {
	state       turnState
	lastOutcome turnOutcome

	stream             <-chan provider.ChatEvent
	streamCtx          context.Context
	cancelStream       context.CancelFunc
	idleWatchdog       *time.Timer
	idleTimeout        time.Duration
	streamStart        time.Time
	streamGen          int
	streamToolCalls    []provider.ToolCall
	streamContinuation *provider.ProviderContinuation

	toolDepth                int
	toolRecoveryAttempts     int
	toolRecoveryReason       provider.ToolRecoveryReason
	lastToolRecoveryDecision provider.ToolRecoveryDecision
	emptyContinuationRetried bool
	malformedToolCallRetried bool
	hasHiddenToolRecovery    bool
	// streamReplayed records that the current model round already used its
	// single mid-stream interruption replay (see replayInterruptedStream).
	// It is independent of the tool-recovery budget: a transport drop says
	// nothing about the model's tool-call behavior.
	streamReplayed  bool
	pendingCalls    []tools.Call
	pendingToolPlan *toolBatchPlan
	pendingBudget   bool
	approvalIdx     int

	// frozenSystem is the system message of this turn's first request, reused
	// verbatim by its native-tool continuations (see frozenSystemFor).
	frozenSystem frozenSystemPrompt

	mcpBatchCancel context.CancelFunc
	mcpBatchGen    int
	// userCancelledGen is the generation of a batch the user cancelled
	// (Ctrl+C, Esc, /agent cancel) whose results have not arrived yet. Its
	// calls may already have changed the workspace, so its results are kept
	// rather than dropped (see acceptCancelledToolResults). A batch that is
	// superseded instead — a new submission or a newer batch starts before
	// they arrive — clears it, and its results stay stale as before. Zero
	// means none.
	userCancelledGen int
	activity         *toolActivity
	progress         *progressLedger
}

func newTurnRuntime(progressThreshold int, progressRoot string) turnRuntime {
	return turnRuntime{
		state:    turnIdle,
		progress: newProgressLedger(progressThreshold, progressRoot),
	}
}

func (r *turnRuntime) transition(state turnState, outcome turnOutcome) turnTransition {
	r.state = state
	r.lastOutcome = outcome
	return turnTransition{State: state, Outcome: outcome}
}

func (r *turnRuntime) resetTurn(progressThreshold int, progressRoot string) {
	r.stopStream()
	r.cancelToolBatch()
	r.userCancelledGen = 0 // a new turn supersedes a cancelled batch
	r.frozenSystem = frozenSystemPrompt{}
	r.resetCycle()
	r.pendingCalls = nil
	r.pendingToolPlan = nil
	r.pendingBudget = false
	r.approvalIdx = 0
	r.progress = newProgressLedger(progressThreshold, progressRoot)
	r.transition(turnIdle, turnOutcomeNone)
}

func (r *turnRuntime) resetCycle() {
	r.toolDepth = 0
	r.toolRecoveryAttempts = 0
	r.toolRecoveryReason = ""
	r.lastToolRecoveryDecision = provider.ToolRecoveryDecision{}
	r.emptyContinuationRetried = false
	r.malformedToolCallRetried = false
	r.hasHiddenToolRecovery = false
	r.streamReplayed = false
	if !r.busy() {
		r.transition(turnIdle, turnOutcomeNone)
	}
}

func (r *turnRuntime) advanceToolRound() {
	r.toolDepth++
}

func (r *turnRuntime) renewToolBudget() {
	r.toolDepth = 0
}

func (r *turnRuntime) claimMalformedToolRetry() bool {
	decision := r.claimToolRecovery(provider.ToolRecoveryMalformedCall)
	if decision.Allowed() {
		r.malformedToolCallRetried = true
	}
	return decision.Allowed()
}

func (r *turnRuntime) clearMalformedToolRetry() {
	r.malformedToolCallRetried = false
}

func (r *turnRuntime) claimHiddenToolRecovery() bool {
	decision := r.claimToolRecovery(provider.ToolRecoveryHiddenMCPTool)
	if decision.Allowed() {
		r.hasHiddenToolRecovery = true
	}
	return decision.Allowed()
}

func (r *turnRuntime) claimEmptyContinuationRetry() bool {
	decision := r.claimToolRecovery(provider.ToolRecoveryEmptyContinuation)
	if decision.Allowed() {
		r.emptyContinuationRetried = true
	}
	return decision.Allowed()
}

// claimStreamReplay grants at most one interruption replay per model round.
func (r *turnRuntime) claimStreamReplay() bool {
	if r.streamReplayed {
		return false
	}
	r.streamReplayed = true
	return true
}

// clearStreamReplay renews the replay allowance once a round completes.
func (r *turnRuntime) clearStreamReplay() {
	r.streamReplayed = false
}

func (r *turnRuntime) clearEmptyContinuationRetry() {
	r.emptyContinuationRetried = false
}

// claimToolRecovery enforces one recovery reissue across all categories in a
// turn. Keeping the budget shared prevents alternating parser, discovery, and
// empty-output failures from multiplying requests.
func (r *turnRuntime) claimToolRecovery(reason provider.ToolRecoveryReason) provider.ToolRecoveryDecision {
	const maxToolRecoveryAttempts = 1
	if r.toolRecoveryAttempts >= maxToolRecoveryAttempts {
		r.lastToolRecoveryDecision = provider.ToolRecoveryDecision{Reason: reason, Outcome: provider.ToolRecoveryExhausted}
		return r.lastToolRecoveryDecision
	}
	r.toolRecoveryAttempts++
	r.toolRecoveryReason = reason
	r.lastToolRecoveryDecision = provider.ToolRecoveryDecision{
		Reason: reason, Attempt: r.toolRecoveryAttempts, Outcome: provider.ToolRecoveryScheduled,
	}
	return r.lastToolRecoveryDecision
}

func (r *turnRuntime) busy() bool {
	return r.state == turnModelStreaming || r.state == turnExecutingTools
}

func (r *turnRuntime) beginStream(parent context.Context, idle time.Duration) (context.Context, int, error) {
	if r.state == turnModelStreaming || r.state == turnExecutingTools || r.state == turnWaitingApproval || r.state == turnWaitingUserInput {
		return nil, r.streamGen, fmt.Errorf("cannot start provider request while turn is %s", r.state)
	}
	ctx, cancel := context.WithCancelCause(parent)
	watchdog := time.AfterFunc(idle, func() { cancel(errStreamIdle) })
	r.streamCtx = ctx
	r.idleWatchdog = watchdog
	r.idleTimeout = idle
	r.cancelStream = func() {
		watchdog.Stop()
		cancel(context.Canceled)
	}
	r.stream = nil
	r.streamStart = time.Now()
	r.streamToolCalls = nil
	r.streamContinuation = nil
	r.streamGen++
	r.transition(turnModelStreaming, turnOutcomeNone)
	return ctx, r.streamGen, nil
}

func (r *turnRuntime) waitForUserInput() turnTransition {
	return r.transition(turnWaitingUserInput, turnOutcomeNeedsUserInput)
}

func (r *turnRuntime) continueAfterUserInput() turnTransition {
	return r.transition(turnProcessingResults, turnOutcomeToolContinuation)
}

func (r *turnRuntime) adoptStream(gen int, stream <-chan provider.ChatEvent) bool {
	if !r.acceptStreamEvent(gen) {
		return false
	}
	r.stream = stream
	return true
}

func (r *turnRuntime) acceptStreamEvent(gen int) bool {
	// Idle acceptance supports direct deterministic event injection in the
	// package tests. Production requests enter turnModelStreaming in
	// beginStream before any event can be delivered.
	if r.state == turnIdle && gen == r.streamGen {
		r.transition(turnModelStreaming, turnOutcomeNone)
	}
	return r.state == turnModelStreaming && gen == r.streamGen
}

func (r *turnRuntime) touchStream() {
	if r.state == turnModelStreaming && r.idleWatchdog != nil {
		r.idleWatchdog.Reset(r.idleTimeout)
	}
}

func (r *turnRuntime) streamCanceledByIdle() bool {
	return r.streamCtx != nil && context.Cause(r.streamCtx) == errStreamIdle
}

func (r *turnRuntime) finishStream(outcome turnOutcome) (<-chan provider.ChatEvent, turnTransition) {
	stream := r.stopStream()
	switch outcome {
	case turnOutcomeFinalAnswer:
		return stream, r.transition(turnCompleted, outcome)
	case turnOutcomeExecutionFailure:
		return stream, r.transition(turnFailed, outcome)
	case turnOutcomeCancelled:
		return stream, r.transition(turnCancelled, outcome)
	default:
		return stream, r.transition(turnProcessingResults, outcome)
	}
}

func (r *turnRuntime) stopStream() <-chan provider.ChatEvent {
	stream := r.stream
	if r.cancelStream != nil {
		r.cancelStream()
	}
	r.cancelStream = nil
	r.idleWatchdog = nil
	r.streamCtx = nil
	r.stream = nil
	return stream
}

func (r *turnRuntime) waitForApproval(plan toolBatchPlan, budget bool) turnTransition {
	r.pendingCalls = plan.runnableCalls()
	r.pendingToolPlan = &plan
	r.pendingBudget = budget
	r.approvalIdx = 0
	return r.transition(turnWaitingApproval, turnOutcomeNeedsApproval)
}

func (r *turnRuntime) pendingPlan() toolBatchPlan {
	if r.pendingToolPlan != nil {
		return *r.pendingToolPlan
	}
	return newToolBatchPlan(r.pendingCalls)
}

func (r *turnRuntime) clearPendingTools() {
	r.pendingCalls = nil
	r.pendingToolPlan = nil
	r.pendingBudget = false
	if r.state == turnWaitingApproval {
		r.transition(turnProcessingResults, turnOutcomeToolContinuation)
	}
}

func (r *turnRuntime) beginToolBatch(parent context.Context, calls []tools.Call) (context.Context, int, error) {
	if r.state == turnModelStreaming || r.state == turnExecutingTools {
		return nil, r.mcpBatchGen, fmt.Errorf("cannot start tool batch while turn is %s", r.state)
	}
	r.clearPendingTools()
	r.advanceToolRound()
	r.userCancelledGen = 0 // a newer batch supersedes a cancelled one
	ctx, cancel := context.WithCancel(parent)
	r.mcpBatchCancel = cancel
	r.mcpBatchGen++
	r.activity = newToolActivity(calls, r.mcpBatchGen)
	r.transition(turnExecutingTools, turnOutcomeNone)
	return ctx, r.mcpBatchGen, nil
}

func (r *turnRuntime) acceptToolResults(gen int) bool {
	if r.state != turnExecutingTools || gen != r.mcpBatchGen {
		return false
	}
	if r.mcpBatchCancel != nil {
		r.mcpBatchCancel()
	}
	r.mcpBatchCancel = nil
	r.activity = nil
	r.transition(turnProcessingResults, turnOutcomeToolContinuation)
	return true
}

func (r *turnRuntime) cancelToolBatch() bool {
	if r.mcpBatchCancel == nil {
		return false
	}
	r.mcpBatchCancel()
	r.mcpBatchCancel = nil
	r.mcpBatchGen++
	r.activity = nil
	r.transition(turnCancelled, turnOutcomeCancelled)
	return true
}

// cancelToolBatchByUser cancels the running batch like cancelToolBatch and
// remembers its generation, so its results — which may include calls that
// already completed — are adopted when they arrive instead of dropped.
func (r *turnRuntime) cancelToolBatchByUser() bool {
	gen := r.mcpBatchGen
	if !r.cancelToolBatch() {
		return false
	}
	r.userCancelledGen = gen
	return true
}

// acceptCancelledToolResults reports whether gen is the user-cancelled
// batch still awaiting its results, and consumes that marker. It never
// resumes the turn: the caller records the results and stops.
func (r *turnRuntime) acceptCancelledToolResults(gen int) bool {
	if gen == 0 || gen != r.userCancelledGen {
		return false
	}
	r.userCancelledGen = 0
	return true
}

// frozenSystemPrompt is one turn's frozen system message. key names the
// provider and model it was composed for; static is prompt.StaticSystem of
// that request's input.
type frozenSystemPrompt struct {
	key    string
	static string
	system string
	// turnRaw and turnContext record the raw user message of the turn's
	// first request and the runtime-context message placed just before it
	// (prompt.fresh_runtime_context). Continuations repeat that context
	// message verbatim at the same place so the prompt prefix stays reusable
	// through the raw user message.
	turnRaw     string
	turnContext string
}

// freezeSystem records the system message a request was sent with. The
// turn's first context message, if any, is kept.
func (r *turnRuntime) freezeSystem(key, static, system string) {
	r.frozenSystem.key, r.frozenSystem.static, r.frozenSystem.system = key, static, system
}

// freezeTurnContext records the runtime-context message a turn's first
// request placed immediately before its raw user message.
func (r *turnRuntime) freezeTurnContext(raw, context string) {
	r.frozenSystem.turnRaw, r.frozenSystem.turnContext = raw, context
}

// frozenTurnContext returns the turn's first runtime-context message and the
// raw user message it preceded, or empty strings when there is none.
func (r *turnRuntime) frozenTurnContext() (raw, context string) {
	return r.frozenSystem.turnRaw, r.frozenSystem.turnContext
}

// frozenSystemFor returns the frozen system message when it was composed for
// the same provider and model and its static part is unchanged. A change to
// a static section mid-turn (a loaded skill, disclosed tools, a protocol
// fallback) must reach the model, so it disables reuse until the next
// request freezes a new system message.
func (r *turnRuntime) frozenSystemFor(key, static string) (string, bool) {
	f := r.frozenSystem
	if f.system == "" || f.key != key || f.static != static {
		return "", false
	}
	return f.system, true
}

func (r *turnRuntime) complete(outcome turnOutcome) turnTransition {
	switch outcome {
	case turnOutcomeFinalAnswer, turnOutcomeExecutionFailure, turnOutcomeCancelled:
		r.frozenSystem = frozenSystemPrompt{} // the turn is over
	}
	switch outcome {
	case turnOutcomeFinalAnswer:
		return r.transition(turnCompleted, outcome)
	case turnOutcomeExecutionFailure:
		return r.transition(turnFailed, outcome)
	case turnOutcomeCancelled:
		return r.transition(turnCancelled, outcome)
	case turnOutcomeNeedsApproval:
		return r.transition(turnWaitingApproval, outcome)
	default:
		return r.transition(turnProcessingResults, outcome)
	}
}
