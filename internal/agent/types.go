// Package agent defines the provider- and UI-independent state machine for
// bounded, verified agent runs.
package agent

import "time"

const (
	// SchemaVersion identifies the persisted AgentRun representation.
	SchemaVersion = 1
	// MaxEvents bounds diagnostic lifecycle history in one run.
	MaxEvents = 128
)

// Stage is one explicit phase in an agent cycle.
type Stage string

const (
	StageTrigger Stage = "trigger"
	// StageContract is the bounded, tool-free controller phase that pins
	// acceptance criteria before the first executor cycle may begin.
	StageContract    Stage = "contract"
	StageRulesLoad   Stage = "rules_load"
	StageExecutor    Stage = "executor"
	StageVerifier    Stage = "verifier"
	StageMemoryWrite Stage = "memory_write"
	StageStopCheck   Stage = "stop_check"
)

// Decision is the result of the stop check. Running is persisted while a
// cycle is active; all other values are explicit cycle-boundary outcomes.
type Decision string

const (
	DecisionRunning         Decision = "running"
	DecisionDone            Decision = "done"
	DecisionContinue        Decision = "continue"
	DecisionRetry           Decision = "retry"
	DecisionNeedsUserInput  Decision = "needs_user_input"
	DecisionParked          Decision = "parked"
	DecisionEscalated       Decision = "escalated"
	DecisionCancelled       Decision = "cancelled"
	DecisionFailed          Decision = "failed"
	DecisionBudgetExhausted Decision = "budget_exhausted"
	// DecisionNoProgress is a terminal outcome distinct from DecisionFailed:
	// the run stopped because a batch of tool calls repeated with no new
	// evidence twice in a row (see internal/tui's progressLedger and
	// docs/architecture/v1-agent-runtime.md §3), not because a verifier
	// rejected the work or an unexpected error occurred.
	DecisionNoProgress Decision = "no_progress"
	// DecisionVerificationUnavailable is a terminal outcome distinct from
	// DecisionFailed: the *verifier* could not produce a valid verdict after
	// its bounded retry budget (provider error, timeout, or repeated malformed
	// JSON) — not because the executor's work was rejected. The cycle's
	// ExecutionResult is preserved; no further executor cycle may be scheduled
	// from this outcome. See REL-001 in .claude/tasks/llmtui-audit-report.md.
	DecisionVerificationUnavailable Decision = "verification_unavailable"
)

// VerificationVerdict is the verifier's assessment of observable evidence.
type VerificationVerdict string

const (
	VerificationPassed       VerificationVerdict = "passed"
	VerificationFailed       VerificationVerdict = "failed"
	VerificationInconclusive VerificationVerdict = "inconclusive"
	VerificationBlocked      VerificationVerdict = "blocked"
)

// Limits are hard run budgets. Durations are persisted as nanoseconds by the
// standard JSON encoder and are documented as Go duration strings in config.
type Limits struct {
	MaxCycles           int           `json:"max_cycles"`
	MaxToolCalls        int           `json:"max_tool_calls"`
	MaxTokens           int           `json:"max_tokens"`
	MaxElapsed          time.Duration `json:"max_elapsed"`
	MaxRepeatedFailures int           `json:"max_repeated_failures"`
}

// DefaultLimits returns conservative bounds for an opt-in run.
func DefaultLimits() Limits {
	return Limits{
		MaxCycles:           8,
		MaxToolCalls:        32,
		MaxTokens:           100_000,
		MaxElapsed:          30 * time.Minute,
		MaxRepeatedFailures: 3,
	}
}

// Event is a concise observable lifecycle record. Detail must contain no
// prompt body, tool output, credential, or hidden reasoning.
type Event struct {
	Time   time.Time `json:"time"`
	RunID  string    `json:"run_id"`
	Cycle  int       `json:"cycle"`
	Stage  Stage     `json:"stage"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
}

// ToolCallRecord stores bounded outcome metadata, never full arguments or
// output, so run memory cannot become a second transcript or secret store.
// Detail is the one narrow, deliberate exception: a single dedup-relevant
// argument (a URL, file path, or search pattern) the executor needs to
// recognize "I already tried this exact thing" across cycles — see
// internal/tui/agent_loop.go's toolCallDetail for exactly what is and is
// not considered safe to echo back (notably: never a run_command's full
// command line, which can carry an inline secret).
type ToolCallRecord struct {
	ID        string    `json:"id,omitempty"`
	Name      string    `json:"name"`
	Detail    string    `json:"detail,omitempty"`
	Succeeded bool      `json:"succeeded"`
	ErrorKind ErrorKind `json:"error_kind,omitempty"`
	Summary   string    `json:"summary,omitempty"`
}

// TestResult is deterministic evidence reported by an executor adapter.
type TestResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary,omitempty"`
}

// ExecutionResult is the bounded, observable outcome of one cycle objective.
type ExecutionResult struct {
	Objective      string           `json:"objective"`
	Summary        string           `json:"summary,omitempty"`
	ToolCalls      []ToolCallRecord `json:"tool_calls,omitempty"`
	Artifacts      []string         `json:"artifacts,omitempty"`
	ChangedFiles   []string         `json:"changed_files,omitempty"`
	TestsRun       []TestResult     `json:"tests_run,omitempty"`
	Errors         []RunError       `json:"errors,omitempty"`
	NeedsUserInput bool             `json:"needs_user_input,omitempty"`
	SuggestedNext  string           `json:"suggested_next,omitempty"`
	NewEvidence    bool             `json:"new_evidence,omitempty"`
}

// VerificationResult is a structured evaluator result. Retry is permitted
// only when Retryable is true and progress evidence is present.
type VerificationResult struct {
	Verdict           VerificationVerdict `json:"verdict"`
	Summary           string              `json:"summary"`
	Evidence          []string            `json:"evidence,omitempty"`
	FailedCriteria    []string            `json:"failed_criteria,omitempty"`
	RemainingCriteria []string            `json:"remaining_criteria,omitempty"`
	RecommendedNext   string              `json:"recommended_next,omitempty"`
	Retryable         bool                `json:"retryable"`
	Confidence        float64             `json:"confidence"`
	NewEvidence       bool                `json:"new_evidence,omitempty"`
	StrategyChanged   bool                `json:"strategy_changed,omitempty"`
	TransientFailure  bool                `json:"transient_failure,omitempty"`
	// NeedsUserInput reports that this cycle's response is substantively a
	// question or choice addressed to the user — not real task progress —
	// and only the user can supply what the next cycle needs. Distinct from
	// ExecutionResult.NeedsUserInput (set when the user denies a tool
	// approval): this is the verifier's own semantic judgment of the
	// executor's text.
	NeedsUserInput bool `json:"needs_user_input,omitempty"`
	// UserOptions holds discrete choices the verifier extracted from the
	// executor's question, when NeedsUserInput is true and the question
	// presented a numbered/lettered list rather than being open-ended. Empty
	// (the common case, and always true for an open-ended question) means
	// the TUI falls back to a free-text prompt — this field is optional
	// enrichment, never a requirement for NeedsUserInput to function.
	UserOptions []string `json:"user_options,omitempty"`
	// ProposedCriteria decomposes the task into stable acceptance criteria.
	// Only the first semantic verification may propose them; PinCriteria
	// ignores proposals once a set is pinned.
	ProposedCriteria []string `json:"proposed_criteria,omitempty"`
	// CriteriaUpdates are per-ID status changes for the pinned criteria.
	CriteriaUpdates []CriterionUpdate `json:"criteria,omitempty"`
	// AtomicTask, when true and this is the run's establishing verification
	// (Input.EstablishCriteria was set), explicitly declares that the task is
	// a single indivisible check with no useful further decomposition, so an
	// empty ProposedCriteria list on this cycle is a deliberate verifier
	// judgment rather than an omission. See REL-002 in
	// .claude/tasks/llmtui-audit-report.md.
	AtomicTask bool `json:"atomic_task,omitempty"`
}

// MemoryEntry is concise cycle-to-cycle state. It deliberately excludes raw
// prompts, tool output, and model reasoning.
type MemoryEntry struct {
	Cycle            int                 `json:"cycle"`
	Objective        string              `json:"objective"`
	ExecutionSummary string              `json:"execution_summary,omitempty"`
	Verdict          VerificationVerdict `json:"verdict"`
	Verification     string              `json:"verification,omitempty"`
	// ToolCalls is one bounded line per call this cycle made ("name(detail)
	// succeeded|failed: kind" — see ToolCallRecord.Detail for what "detail"
	// is and is not). Without this, an executor whose next cycle no longer
	// sees the prior cycle's raw tool traffic (see requestHistory's
	// per-cycle projection) has no way to tell it already fetched a URL
	// that failed, or already got a URL that succeeded, and will blindly
	// repeat both — observed in practice as a run re-fetching an
	// already-successful page and re-hitting an already-failed one across
	// consecutive cycles.
	ToolCalls         []string  `json:"tool_calls,omitempty"`
	FailedCriteria    []string  `json:"failed_criteria,omitempty"`
	RemainingCriteria []string  `json:"remaining_criteria,omitempty"`
	Artifacts         []string  `json:"artifacts,omitempty"`
	RecommendedNext   string    `json:"recommended_next,omitempty"`
	RecordedAt        time.Time `json:"recorded_at"`
}

// Cycle records one objective and its observable execution and verification.
type Cycle struct {
	Number         int                 `json:"number"`
	Objective      string              `json:"objective"`
	ContextSources []string            `json:"context_sources,omitempty"`
	StartedAt      time.Time           `json:"started_at"`
	CompletedAt    time.Time           `json:"completed_at,omitempty"`
	Execution      *ExecutionResult    `json:"execution,omitempty"`
	Verification   *VerificationResult `json:"verification,omitempty"`
}

// ContextTurn is a bounded, provider-neutral prior final turn captured when a
// run starts. Tool protocol and controller messages are excluded by the TUI
// before persistence.
type ContextTurn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AgentRun is the serializable state of one bounded user request.
type AgentRun struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Cycle      int       `json:"cycle"`
	Stage      Stage     `json:"stage"`
	Status     Decision  `json:"status"`
	StopReason string    `json:"stop_reason,omitempty"`
	Request    string    `json:"request"`
	// ContractInput is user-supplied clarification retained only when a
	// pre-execution contract needed it. Request remains the immutable original
	// goal; this field is supplemental input, never a rewritten request.
	ContractInput    string        `json:"contract_input,omitempty"`
	Objective        string        `json:"objective,omitempty"`
	Limits           Limits        `json:"limits"`
	ToolCalls        int           `json:"tool_calls"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	RepeatedFailures int           `json:"repeated_failures"`
	FailureKey       string        `json:"failure_key,omitempty"`
	Cycles           []Cycle       `json:"cycles,omitempty"`
	Memory           []MemoryEntry `json:"memory,omitempty"`
	Events           []Event       `json:"events,omitempty"`
	// Criteria are the stable acceptance criteria pinned once near run start;
	// Evidence is the bounded cumulative structured ledger. Both are additive
	// to schema v1: older records load with empty slices.
	Criteria []Criterion    `json:"criteria,omitempty"`
	Evidence []EvidenceItem `json:"evidence,omitempty"`
	// StartContext preserves the exact bounded conversation context selected
	// at admission. It is used only for cycle one; later cycles remain scoped
	// to verified run memory. Captured distinguishes an intentionally empty
	// snapshot from an older persisted record that predates this field.
	StartContextCaptured bool          `json:"start_context_captured,omitempty"`
	StartSummary         string        `json:"start_summary,omitempty"`
	StartTurns           []ContextTurn `json:"start_turns,omitempty"`
}

// StopResult is the explicit stop-check output.
type StopResult struct {
	Decision      Decision `json:"decision"`
	Reason        string   `json:"reason"`
	NextObjective string   `json:"next_objective,omitempty"`
}
