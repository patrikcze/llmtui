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

	stream          <-chan provider.ChatEvent
	streamCtx       context.Context
	cancelStream    context.CancelFunc
	idleWatchdog    *time.Timer
	idleTimeout     time.Duration
	streamStart     time.Time
	streamGen       int
	streamToolCalls []provider.ToolCall

	toolDepth                int
	emptyContinuationRetried bool
	malformedToolCallRetried bool
	pendingCalls             []tools.Call
	pendingToolPlan          *toolBatchPlan
	pendingBudget            bool
	approvalIdx              int

	mcpBatchCancel context.CancelFunc
	mcpBatchGen    int
	activity       *toolActivity
	progress       *progressLedger
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
	r.emptyContinuationRetried = false
	r.malformedToolCallRetried = false
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
	if r.malformedToolCallRetried {
		return false
	}
	r.malformedToolCallRetried = true
	return true
}

func (r *turnRuntime) clearMalformedToolRetry() {
	r.malformedToolCallRetried = false
}

func (r *turnRuntime) claimEmptyContinuationRetry() bool {
	if r.emptyContinuationRetried {
		return false
	}
	r.emptyContinuationRetried = true
	return true
}

func (r *turnRuntime) clearEmptyContinuationRetry() {
	r.emptyContinuationRetried = false
}

func (r *turnRuntime) busy() bool {
	return r.state == turnModelStreaming || r.state == turnExecutingTools
}

func (r *turnRuntime) beginStream(parent context.Context, idle time.Duration) (context.Context, int, error) {
	if r.state == turnModelStreaming || r.state == turnExecutingTools || r.state == turnWaitingApproval {
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
	r.streamGen++
	r.transition(turnModelStreaming, turnOutcomeNone)
	return ctx, r.streamGen, nil
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

func (r *turnRuntime) complete(outcome turnOutcome) turnTransition {
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
