package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
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
// execution before the run is marked cancelled. The next /agent run then
// carries that exchange natively in its first cycle, plus a text receipt in
// its persisted start turns (see cancelledExchange).

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
	m.rememberCancelledExchange(msg.results)
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

// cancelledExchange remembers the most recent user-cancelled batch so the
// next /agent run can see it. A new run otherwise starts from the text of
// earlier turns only (snapshotAgentStartTurns drops every tool call and
// result), which hid a mutation that completed before the cancel.
type cancelledExchange struct {
	// start is the session index of the assistant message that made the
	// calls; callIDs identify it, so a cleared or rewritten session is
	// detected instead of trusted.
	start   int
	callIDs []string
	// receipt is a short text record of each call's outcome. It is kept in
	// the next run's persisted start turns, so it survives a resume, where
	// the native exchange (process-local) is not carried.
	receipt string
}

// maxCancelledReceiptDetail bounds one call's detail in the receipt.
const maxCancelledReceiptDetail = 160

// rememberCancelledExchange records the batch whose results were just
// appended, for the next run. results are in call order.
func (m *Model) rememberCancelledExchange(results []tools.Result) {
	m.agentLoop.lastCancelled = nil
	if len(results) == 0 {
		return
	}
	start := -1
	for i := len(m.session.Messages) - 1; i >= 0; i-- {
		msg := m.session.Messages[i]
		if msg.Role == provider.RoleAssistant && (len(msg.ToolCalls) > 0 || len(tools.Parse(msg.Content)) > 0) {
			start = i
			break
		}
	}
	if start < 0 {
		return
	}
	var ids []string
	var b strings.Builder
	b.WriteString(cancelledReceiptPrefix)
	for _, result := range results {
		if result.Call.ID != "" {
			ids = append(ids, result.Call.ID)
		}
		outcome := "completed"
		switch {
		case isNotStartedResult(result):
			outcome = "not executed (the batch was cancelled before it started)"
		case result.Err != nil:
			outcome = "failed"
		case result.Meta.Effect == tools.EffectChanged:
			outcome = "completed, changed the workspace"
		}
		fmt.Fprintf(&b, "\n- %s", result.Call.Tool)
		if detail := strings.TrimSpace(toolCallDetail(result.Call)); detail != "" {
			fmt.Fprintf(&b, " %s", truncateAgentText(detail, maxCancelledReceiptDetail))
		}
		fmt.Fprintf(&b, ": %s", outcome)
	}
	m.agentLoop.lastCancelled = &cancelledExchange{start: start, callIDs: ids, receipt: b.String()}
}

const cancelledReceiptPrefix = "[llmtui receipt, not a user request] The user cancelled the previous tool batch. Outcome of each call:"

// takeCancelledExchange hands the remembered batch to a starting run and
// forgets it, so it is carried into exactly one run. It returns the native
// call/result messages and the receipt. When the session no longer holds
// the exchange (/clear, /history load), nothing is carried: the user chose a
// different conversation.
func (m *Model) takeCancelledExchange() (exchange []provider.Message, receipt string) {
	last := m.agentLoop.lastCancelled
	m.agentLoop.lastCancelled = nil
	if last == nil {
		return nil, ""
	}
	if last.start >= len(m.session.Messages) || !messageHasCallIDs(m.session.Messages[last.start], last.callIDs) {
		return nil, ""
	}
	end := last.start + 1
	for end < len(m.session.Messages) {
		msg := m.session.Messages[end]
		if msg.Role == provider.RoleTool || (msg.Role == provider.RoleUser && strings.HasPrefix(msg.Content, tools.ResultsPrefix)) {
			end++
			continue
		}
		break
	}
	return append([]provider.Message(nil), m.session.Messages[last.start:end]...), last.receipt
}

func messageHasCallIDs(msg provider.Message, ids []string) bool {
	if msg.Role != provider.RoleAssistant {
		return false
	}
	if len(ids) == 0 { // fenced protocol: calls carry no IDs
		return len(tools.Parse(msg.Content)) > 0
	}
	have := make(map[string]bool, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		have[call.ID] = true
	}
	for _, id := range ids {
		if !have[id] {
			return false
		}
	}
	return true
}
