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
	// Status classifies what actually happened to the call attempt, additive
	// to schema v1 (empty on records persisted before this field existed —
	// treat that as unknown, never infer it was executed). Distinct from
	// Succeeded, which only reports the outcome of a call that did run: a
	// denied or ledger-blocked call has Succeeded=false and Status other than
	// ActionExecuted, so no side effect can be inferred from failure alone.
	// See ActionStatus in receipts.go.
	Status ActionStatus `json:"status,omitempty"`
}

// TestResult is deterministic evidence reported by an executor adapter.
type TestResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Summary string `json:"summary,omitempty"`
}

// ReadObservation is a bounded, criterion-neutral receipt of one delivered
// read window for one target resource. It exists so criteria/yield coverage
// logic can reason about *what was actually delivered*, not merely that some
// call touching a path succeeded (agent-execution-harness plan §4 finding
// #3: "read success loses coverage at the agent boundary"). It carries no
// raw body — only identity, version, window, and total-source metadata
// already present in tools.ResultMeta, translated by the TUI layer (this
// package must never import internal/tools).
type ReadObservation struct {
	// Target is the criterion-neutral resource identity this observation is
	// about — a workspace-relative path, lowercased/trimmed exactly like
	// ExactReadCriterionTarget's own extraction, so the two compare directly.
	Target string `json:"target"`
	// SourceDigest is the producer's own SHA-256 of the complete raw source
	// (tools.ResultMeta.SourceDigest), populated only when the read
	// established it. Empty means unknown. Observations for the same Target
	// with two different non-empty digests must never be combined into one
	// coverage claim — that would mix two versions of the file.
	SourceDigest string `json:"source_digest,omitempty"`
	// StartLine/EndLine are the 1-based inclusive delivered line window; zero
	// for a byte-only or non-line-addressable observation. Coverage union
	// only considers observations with a valid line window this phase.
	StartLine int64 `json:"start_line,omitempty"`
	EndLine   int64 `json:"end_line,omitempty"`
	// TotalLines is the producer's own total line count for the complete
	// source, populated only when its scan reached end of file without a
	// scan-limit truncation. Nil means unknown; an observation with a nil
	// TotalLines can never by itself, or in union with others, prove
	// whole-file coverage.
	TotalLines *int64 `json:"total_lines,omitempty"`
	// Sequence is this observation's position in this execution's ordered
	// receipt list (assigned by AppendReadObservation) — monotonically
	// increasing, never a timestamp.
	Sequence int `json:"sequence,omitempty"`
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
	// ReadObservations is the bounded, ordered list of delivered read windows
	// this cycle has observed so far — see ReadObservation and
	// AppendReadObservation. Additive to schema v1: absent/empty on any
	// record persisted before this field existed, which must be read as
	// "unknown coverage", never as "fully covered".
	ReadObservations []ReadObservation `json:"read_observations,omitempty"`
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
	// Episode is bounded, additive checkpoint metadata for this cycle's
	// executor episode (Phase 2 of the agent-execution-harness plan). It is
	// nil for every cycle today and for any run persisted before this field
	// existed; a nil Episode must never be read as "no obligations remain"
	// or as any other decision — only as "not yet tracked".
	Episode *EpisodeCheckpoint `json:"episode,omitempty"`
}

// EpisodeCheckpoint is bounded, additive metadata for one execution episode
// — the executor portion of a Cycle, from BeginCycle to its single
// CompleteExecution, including waits for approval/input and any number of
// budgeted model/tool turns. Episode identity is implicitly (run.ID,
// run.Cycle); this type adds no independent episode number that could drift
// from cycle identity. It exists so a controller-owned yield continuation
// (Phase 2) can track its own bounded counters and last decision without a
// second execution-state store. It carries no raw transcript, tool body,
// hidden reasoning, or secret — see docs/architecture/decisions/0013 §
// "Persistence, restart and accounting".
type EpisodeCheckpoint struct {
	// PolicyVersion is the yield policy version active when this episode
	// started. Persisted so a mid-run configuration change cannot silently
	// reinterpret an in-flight checkpoint.
	PolicyVersion int `json:"policy_version"`
	// EnabledAtStart records whether yield continuation was enabled when
	// this episode began, independent of the run's current live config.
	EnabledAtStart bool `json:"enabled_at_start"`

	// ExecutorRequests counts every provider attempt this episode's executor
	// has made, including transport retries and native-tool fallback — not
	// only successful replies. Bounded by a configured per-episode ceiling,
	// independent of the existing tool-round budget.
	ExecutorRequests int `json:"executor_requests,omitempty"`
	// NoProgressNudges counts controller continuations admitted since the
	// last observed relevant progress. See YieldInput.NoProgressNudges.
	NoProgressNudges int `json:"no_progress_nudges,omitempty"`

	// LastYieldReason records the most recent yield decision's reason, for
	// diagnostics and debug overlays only — never authority for a later
	// decision by itself.
	LastYieldReason YieldReason `json:"last_yield_reason,omitempty"`
	// LastProgressDigest is the most recent deterministic progress
	// fingerprint this episode observed (see internal/tui/progress.go),
	// opaque here — used only to detect whether a later yield changed
	// anything relevant.
	LastProgressDigest string `json:"last_progress_digest,omitempty"`
	// UnresolvedCriterionIDs are the pinned criterion IDs this episode's
	// last decision still considered outstanding, capped at MaxCriteria.
	UnresolvedCriterionIDs []string `json:"unresolved_criterion_ids,omitempty"`

	// Revision increments on every checkpoint write so a stale asynchronous
	// save (see persistAgentRun) can never silently overwrite a newer one.
	Revision int `json:"revision,omitempty"`
	// Interrupted marks a checkpoint saved mid-episode (e.g. a process
	// restart) whose pending counts were never consumed by a
	// CompleteExecution. Resume must never infer completion from it.
	Interrupted bool `json:"interrupted,omitempty"`
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
