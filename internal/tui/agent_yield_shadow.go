package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/decision"
	"github.com/patrikcze/llmtui/internal/untrusted"
)

// Phase 7 is an explicitly opt-in observation of the existing yield boundary.
// It reuses the already wired decision runtime, but its result is never read
// by the agent controller. The sample stream is session-local and bounded; it
// is not persisted as a checkpoint, memory entry, or transcript.
const (
	agentYieldShadowSchemaVersion = 1
	maxAgentYieldShadowEntries    = 16
	maxAgentYieldShadowSamples    = 256
	maxAgentYieldShadowCriteria   = 20
	maxAgentYieldShadowTools      = 20
	maxAgentYieldShadowResources  = 20
)

const (
	agentYieldShadowOutcomeAvailable = "available"
	agentYieldShadowOutcomeMissing   = "missing"
	agentYieldShadowOutcomeCensored  = "censored"
	agentYieldShadowOutcomeLate      = "late"
)

// agentYieldShadowState is the only state admitted to the optional Phase 7
// question. Free-text objective is bounded; all other fields are IDs, closed
// vocabulary, counters, or version metadata. No body, hidden reasoning,
// arbitrary tool arguments, or MCP description enters this projection.
type agentYieldShadowState struct {
	SchemaVersion    int                            `json:"schema_version"`
	Objective        string                         `json:"objective"`
	Cycle            int                            `json:"cycle"`
	YieldSequence    int                            `json:"yield_sequence"`
	ProgressCategory string                         `json:"progress_category"`
	BaselineRoute    string                         `json:"baseline_route"`
	ProposedAction   string                         `json:"proposed_action"`
	Criteria         []agentDecisionShadowCriterion `json:"criteria,omitempty"`
	ToolOutcomeCodes []string                       `json:"tool_outcome_codes,omitempty"`
	ResourceRefs     []agentYieldShadowResource     `json:"resource_refs,omitempty"`
}

type agentYieldShadowResource struct {
	ResourceKey string `json:"resource_key"`
	Digest      string `json:"digest"`
	SizeBytes   int64  `json:"size_bytes"`
	Complete    bool   `json:"complete"`
}

// agentYieldShadowQuestions deliberately asks for an observation, not an
// instruction. The vocabulary is diagnostic and is not an authorization
// surface; the deterministic controller remains the only policy authority.
var agentYieldShadowQuestions = map[string]decision.Question{
	"yield_action": {
		Type:         decision.QuestionChoice,
		Instructions: "Which bounded action would best fit this observable agent yield?",
		Criteria: map[string]string{
			"finish":   "The pinned objective appears complete.",
			"continue": "More bounded executor work is needed.",
			"verify":   "The yield is ambiguous and needs semantic verification.",
			"ask_user": "Explicit user input or permission is required.",
			"blocked":  "Available evidence or capabilities cannot make progress.",
		},
	},
}

type agentYieldShadowMsg struct {
	runID    string
	cycle    int
	sequence int
	model    string
	result   decision.Result
	err      error
	elapsed  time.Duration
}

type agentYieldShadowCorrelation struct {
	runID    string
	cycle    int
	sequence int
	state    agentYieldShadowState
	model    string

	predictionArrived bool
	advice            string
	predictionErr     string
	modelRevision     string
	predictionLatency time.Duration

	actualArrived bool
	actualAction  string
}

// agentYieldShadowSample is versioned so later analysis can reject samples
// whose state/question contract changed. It is intentionally not an AgentRun
// field: shadow data must never become restart authority.
type agentYieldShadowSample struct {
	SchemaVersion  int
	RunID          string
	Cycle          int
	YieldSequence  int
	BaselineRoute  string
	ProposedAction string
	Advice         string
	ActualAction   string
	Outcome        string
	ErrorCode      string
	Model          string
	ModelRevision  string
	Latency        time.Duration
}

type agentYieldShadowMetrics struct {
	Total     int
	Available int
	Missing   int
	Censored  int
	Late      int
	Duplicate int
}

func agentYieldShadowKey(runID string, cycle int) string {
	return fmt.Sprintf("%s:%d", runID, cycle)
}

func (m *Model) dispatchAgentYieldShadow(run *agent.AgentRun, execution agent.ExecutionResult, plan agentVerificationPlan) tea.Cmd {
	if run == nil || m.agentLoop == nil || m.decisionShadow == nil || m.decisionShadow.service == nil ||
		!m.cfg.DecisionEngine.Enabled || !m.cfg.DecisionEngine.YieldShadow {
		return nil
	}
	m.agentLoop.yieldShadowGen++
	sequence := m.agentLoop.yieldShadowGen
	state := m.buildAgentYieldShadowState(run, execution, plan, sequence)
	key := agentYieldShadowKey(run.ID, run.Cycle)
	if m.yieldShadowCorrelations == nil {
		m.yieldShadowCorrelations = make(map[string]*agentYieldShadowCorrelation)
	}
	if len(m.yieldShadowCorrelations) >= maxAgentYieldShadowEntries {
		m.evictOldestAgentYieldShadow()
	}
	entry := &agentYieldShadowCorrelation{
		runID: run.ID, cycle: run.Cycle, sequence: sequence, state: state,
		model: m.decisionShadow.model,
	}
	m.yieldShadowCorrelations[key] = entry
	m.yieldShadowMetrics.Total++

	runID, cycle, model := run.ID, run.Cycle, m.decisionShadow.model
	svc := m.decisionShadow.service
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), decisionShadowTimeout)
		defer cancel()
		started := time.Now()
		result, err := svc.Predict(ctx, state, agentYieldShadowQuestions, decision.PredictOptions{Model: model})
		return agentYieldShadowMsg{
			runID: runID, cycle: cycle, sequence: sequence, model: model,
			result: result, err: err, elapsed: time.Since(started),
		}
	}
}

func (m *Model) buildAgentYieldShadowState(run *agent.AgentRun, execution agent.ExecutionResult, plan agentVerificationPlan, sequence int) agentYieldShadowState {
	state := agentYieldShadowState{
		SchemaVersion: agentYieldShadowSchemaVersion,
		Objective:     untrusted.Frame("agent_yield_objective", "controller", truncateAgentText(run.Objective, decisionShadowMaxObjectiveBytes)),
		Cycle:         run.Cycle, YieldSequence: sequence,
		BaselineRoute:    yieldShadowRoute(plan),
		ProposedAction:   yieldShadowProposedAction(run, plan),
		ProgressCategory: yieldShadowProgressCategory(execution, plan),
	}
	for _, criterion := range run.Criteria {
		if len(state.Criteria) >= maxAgentYieldShadowCriteria {
			break
		}
		state.Criteria = append(state.Criteria, agentDecisionShadowCriterion{
			ID: criterion.ID, Kind: string(criterion.Kind), Status: string(criterion.Status),
		})
	}
	for _, call := range execution.ToolCalls {
		if len(state.ToolOutcomeCodes) >= maxAgentYieldShadowTools {
			break
		}
		state.ToolOutcomeCodes = append(state.ToolOutcomeCodes, yieldShadowToolOutcome(call))
	}
	seenResources := make(map[string]bool)
	for _, call := range execution.ToolCalls {
		if len(state.ResourceRefs) >= maxAgentYieldShadowResources || call.Name != "read_file" || strings.TrimSpace(call.Detail) == "" {
			continue
		}
		key := "path:" + fileVersionKey(call.Detail)
		if seenResources[key] {
			continue
		}
		observed, ok := m.observedFileVersions[key]
		if !ok {
			continue
		}
		seenResources[key] = true
		state.ResourceRefs = append(state.ResourceRefs, agentYieldShadowResource{
			ResourceKey: untrusted.Frame("agent_yield_resource", call.Name, truncateAgentText(call.Detail, 160)),
			Digest:      truncateAgentText(observed.Version.Digest, 128), SizeBytes: observed.Version.SizeBytes,
			Complete: observed.Version.Complete,
		})
	}
	return state
}

func yieldShadowRoute(plan agentVerificationPlan) string {
	if plan.Route == agentVerificationPlanSemantic {
		return "semantic"
	}
	return "deterministic"
}

func yieldShadowProposedAction(run *agent.AgentRun, plan agentVerificationPlan) string {
	if plan.Route == agentVerificationPlanSemantic {
		return "verify"
	}
	result := plan.Result
	if result.NeedsUserInput {
		return "ask_user"
	}
	switch result.Verdict {
	case agent.VerificationPassed:
		if run.HasCriteria() && len(run.UnresolvedCriteria()) > 0 {
			return "continue"
		}
		return "finish"
	case agent.VerificationFailed, agent.VerificationInconclusive:
		if result.Retryable {
			return "continue"
		}
		return "blocked"
	case agent.VerificationBlocked:
		return "blocked"
	default:
		return "verify"
	}
}

func yieldShadowProgressCategory(execution agent.ExecutionResult, plan agentVerificationPlan) string {
	if execution.NeedsUserInput {
		return "human_barrier"
	}
	if len(execution.Errors) > 0 {
		return "error"
	}
	if plan.Route == agentVerificationPlanSemantic {
		return "ambiguous"
	}
	if len(execution.ToolCalls) == 0 {
		return "text_only_yield"
	}
	if execution.NewEvidence {
		return "new_evidence"
	}
	return "bounded_tool_round"
}

func yieldShadowToolOutcome(call agent.ToolCallRecord) string {
	switch call.Status {
	case agent.ActionDenied:
		return "denied"
	case agent.ActionBlocked:
		return "blocked"
	case agent.ActionUnknown:
		return "unknown"
	case agent.ActionExecuted:
		if call.Succeeded {
			return "executed_success"
		}
		return "executed_failure"
	default:
		if call.Succeeded {
			return "legacy_success"
		}
		return "legacy_failure"
	}
}

func (m *Model) handleAgentYieldShadow(msg agentYieldShadowMsg) (tea.Model, tea.Cmd) {
	entry, ok := m.yieldShadowCorrelations[agentYieldShadowKey(msg.runID, msg.cycle)]
	if !ok || entry.sequence != msg.sequence {
		return m, nil
	}
	if entry.predictionArrived {
		m.yieldShadowMetrics.Duplicate++
		return m, nil
	}
	entry.predictionArrived = true
	entry.predictionLatency = msg.elapsed
	entry.model = msg.model
	if msg.err != nil {
		entry.predictionErr = yieldShadowErrorCode(msg.err)
	} else if answer, ok := msg.result.Answers["yield_action"]; !ok || strings.TrimSpace(answer.Choice) == "" {
		entry.predictionErr = "invalid_answer"
	} else {
		entry.advice = answer.Choice
		entry.modelRevision = msg.result.Routing.Revision
	}
	m.finalizeAgentYieldShadow(entry)
	return m, nil
}

func yieldShadowErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "unavailable"
}

func (m *Model) recordAgentYieldShadowActual(runID string, cycle int, action string) {
	entry, ok := m.yieldShadowCorrelations[agentYieldShadowKey(runID, cycle)]
	if !ok {
		return
	}
	if entry.actualArrived {
		m.yieldShadowMetrics.Duplicate++
		return
	}
	entry.actualArrived = true
	entry.actualAction = action
	m.finalizeAgentYieldShadow(entry)
}

func (m *Model) finalizeAgentYieldShadow(entry *agentYieldShadowCorrelation) {
	if !entry.predictionArrived || !entry.actualArrived {
		return
	}
	delete(m.yieldShadowCorrelations, agentYieldShadowKey(entry.runID, entry.cycle))
	live := m.agentLoop != nil && m.agentLoop.run != nil && m.agentLoop.run.ID == entry.runID && m.agentLoop.run.Cycle == entry.cycle
	outcome := agentYieldShadowOutcomeAvailable
	if entry.predictionErr != "" {
		outcome = agentYieldShadowOutcomeMissing
		m.yieldShadowMetrics.Missing++
	} else if !live {
		outcome = agentYieldShadowOutcomeLate
		m.yieldShadowMetrics.Late++
	} else {
		m.yieldShadowMetrics.Available++
	}
	m.appendAgentYieldShadowSample(agentYieldShadowSample{
		SchemaVersion: agentYieldShadowSchemaVersion, RunID: entry.runID, Cycle: entry.cycle,
		YieldSequence: entry.sequence, BaselineRoute: entry.state.BaselineRoute,
		ProposedAction: entry.state.ProposedAction, Advice: entry.advice,
		ActualAction: entry.actualAction, Outcome: outcome, ErrorCode: entry.predictionErr,
		Model: entry.model, ModelRevision: entry.modelRevision, Latency: entry.predictionLatency,
	})
}

func (m *Model) censorPendingAgentYieldShadows() {
	for key, entry := range m.yieldShadowCorrelations {
		if entry.predictionArrived && entry.actualArrived {
			delete(m.yieldShadowCorrelations, key)
			continue
		}
		delete(m.yieldShadowCorrelations, key)
		m.yieldShadowMetrics.Censored++
		m.appendAgentYieldShadowSample(agentYieldShadowSample{
			SchemaVersion: agentYieldShadowSchemaVersion, RunID: entry.runID, Cycle: entry.cycle,
			YieldSequence: entry.sequence, BaselineRoute: entry.state.BaselineRoute,
			ProposedAction: entry.state.ProposedAction, Outcome: agentYieldShadowOutcomeCensored,
			Model: entry.model,
		})
	}
}

func (m *Model) evictOldestAgentYieldShadow() {
	var oldestKey string
	oldestSequence := 0
	for key, entry := range m.yieldShadowCorrelations {
		if oldestKey == "" || entry.sequence < oldestSequence {
			oldestKey, oldestSequence = key, entry.sequence
		}
	}
	if oldestKey == "" {
		return
	}
	entry := m.yieldShadowCorrelations[oldestKey]
	delete(m.yieldShadowCorrelations, oldestKey)
	m.yieldShadowMetrics.Missing++
	m.appendAgentYieldShadowSample(agentYieldShadowSample{
		SchemaVersion: agentYieldShadowSchemaVersion, RunID: entry.runID, Cycle: entry.cycle,
		YieldSequence: entry.sequence, BaselineRoute: entry.state.BaselineRoute,
		ProposedAction: entry.state.ProposedAction, Outcome: agentYieldShadowOutcomeMissing,
		ErrorCode: "correlation_evicted", Model: entry.model,
	})
}

func (m *Model) appendAgentYieldShadowSample(sample agentYieldShadowSample) {
	m.yieldShadowLast = sample
	m.yieldShadowSamples = append(m.yieldShadowSamples, sample)
	if len(m.yieldShadowSamples) > maxAgentYieldShadowSamples {
		m.yieldShadowSamples = m.yieldShadowSamples[len(m.yieldShadowSamples)-maxAgentYieldShadowSamples:]
	}
}
