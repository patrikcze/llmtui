package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
)

// A tool batch runs asynchronously, so by the time the user cancels it some
// of its calls may already have written a file or run a command. Dropping
// the batch's results as stale (the behavior for a superseded batch) would
// leave that completed mutation recorded nowhere the model or the run can
// see. A user cancel therefore keeps the results: runPlannedToolBatch stops
// starting calls once its context is cancelled and gives every unstarted
// call a synthetic not-executed result, and when the batch's message
// arrives adoptCancelledToolResults appends the call/result pairs to the
// session and, for an agent run, records them in the cycle's partial
// execution before the run is marked cancelled.

// userCancelToolBatch cancels the running tool batch on the user's behalf
// (Ctrl+C, Esc). An agent run in its executor stage is not terminated yet:
// it stays active until the batch's results arrive so they can be recorded
// in the abandoned cycle's partial execution, and pendingBatchCancel holds
// the reason until then. Every other case ends the run at once, as before.
// ok reports whether a batch was running.
func (m *Model) userCancelToolBatch(reason string) (save tea.Cmd, ok bool) {
	if !m.cancelToolBatchByUser() {
		return nil, false
	}
	m.clearProviderContinuations()
	m.relayout()
	if m.agentRunActive() && m.agentLoop.run.Stage == agent.StageExecutor {
		m.agentLoop.pendingBatchCancel = reason
		return nil, true
	}
	m.cancelVerifiedRun(reason)
	m.endAgentRun()
	return m.persistAgentRun(), true
}

// adoptCancelledToolResults keeps the results of a batch the user
// cancelled: one result per accepted call — the real result for every call
// that ran, a synthetic not-executed result for every call that never
// started — so history stays call/result-paired. It never continues the
// turn.
func (m *Model) adoptCancelledToolResults(msg mcpToolResultsMsg) tea.Cmd {
	m.relayout()
	ok, failed := countToolOutcomes(msg.results)
	m.toolOK += ok
	m.toolErr += failed
	// A not-started slot carries agent.ActionBlocked, so it is neither
	// evidence nor tool-budget usage (see markNotStartedResults).
	m.recordAgentToolResultsCount(msg.results, false, msg.statuses)
	m.appendTerminalToolResults(msg.results)
	notStarted := 0
	for _, result := range msg.results {
		if isNotStartedResult(result) {
			notStarted++
		}
	}
	m.notice = fmt.Sprintf("tool batch cancelled — kept %d completed result(s), %d call(s) not started",
		len(msg.results)-notStarted, notStarted)
	save, _ := m.finalizeCancelledToolBatch()
	m.refreshViewport()
	return save
}

// finalizeCancelledToolBatch ends the agent run whose batch the user
// cancelled, committing the cycle's execution so far as a partial record.
// It runs when the batch's results arrive, and also when something
// supersedes that batch first (a new submission, /agent cancel), in which
// case the results are later dropped as stale. ok reports whether a pending
// cancellation ended a run; save persists it.
func (m *Model) finalizeCancelledToolBatch() (save tea.Cmd, ok bool) {
	if m.agentLoop == nil || m.agentLoop.pendingBatchCancel == "" {
		return nil, false
	}
	reason := m.agentLoop.pendingBatchCancel
	m.agentLoop.pendingBatchCancel = ""
	if !m.agentRunActive() {
		return nil, false
	}
	m.terminateAgentRun(agent.DecisionCancelled, reason)
	m.cancelVerifiedRun(reason)
	m.endAgentRun()
	return m.persistAgentRun(), true
}
