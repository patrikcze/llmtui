package tui

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
)

// This file wires internal/agent's pure EvaluateYield policy
// (docs/architecture/decisions/0013-agent-execution-yield-policy.md) into
// the TUI's clean, no-tool completion boundary — Phase 2 of the
// agent-execution-harness plan. Model remains the only adapter that mutates
// UI/run state and schedules commands; the pure policy itself never does.
//
// Scope of this first wiring pass: at handleAgentYield's call sites (see
// app.go's handleStreamEvent and vision_observation.go's capture-completion
// callbacks), PendingApproval, PendingAsk, IncompleteProtocol, and
// PendingToolBatch are always false — every condition that could make them
// true already returns earlier in handleStreamEvent's own EventDone
// handling (native/fenced tool admission, the truncated-call guards, and
// the pendingAsk check), and a pending vision capture is already handled by
// the existing maybeStartVisionCapture/afterVisionCapture mechanism, which
// only calls into this adapter once no capture is outstanding. Representing
// those states explicitly in YieldInput is left for a later phase if a
// second call site ever needs it.

// maxAgentYieldObligations bounds how many actionable obligations
// buildAgentYieldDirective names in one request, independent of the byte
// cap below — matches §9's "at most 4 immediate obligations" guidance.
const maxAgentYieldObligations = 4

// handleAgentYield is the single decision point for a clean, no-tool
// assistant completion in an active agent run. When agent.yield.enabled is
// false it falls back to the pre-existing behavior (startAgentVerification)
// unchanged, so ordinary chat and flag-off agent runs are byte-for-byte the
// same as before this feature existed.
func (m *Model) handleAgentYield() tea.Cmd {
	if !m.agentRunActive() {
		return nil
	}
	if !m.cfg.Agent.Yield.Enabled {
		return m.startAgentVerification()
	}
	run := m.agentLoop.run
	cycle := run.LatestCycle()
	if cycle == nil {
		// Defensive: BeginCycle always appends a Cycle before executor work
		// starts, so this should be unreachable while the run is active.
		return m.startAgentVerification()
	}
	if cycle.Episode == nil {
		cycle.Episode = &agent.EpisodeCheckpoint{PolicyVersion: 1, EnabledAtStart: true}
	}
	checkpoint := cycle.Episode

	obligations := run.PendingExactReadObligations(m.agentLoop.execution)
	in, ids := m.buildAgentYieldInput(checkpoint, obligations)
	decision := agent.EvaluateYield(in)

	progressed := !equalStringSlices(checkpoint.UnresolvedCriterionIDs, ids)
	if progressed {
		checkpoint.NoProgressNudges = 0
	}
	checkpoint.UnresolvedCriterionIDs = ids
	checkpoint.LastYieldReason = decision.Reason
	checkpoint.Revision++
	m.agentLoop.yieldDirective = ""

	switch decision.Action {
	case agent.YieldContinue:
		if !progressed {
			checkpoint.NoProgressNudges++
		}
		checkpoint.ExecutorRequests++
		m.agentLoop.yieldDirective = buildAgentYieldDirective(decision, obligations)
		return m.continueAgentEpisode(decision)
	case agent.YieldVerify:
		return m.startAgentVerification()
	default:
		return m.terminateAgentYield(decision)
	}
}

// buildAgentYieldInput assembles a YieldInput from current run/model state,
// plus the criterion IDs behind every pending exact-read obligation found
// (actionable or not) — the caller uses those IDs for checkpoint progress
// tracking regardless of which way the decision goes.
//
// ContextFeasible is optimistically true: continueChat already runs
// prepareRequest and fails safely through its own existing error path if
// preparation is not feasible, so this function does not duplicate that
// check for Phase 2.
func (m *Model) buildAgentYieldInput(checkpoint *agent.EpisodeCheckpoint, obligations []agent.ExactReadObligation) (agent.YieldInput, []string) {
	in := agent.YieldInput{ContextFeasible: true}

	if m.agentLoop.ctx != nil && m.agentLoop.ctx.Err() != nil {
		in.Cancelled = true
	}

	if hardExceeded, hardReason := m.agentHardBudgetExceeded(0); hardExceeded {
		in.BudgetExhausted = true
		in.BudgetDetail = hardReason
	} else if limit := m.cfg.Agent.Yield.MaxEpisodeRequests; limit > 0 && checkpoint.ExecutorRequests >= limit {
		in.BudgetExhausted = true
		in.BudgetDetail = fmt.Sprintf("agent episode request budget exhausted (maximum %d)", limit)
	}

	// The TUI validates capability availability only, without acquiring
	// permissions or touching the filesystem — see ADR 0013 and the
	// Workspace Tool Safety Invariants in CLAUDE.md on why path resolution
	// stays confined to internal/tools' own confined resolver rather than
	// being duplicated here.
	actionable := m.toolsOn && m.toolRunner != nil
	ids := make([]string, 0, len(obligations))
	mech := make([]agent.MechanicalObligation, 0, len(obligations))
	for _, ob := range obligations {
		ids = append(ids, ob.CriterionID)
		mech = append(mech, agent.MechanicalObligation{CriterionID: ob.CriterionID, Actionable: actionable})
	}
	in.MechanicalObligations = mech
	in.NoProgressNudges = checkpoint.NoProgressNudges
	in.NudgeLimit = m.cfg.Agent.Yield.MaxNudgesWithoutProgress

	return in, ids
}

// continueAgentEpisode admits one more bounded model request in the same
// episode toward decision's actionable obligation(s). It must not call
// BeginCycle, startNextAgentCycle, resetTurn, resetCycle, renewToolBudget,
// or CompleteExecution — the current objective, evidence, execution
// receipts, run deadline, approvals, and progress ledger all survive
// unchanged; only m.agentLoop.yieldDirective (already set by the caller) is
// new.
func (m *Model) continueAgentEpisode(decision agent.YieldDecision) tea.Cmd {
	run := m.agentLoop.run
	m.notice = fmt.Sprintf("agent %s · cycle %d/%d · continuing — %s",
		shortRunID(run.ID), run.Cycle, run.Limits.MaxCycles, decision.Reason)
	m.refreshViewport()
	return m.continueChat()
}

// terminateAgentYield ends the run for a terminal, non-Continue, non-Verify
// yield decision (Cancelled, BudgetExhausted, or Blocked — NeedsUser and
// Failed do not arise at this Phase 2 call site; see this file's top
// comment for why). It mirrors terminateAgentModelRequestBudget's shape
// exactly and reuses the existing Decision vocabulary via
// YieldDecision.TerminalDecision rather than inventing a new stop path.
func (m *Model) terminateAgentYield(decision agent.YieldDecision) tea.Cmd {
	run := m.agentLoop.run
	outcome, ok := decision.TerminalDecision()
	if !ok {
		// Never silently treat unknown/malformed policy output as done —
		// fail closed exactly like other invalid-state paths in this
		// package (see failVerifiedRun).
		outcome = agent.DecisionFailed
	}
	reason := yieldTerminationReason(decision)
	_ = run.Terminate(outcome, reason, time.Now())
	m.notice = fmt.Sprintf("agent %s · %s", shortRunID(run.ID), reason)
	m.endAgentRun()
	m.refreshViewport()
	return m.persistAgentRun()
}

func yieldTerminationReason(decision agent.YieldDecision) string {
	if decision.Note != "" {
		return decision.Note
	}
	switch decision.Reason {
	case agent.ReasonCancelled:
		return "agent run was cancelled"
	case agent.ReasonNoProgressStalled:
		return "no relevant progress after repeated controller continuations"
	case agent.ReasonSafetyBlocked:
		return "execution encountered a safety constraint"
	case agent.ReasonContextInfeasible:
		return "context preparation is not feasible for the pending obligation"
	default:
		return fmt.Sprintf("agent yield stopped (%s)", decision.Reason)
	}
}

// buildAgentYieldDirective renders decision's actionable obligations as a
// small, clearly marked execution-state subsection for the bounded
// AgentDirective slot (internal/prompt/compose.go's authority-limiting
// preamble already wraps whatever agentDirective returns). It never copies
// raw tool output into the template: only controller-owned criterion IDs
// and each criterion's own already-pinned, bounded target text are quoted
// as data. Bounded to maxAgentYieldObligations entries and, via the caller
// truncating agentDirective's whole output, to maxAgentDirectiveBytes.
func buildAgentYieldDirective(decision agent.YieldDecision, obligations []agent.ExactReadObligation) string {
	if decision.Action != agent.YieldContinue || len(decision.CriterionIDs) == 0 {
		return ""
	}
	targets := make(map[string]string, len(obligations))
	for _, ob := range obligations {
		targets[ob.CriterionID] = ob.Target
	}
	ids := decision.CriterionIDs
	if len(ids) > maxAgentYieldObligations {
		ids = ids[:maxAgentYieldObligations]
	}
	var b []byte
	b = append(b, "Runtime execution state: yielded with missing required evidence.\n"...)
	for _, id := range ids {
		target, ok := targets[id]
		if !ok {
			continue
		}
		b = append(b, fmt.Sprintf("Criterion %s still lacks the requested file coverage for %q. Use the offered read_file tool to obtain it.\n", id, target)...)
	}
	b = append(b, "Existing permissions and user constraints still apply.\n"...)
	return string(b)
}

// resumeAfterVisionCapture is the single completion point for a vision
// capture requested during agent execution (m.afterVisionCapture), win or
// lose. It replaces a direct startAgentVerification call so a capture that
// completes while yield continuation is enabled re-enters the same decision
// boundary handleStreamEvent uses, instead of always jumping straight to
// verification. Capture itself only ever runs once per yield decision —
// maybeStartVisionCapture is not invoked again from here.
func (m *Model) resumeAfterVisionCapture() tea.Cmd {
	return m.handleAgentYield()
}

// equalStringSlices reports whether a and b contain the same criterion IDs
// in the same order. PendingExactReadObligations iterates run.Criteria in
// its stable, pinned-once order, so order-sensitive comparison is
// sufficient here and cheaper than sorting on every yield.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
