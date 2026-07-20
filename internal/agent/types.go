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
	StageTrigger     Stage = "trigger"
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

// ToolCallRecord stores only bounded outcome metadata, never arguments or
// output, so run memory cannot become a second transcript or secret store.
type ToolCallRecord struct {
	ID        string    `json:"id,omitempty"`
	Name      string    `json:"name"`
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
}

// MemoryEntry is concise cycle-to-cycle state. It deliberately excludes raw
// prompts, tool arguments/output, and model reasoning.
type MemoryEntry struct {
	Cycle             int                 `json:"cycle"`
	Objective         string              `json:"objective"`
	ExecutionSummary  string              `json:"execution_summary,omitempty"`
	Verdict           VerificationVerdict `json:"verdict"`
	Verification      string              `json:"verification,omitempty"`
	FailedCriteria    []string            `json:"failed_criteria,omitempty"`
	RemainingCriteria []string            `json:"remaining_criteria,omitempty"`
	Artifacts         []string            `json:"artifacts,omitempty"`
	RecommendedNext   string              `json:"recommended_next,omitempty"`
	RecordedAt        time.Time           `json:"recorded_at"`
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

// AgentRun is the serializable state of one bounded user request.
type AgentRun struct {
	Version          int           `json:"version"`
	ID               string        `json:"id"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
	Cycle            int           `json:"cycle"`
	Stage            Stage         `json:"stage"`
	Status           Decision      `json:"status"`
	StopReason       string        `json:"stop_reason,omitempty"`
	Request          string        `json:"request"`
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
}

// StopResult is the explicit stop-check output.
type StopResult struct {
	Decision      Decision `json:"decision"`
	Reason        string   `json:"reason"`
	NextObjective string   `json:"next_objective,omitempty"`
}
