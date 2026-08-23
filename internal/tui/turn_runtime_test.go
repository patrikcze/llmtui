package tui

import (
	"context"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestTurnRuntimeTransitions(t *testing.T) {
	runtime := newTurnRuntime(3, t.TempDir())
	if runtime.state != turnIdle || runtime.busy() {
		t.Fatalf("initial runtime = %s busy=%t, want idle", runtime.state, runtime.busy())
	}

	_, streamGen, err := runtime.beginStream(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("begin stream: %v", err)
	}
	if runtime.state != turnModelStreaming || !runtime.busy() {
		t.Fatalf("after begin stream = %s busy=%t", runtime.state, runtime.busy())
	}
	if _, _, err := runtime.beginStream(context.Background(), time.Minute); err == nil {
		t.Fatal("second concurrent stream was accepted")
	}
	if runtime.acceptStreamEvent(streamGen - 1) {
		t.Fatal("stale stream generation was accepted")
	}
	events := make(chan provider.ChatEvent)
	if !runtime.adoptStream(streamGen, events) {
		t.Fatal("current stream generation was rejected")
	}
	stream, transition := runtime.finishStream(turnOutcomeToolContinuation)
	if stream != events || transition.State != turnProcessingResults {
		t.Fatalf("stream finish = (%v, %+v), want adopted stream and processing", stream == events, transition)
	}

	plan := newToolBatchPlan([]tools.Call{{ID: "call-1", Tool: tools.ToolListDir, Path: "."}})
	transition = runtime.waitForApproval(plan, false)
	if transition.Outcome != turnOutcomeNeedsApproval || runtime.busy() || len(runtime.pendingCalls) != 1 {
		t.Fatalf("approval transition = %+v busy=%t pending=%d", transition, runtime.busy(), len(runtime.pendingCalls))
	}
	runtime.clearPendingTools()
	_, batchGen, err := runtime.beginToolBatch(context.Background(), plan.runnableCalls())
	if err != nil {
		t.Fatalf("begin tool batch: %v", err)
	}
	if runtime.state != turnExecutingTools || !runtime.busy() || runtime.toolDepth != 1 {
		t.Fatalf("tool state = %s busy=%t depth=%d", runtime.state, runtime.busy(), runtime.toolDepth)
	}
	if runtime.acceptToolResults(batchGen - 1) {
		t.Fatal("stale tool generation was accepted")
	}
	if !runtime.acceptToolResults(batchGen) {
		t.Fatal("current tool generation was rejected")
	}
	if runtime.state != turnProcessingResults || runtime.busy() {
		t.Fatalf("settled tool state = %s busy=%t", runtime.state, runtime.busy())
	}
	transition = runtime.complete(turnOutcomeFinalAnswer)
	if transition.State != turnCompleted || transition.Outcome != turnOutcomeFinalAnswer {
		t.Fatalf("final transition = %+v", transition)
	}
}

func TestTurnRuntimeCancellationInvalidatesToolResults(t *testing.T) {
	runtime := newTurnRuntime(3, t.TempDir())
	_, gen, err := runtime.beginToolBatch(context.Background(), []tools.Call{{Tool: tools.ToolListDir}})
	if err != nil {
		t.Fatalf("begin tool batch: %v", err)
	}
	if !runtime.cancelToolBatch() {
		t.Fatal("active tool batch was not cancelled")
	}
	if runtime.state != turnCancelled || runtime.lastOutcome != turnOutcomeCancelled {
		t.Fatalf("cancel state = %s outcome=%s", runtime.state, runtime.lastOutcome)
	}
	if runtime.acceptToolResults(gen) {
		t.Fatal("cancelled batch result was accepted")
	}
}

func TestTurnRuntimeResetStartsFreshTurn(t *testing.T) {
	runtime := newTurnRuntime(1, t.TempDir())
	runtime.toolDepth = 4
	runtime.emptyContinuationRetried = true
	runtime.malformedToolCallRetried = true
	runtime.pendingCalls = []tools.Call{{Tool: tools.ToolWriteFile}}
	runtime.complete(turnOutcomeExecutionFailure)

	runtime.resetTurn(2, t.TempDir())
	if runtime.state != turnIdle || runtime.lastOutcome != turnOutcomeNone {
		t.Fatalf("reset transition = %s/%s", runtime.state, runtime.lastOutcome)
	}
	if runtime.toolDepth != 0 || runtime.emptyContinuationRetried || runtime.malformedToolCallRetried {
		t.Fatalf("retry state not reset: depth=%d empty=%t malformed=%t", runtime.toolDepth, runtime.emptyContinuationRetried, runtime.malformedToolCallRetried)
	}
	if len(runtime.pendingCalls) != 0 || runtime.progress == nil || runtime.progress.threshold != 2 {
		t.Fatalf("reset pending/progress = %d/%+v", len(runtime.pendingCalls), runtime.progress)
	}
}

func TestTurnRuntimeCycleResetPreservesRunProgress(t *testing.T) {
	runtime := newTurnRuntime(2, t.TempDir())
	call := tools.Call{Tool: tools.ToolReadFile, Path: "README.md"}
	runtime.progress.observeResults([]tools.Result{{Call: call, Output: "same"}})
	runtime.toolDepth = 3
	runtime.emptyContinuationRetried = true
	runtime.malformedToolCallRetried = true
	progress := runtime.progress

	runtime.resetCycle()

	if runtime.progress != progress {
		t.Fatal("cycle reset replaced the run-wide progress ledger")
	}
	if runtime.toolDepth != 0 || runtime.emptyContinuationRetried || runtime.malformedToolCallRetried {
		t.Fatalf("cycle state not reset: depth=%d empty=%t malformed=%t", runtime.toolDepth, runtime.emptyContinuationRetried, runtime.malformedToolCallRetried)
	}
}

func TestTurnRuntimeContinuationRetriesAreOneShot(t *testing.T) {
	runtime := newTurnRuntime(3, t.TempDir())
	if !runtime.claimEmptyContinuationRetry() || runtime.claimEmptyContinuationRetry() {
		t.Fatal("empty continuation retry was not bounded to one attempt")
	}
	runtime.clearEmptyContinuationRetry()
	if !runtime.claimEmptyContinuationRetry() {
		t.Fatal("empty continuation retry did not reset after usable output")
	}
	if !runtime.claimMalformedToolRetry() || runtime.claimMalformedToolRetry() {
		t.Fatal("malformed tool retry was not bounded to one attempt")
	}
}

func TestTurnRuntimeFinishReleasesStreamLifecycle(t *testing.T) {
	runtime := newTurnRuntime(3, t.TempDir())
	ctx, gen, err := runtime.beginStream(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("begin stream: %v", err)
	}
	events := make(chan provider.ChatEvent)
	if !runtime.adoptStream(gen, events) {
		t.Fatal("current stream was not adopted")
	}

	stream, transition := runtime.finishStream(turnOutcomeFinalAnswer)
	if stream != events || transition.State != turnCompleted {
		t.Fatalf("finish = stream:%t transition:%+v", stream == events, transition)
	}
	if runtime.stream != nil || runtime.streamCtx != nil || runtime.cancelStream != nil || runtime.idleWatchdog != nil {
		t.Fatalf("stream lifecycle retained after finish: stream=%v ctx=%v cancel=%v watchdog=%v",
			runtime.stream != nil, runtime.streamCtx != nil, runtime.cancelStream != nil, runtime.idleWatchdog != nil)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("finished stream context was not cancelled")
	}
	if runtime.acceptStreamEvent(gen) {
		t.Fatal("late event was accepted after terminal cleanup")
	}
}

func TestTurnRuntimeAdmissionTransitionTable(t *testing.T) {
	states := []turnState{
		turnIdle,
		turnModelStreaming,
		turnWaitingApproval,
		turnExecutingTools,
		turnProcessingResults,
		turnCompleted,
		turnCancelled,
		turnFailed,
	}
	streamRejected := map[turnState]bool{
		turnModelStreaming:  true,
		turnWaitingApproval: true,
		turnExecutingTools:  true,
	}
	toolRejected := map[turnState]bool{
		turnModelStreaming: true,
		turnExecutingTools: true,
	}

	for _, state := range states {
		t.Run("stream_from_"+string(state), func(t *testing.T) {
			runtime := newTurnRuntime(3, t.TempDir())
			runtime.state = state
			_, _, err := runtime.beginStream(context.Background(), time.Minute)
			if got := err != nil; got != streamRejected[state] {
				t.Fatalf("beginStream from %s rejected=%t, want %t (err=%v)", state, got, streamRejected[state], err)
			}
			runtime.stopStream()
		})
		t.Run("tools_from_"+string(state), func(t *testing.T) {
			runtime := newTurnRuntime(3, t.TempDir())
			runtime.state = state
			_, _, err := runtime.beginToolBatch(context.Background(), []tools.Call{{Tool: tools.ToolListDir}})
			if got := err != nil; got != toolRejected[state] {
				t.Fatalf("beginToolBatch from %s rejected=%t, want %t (err=%v)", state, got, toolRejected[state], err)
			}
			runtime.cancelToolBatch()
		})
	}
}

func TestTurnRuntimeOutcomeTransitionTable(t *testing.T) {
	cases := []struct {
		outcome turnOutcome
		state   turnState
	}{
		{turnOutcomeFinalAnswer, turnCompleted},
		{turnOutcomeNeedsApproval, turnWaitingApproval},
		{turnOutcomeToolContinuation, turnProcessingResults},
		{turnOutcomeExecutionFailure, turnFailed},
		{turnOutcomeCancelled, turnCancelled},
	}
	for _, tc := range cases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			runtime := newTurnRuntime(3, t.TempDir())
			transition := runtime.complete(tc.outcome)
			if transition.State != tc.state || transition.Outcome != tc.outcome {
				t.Fatalf("complete(%s) = %+v, want state %s", tc.outcome, transition, tc.state)
			}
		})
	}
}

func TestTurnRuntimeReplacementRejectsStaleEvents(t *testing.T) {
	runtime := newTurnRuntime(3, t.TempDir())
	_, streamA, err := runtime.beginStream(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("begin stream A: %v", err)
	}
	runtime.finishStream(turnOutcomeCancelled)
	_, streamB, err := runtime.beginStream(context.Background(), time.Minute)
	if err != nil {
		t.Fatalf("begin stream B: %v", err)
	}
	if runtime.acceptStreamEvent(streamA) {
		t.Fatal("stream A event was accepted after stream B started")
	}
	if !runtime.acceptStreamEvent(streamB) {
		t.Fatal("stream B event was rejected")
	}
	runtime.finishStream(turnOutcomeFinalAnswer)

	_, batchA, err := runtime.beginToolBatch(context.Background(), []tools.Call{{ID: "a", Tool: tools.ToolListDir}})
	if err != nil {
		t.Fatalf("begin batch A: %v", err)
	}
	runtime.cancelToolBatch()
	_, batchB, err := runtime.beginToolBatch(context.Background(), []tools.Call{{ID: "b", Tool: tools.ToolListDir}})
	if err != nil {
		t.Fatalf("begin batch B: %v", err)
	}
	if runtime.acceptToolResults(batchA) {
		t.Fatal("batch A result was accepted after batch B started")
	}
	if !runtime.acceptToolResults(batchB) {
		t.Fatal("batch B result was rejected")
	}
}

func TestTurnRuntimeOrdinaryChatCharacterization(t *testing.T) {
	m := newTestModel(t)
	m.prov = &pacedProvider{chunks: 2}

	runStream(t, m, m.dispatch("hello", nil))

	if m.turnRuntime.state != turnCompleted || m.turnRuntime.lastOutcome != turnOutcomeFinalAnswer {
		t.Fatalf("ordinary chat terminal state = %s/%s", m.turnRuntime.state, m.turnRuntime.lastOutcome)
	}
	if m.busy() || m.stream != nil || m.streamCtx != nil || m.cancelStream != nil || m.idleWatchdog != nil {
		t.Fatal("ordinary chat retained active execution state after its final answer")
	}
	if len(m.session.Messages) != 3 {
		t.Fatalf("ordinary chat messages = %d, want system, user, and assistant", len(m.session.Messages))
	}
	if m.session.Messages[0].Role != provider.RoleSystem {
		t.Fatalf("system message = %+v", m.session.Messages[0])
	}
	if m.session.Messages[1].Role != provider.RoleUser || m.session.Messages[1].Content != "hello" {
		t.Fatalf("user message = %+v", m.session.Messages[1])
	}
	if m.session.Messages[2].Role != provider.RoleAssistant || m.session.Messages[2].Content != "chunk-0 chunk-1 " {
		t.Fatalf("assistant message = %+v", m.session.Messages[2])
	}
}
