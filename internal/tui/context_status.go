package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/contextmgr"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

const contextPreviewLimit = 480

// agentScopedSummary is an in-memory prompt projection produced for a
// verified run. It is diagnostics only; AgentRun remains authoritative for
// all agent control state.
type agentScopedSummary struct {
	RunID      string
	Cycle      int
	Summary    string
	RecordedAt time.Time
}

// contextStatusSnapshot is a point-in-time, value-only view of context
// planning and lifecycle ownership. It intentionally retains no message,
// tool-result, memory, or reasoning slices from Model.
type contextStatusSnapshot struct {
	CapturedAt time.Time

	Scope    string
	Strategy string

	WindowTokens          int
	WindowSource          string
	EstimatedPromptTokens int
	ResponseReserveTokens int
	AvailableTokens       int
	UsagePercent          float64

	SystemTokens     int
	MessageTokens    int
	ToolSchemaTokens int
	SummaryTokens    int
	MemoryTokens     int
	RAGTokens        int
	FixedTokens      int

	OlderMessages  int
	RecentMessages int

	SummaryKind    string
	SummaryActive  bool
	SummaryPreview string

	CompressionRequired bool
	CompressionStrategy string

	Agent *agentContextStatus
	Turn  turnContextStatus

	MutationAllowed bool
	BlockedReason   string
}

type agentContextStatus struct {
	RunID                 string
	Objective             string
	Cycle                 int
	MaxCycles             int
	Stage                 string
	Status                string
	StartContextCaptured  bool
	StartSummaryTokens    int
	CapturedStartTurns    int
	VerifiedMemories      int
	CurrentCycleMessages  int
	CompletedRawProjected bool
	CriteriaTotal         int
	UnresolvedCriteria    int
	ScopedSummaryActive   bool
	LastCompression       string
	Verifier              verifierContextStatus
}

type verifierContextStatus struct {
	State         string
	Model         string
	Attempt       int
	MaxAttempts   int
	LastVerdict   string
	EvidenceItems int
}

type turnContextStatus struct {
	State               string
	Streaming           bool
	PendingToolCalls    int
	NativeBatchRunning  bool
	MCPBatchRunning     bool
	PendingApprovals    int
	PendingBudget       bool
	PendingAskUser      bool
	HarmonyContinuation bool
	ToolDepth           int
}

func (m *Model) contextSnapshot() contextStatusSnapshot {
	window, source := m.contextWindow()
	history, requestSummary, agentScoped := m.requestHistory()
	agentScope := agentScoped || m.contextAgentOwnsState()
	hasAgentRun := m.agentLoop != nil && m.agentLoop.run != nil
	specs := m.activeToolSpecs()
	systemTokens := provider.EstimateTokens(m.cfg.Chat.SystemPrompt)
	toolTokens := provider.EstimateToolSpecsTokens(specs)
	reserve := m.cfg.Context.ReserveResponseTokens
	decision := contextmgr.Decide(history, contextmgr.Params{
		Strategy:               m.ctxStrategy,
		ContextWindow:          window,
		ReserveResponseTokens:  reserve,
		SummarizeAfterMessages: m.cfg.Context.SummarizeAfterMessages,
		FixedTokens:            systemTokens + toolTokens,
	})
	keep := len(history)
	if decision.Compress {
		keep = m.cfg.Context.KeepLastMessages
	}
	older, recent := contextmgr.Split(history, keep)

	summary, summaryKind := m.contextSummaryForInspection(requestSummary, agentScope)
	summaryTokens := provider.EstimateTokens(summary)
	promptTokens := systemTokens + contextmgr.EstimateTokens(recent) + toolTokens + summaryTokens
	available := max(window-reserve, 0)
	usage := 0.0
	if available > 0 {
		usage = float64(promptTokens) / float64(available) * 100
	}

	blocked := m.contextMutationBlockedReason()
	snapshot := contextStatusSnapshot{
		CapturedAt:            time.Now(),
		Scope:                 "session",
		Strategy:              m.ctxStrategy,
		WindowTokens:          window,
		WindowSource:          source,
		EstimatedPromptTokens: promptTokens,
		ResponseReserveTokens: reserve,
		AvailableTokens:       available,
		UsagePercent:          usage,
		SystemTokens:          systemTokens,
		MessageTokens:         contextmgr.EstimateTokens(recent),
		ToolSchemaTokens:      toolTokens,
		SummaryTokens:         summaryTokens,
		FixedTokens:           systemTokens + toolTokens,
		OlderMessages:         len(older),
		RecentMessages:        len(recent),
		SummaryKind:           summaryKind,
		SummaryActive:         summary != "",
		SummaryPreview:        boundedContextPreview(summary),
		CompressionRequired:   decision.Compress,
		CompressionStrategy:   decision.Strategy,
		Turn:                  m.turnContextSnapshot(),
		MutationAllowed:       blocked == "",
		BlockedReason:         blocked,
	}
	if hasAgentRun {
		snapshot.Agent = m.agentContextSnapshot()
	}
	if agentScope {
		snapshot.Scope = "agent"
	}
	return snapshot
}

func (m *Model) contextSummaryForInspection(requestSummary string, agentScoped bool) (string, string) {
	if !agentScoped {
		if requestSummary == "" {
			return "", "none"
		}
		return requestSummary, "session summary"
	}
	if m.agentLoop != nil && m.agentLoop.run != nil {
		run := m.agentLoop.run
		if run.Cycle <= 1 && run.StartSummary != "" {
			return run.StartSummary, "agent start summary"
		}
		if m.agentContextSummary.RunID == run.ID && m.agentContextSummary.Cycle == run.Cycle && m.agentContextSummary.Summary != "" {
			return m.agentContextSummary.Summary, "agent-scoped request summary"
		}
	}
	return "", "none"
}

func (m *Model) agentContextSnapshot() *agentContextStatus {
	if m.agentLoop == nil || m.agentLoop.run == nil {
		return nil
	}
	run := m.agentLoop.run
	status := &agentContextStatus{
		RunID:                 run.ID,
		Objective:             boundedContextPreview(run.Objective),
		Cycle:                 run.Cycle,
		MaxCycles:             run.Limits.MaxCycles,
		Stage:                 string(run.Stage),
		Status:                string(run.Status),
		StartContextCaptured:  run.StartContextCaptured,
		StartSummaryTokens:    provider.EstimateTokens(run.StartSummary),
		CapturedStartTurns:    len(run.StartTurns),
		VerifiedMemories:      len(run.Memory),
		CurrentCycleMessages:  m.currentCycleMessageCount(),
		CompletedRawProjected: run.Cycle > 1 && len(m.agentLoop.cycleBoundaries) > 0,
		CriteriaTotal:         len(run.Criteria),
		UnresolvedCriteria:    len(run.UnresolvedCriteria()),
		ScopedSummaryActive:   m.agentContextSummary.RunID == run.ID && m.agentContextSummary.Cycle == run.Cycle && m.agentContextSummary.Summary != "",
		LastCompression:       contextCompressionDescription(run),
		Verifier:              m.verifierContextSnapshot(run),
	}
	return status
}

func (m *Model) currentCycleMessageCount() int {
	if m.agentLoop == nil {
		return 0
	}
	start := m.agentLoop.historyStart
	for _, boundary := range m.agentLoop.cycleBoundaries {
		if boundary > start && boundary <= len(m.session.Messages) {
			start = boundary
		}
	}
	if start < 0 || start > len(m.session.Messages) {
		return 0
	}
	return len(m.session.Messages) - start
}

func (m *Model) verifierContextSnapshot(run *agent.AgentRun) verifierContextStatus {
	status := verifierContextStatus{State: "idle", Model: m.effectiveVerifierModel(), MaxAttempts: max(m.cfg.Agent.Verifier.MaxAttempts, 1)}
	if cycle := run.LatestCycle(); cycle != nil && cycle.Verification != nil {
		status.State = "complete"
		status.LastVerdict = string(cycle.Verification.Verdict)
		status.EvidenceItems = len(cycle.Verification.Evidence)
	}
	if run.Status == agent.DecisionVerificationUnavailable {
		status.State = "unavailable"
	}
	if m.agentLoop.verifying {
		status.State = "running"
		if m.agentLoop.verifierAttempts > 0 {
			status.State = "retrying"
		}
		status.Attempt = min(m.agentLoop.verifierAttempts+1, status.MaxAttempts)
	}
	return status
}

func (m *Model) turnContextSnapshot() turnContextStatus {
	nativeRunning, mcpRunning := false, false
	if m.state == turnExecutingTools && m.activity != nil {
		for _, entry := range m.activity.entries {
			if entry.call.MCPServer == "" {
				nativeRunning = true
			} else {
				mcpRunning = true
			}
		}
	}
	status := turnContextStatus{
		State:               string(m.state),
		Streaming:           m.state == turnModelStreaming,
		PendingToolCalls:    len(m.pendingCalls),
		NativeBatchRunning:  nativeRunning,
		MCPBatchRunning:     mcpRunning,
		PendingApprovals:    len(m.pendingCalls),
		PendingBudget:       m.pendingBudget,
		PendingAskUser:      m.pendingAsk != nil,
		HarmonyContinuation: m.streamContinuation != nil,
		ToolDepth:           m.toolDepth,
	}
	return status
}

func contextCompressionDescription(run *agent.AgentRun) string {
	for index := len(run.Events) - 1; index >= 0; index-- {
		event := run.Events[index]
		if event.Kind == "context_compressed" {
			return fmt.Sprintf("cycle %d: %s", event.Cycle, event.Detail)
		}
	}
	return "none"
}

func boundedContextPreview(value string) string {
	value = strings.TrimSpace(terminaltext.Sanitize(value))
	preview, truncated := terminaltext.TruncateBytes(value, contextPreviewLimit)
	if truncated {
		return preview + "..."
	}
	return preview
}

func (m *Model) contextAgentOwnsState() bool {
	if m.agentLoop == nil || m.agentLoop.run == nil {
		return false
	}
	switch m.agentLoop.run.Status {
	case agent.DecisionRunning, agent.DecisionContinue, agent.DecisionRetry, agent.DecisionNeedsUserInput:
		return true
	}
	return false
}

// contextMutationBlockedReason reports why a command that would change prompt
// projections cannot run. Read-only context inspection does not use this
// guard, so it remains available throughout active work.
func (m *Model) contextMutationBlockedReason() string {
	if m.pendingBudget {
		return "a tool-round budget extension is pending"
	}
	if len(m.pendingCalls) > 0 {
		return "a tool approval is pending"
	}
	if m.pendingAsk != nil {
		return "an ask_user response is pending"
	}
	if m.agentVerifying() {
		if m.agentLoop.verifierAttempts > 0 {
			return "a verifier retry is active"
		}
		return "the verifier is active"
	}
	if m.thinking {
		return "a model response is streaming"
	}
	switch m.state {
	case turnModelStreaming:
		return "a model response is streaming"
	case turnExecutingTools:
		return "a tool batch is running"
	case turnProcessingResults:
		return "tool results are being processed"
	case turnWaitingApproval:
		return "a tool approval is pending"
	case turnWaitingUserInput:
		return "an ask_user response is pending"
	}
	if m.streamContinuation != nil {
		return "a Harmony tool continuation is incomplete"
	}
	if m.contextAgentOwnsState() {
		switch m.agentLoop.run.Status {
		case agent.DecisionRunning, agent.DecisionContinue, agent.DecisionRetry:
			return fmt.Sprintf("agent cycle %d is executing", m.agentLoop.run.Cycle)
		case agent.DecisionNeedsUserInput:
			return "the agent is waiting for required user input"
		}
	}
	return ""
}
