package agent

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// NewID returns a random, log-friendly run identifier.
func NewID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate agent run ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewRun validates limits and starts a run at the trigger stage.
func NewRun(id, request string, limits Limits, now time.Time) (*AgentRun, error) {
	id = strings.TrimSpace(id)
	request = strings.TrimSpace(request)
	if id == "" || request == "" {
		return nil, fmt.Errorf("%w: run ID and request are required", ErrInvalidTransition)
	}
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	r := &AgentRun{
		Version:   SchemaVersion,
		ID:        id,
		CreatedAt: now.UTC(),
		UpdatedAt: now.UTC(),
		Stage:     StageTrigger,
		Status:    DecisionRunning,
		Request:   truncate(request, 16*1024),
		Limits:    limits,
	}
	r.addEvent(now, "run_started", "agent run started")
	return r, nil
}

func validateLimits(l Limits) error {
	if l.MaxCycles <= 0 || l.MaxToolCalls <= 0 || l.MaxTokens <= 0 || l.MaxElapsed <= 0 || l.MaxRepeatedFailures <= 0 {
		return fmt.Errorf("%w: all limits must be positive", ErrBudgetExhausted)
	}
	return nil
}

// BeginCycle records deterministic context provenance and enters the executor.
func (r *AgentRun) BeginCycle(objective string, sources []string, now time.Time) error {
	if r == nil || r.Status != DecisionRunning || (r.Stage != StageTrigger && r.Stage != StageStopCheck) {
		return r.transitionError(StageRulesLoad)
	}
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return fmt.Errorf("%w: cycle objective is empty", ErrInvalidTransition)
	}
	if r.Cycle >= r.Limits.MaxCycles {
		return fmt.Errorf("%w: maximum %d cycles reached", ErrBudgetExhausted, r.Limits.MaxCycles)
	}
	r.Cycle++
	r.Objective = truncate(objective, 4096)
	r.Stage = StageRulesLoad
	r.UpdatedAt = now.UTC()
	r.Cycles = append(r.Cycles, Cycle{
		Number:         r.Cycle,
		Objective:      r.Objective,
		ContextSources: boundedStrings(sources, 32, 128),
		StartedAt:      now.UTC(),
	})
	r.addEvent(now, "rules_loaded", fmt.Sprintf("loaded %d context source(s)", len(sources)))
	r.addEvent(now, "objective_selected", r.Objective)
	r.Stage = StageExecutor
	r.addEvent(now, "execution_started", "bounded cycle execution started")
	return nil
}

// CompleteExecution records observable executor output and enters verification.
func (r *AgentRun) CompleteExecution(result ExecutionResult, now time.Time) error {
	cycle, err := r.currentCycle(StageExecutor)
	if err != nil {
		return err
	}
	result.Objective = truncate(strings.TrimSpace(result.Objective), 4096)
	if result.Objective == "" {
		result.Objective = r.Objective
	}
	boundExecution(&result)
	cycle.Execution = &result
	r.ToolCalls += len(result.ToolCalls)
	r.Stage = StageVerifier
	r.UpdatedAt = now.UTC()
	r.addEvent(now, "execution_completed", result.Summary)
	r.addEvent(now, "verification_started", "fresh-context verification started")
	return nil
}

// CompleteVerification records a structured verdict and enters memory write.
func (r *AgentRun) CompleteVerification(result VerificationResult, now time.Time) error {
	cycle, err := r.currentCycle(StageVerifier)
	if err != nil {
		return err
	}
	if !validVerdict(result.Verdict) {
		return fmt.Errorf("%w: unknown verdict %q", ErrMalformedControl, result.Verdict)
	}
	boundVerification(&result)
	cycle.Verification = &result
	r.updateFailureCount(result)
	r.Stage = StageMemoryWrite
	r.UpdatedAt = now.UTC()
	r.addEvent(now, "verification_completed", string(result.Verdict)+": "+result.Summary)
	return nil
}

// WriteMemory creates one concise cycle entry and enters the stop check.
func (r *AgentRun) WriteMemory(now time.Time) error {
	cycle, err := r.currentCycle(StageMemoryWrite)
	if err != nil {
		return err
	}
	if cycle.Execution == nil || cycle.Verification == nil {
		return fmt.Errorf("%w: cycle is missing execution or verification", ErrInvalidTransition)
	}
	r.Memory = append(r.Memory, MemoryEntry{
		Cycle:             cycle.Number,
		Objective:         cycle.Objective,
		ExecutionSummary:  cycle.Execution.Summary,
		Verdict:           cycle.Verification.Verdict,
		Verification:      cycle.Verification.Summary,
		FailedCriteria:    append([]string(nil), cycle.Verification.FailedCriteria...),
		RemainingCriteria: append([]string(nil), cycle.Verification.RemainingCriteria...),
		Artifacts:         append([]string(nil), cycle.Execution.Artifacts...),
		RecommendedNext:   cycle.Verification.RecommendedNext,
		RecordedAt:        now.UTC(),
	})
	if len(r.Memory) > r.Limits.MaxCycles {
		r.Memory = r.Memory[len(r.Memory)-r.Limits.MaxCycles:]
	}
	r.Stage = StageStopCheck
	r.UpdatedAt = now.UTC()
	r.addEvent(now, "memory_written", "bounded cycle summary recorded")
	return nil
}

// ApplyStop records a stop-policy outcome. Continue and retry keep the run
// active at the stop-check boundary; every other outcome is terminal.
func (r *AgentRun) ApplyStop(stop StopResult, now time.Time) error {
	if r == nil || r.Stage != StageStopCheck || r.Status != DecisionRunning {
		return r.transitionError(StageStopCheck)
	}
	if stop.Decision == DecisionRunning || stop.Decision == "" {
		return fmt.Errorf("%w: stop decision is not explicit", ErrInvalidTransition)
	}
	r.StopReason = truncate(stop.Reason, 1024)
	r.UpdatedAt = now.UTC()
	cycle := &r.Cycles[len(r.Cycles)-1]
	cycle.CompletedAt = now.UTC()
	if stop.Decision == DecisionContinue || stop.Decision == DecisionRetry {
		r.Objective = truncate(strings.TrimSpace(stop.NextObjective), 4096)
		r.addEvent(now, string(stop.Decision), r.StopReason)
		return nil
	}
	r.Status = stop.Decision
	r.addEvent(now, "run_"+string(stop.Decision), r.StopReason)
	return nil
}

// Cancel marks a non-terminal run cancelled at any stage.
func (r *AgentRun) Cancel(reason string, now time.Time) {
	if r == nil || r.Status != DecisionRunning {
		return
	}
	r.Status = DecisionCancelled
	r.StopReason = truncate(reason, 1024)
	r.UpdatedAt = now.UTC()
	r.addEvent(now, "run_cancelled", r.StopReason)
}

// Terminate records an exceptional terminal outcome from any active stage.
// Normal cycle completion must still use Decide and ApplyStop.
func (r *AgentRun) Terminate(decision Decision, reason string, now time.Time) error {
	if r == nil || r.Status != DecisionRunning || !isTerminal(decision) {
		return fmt.Errorf("%w: cannot terminate as %s", ErrInvalidTransition, decision)
	}
	r.Status = decision
	r.StopReason = truncate(reason, 1024)
	r.UpdatedAt = now.UTC()
	r.addEvent(now, "run_"+string(decision), r.StopReason)
	return nil
}

// Resume normalizes a persisted parked, input-blocked, or interrupted run to
// the stop-check boundary so the caller can begin a fresh cycle. It never
// replays an incomplete executor or tool call.
func (r *AgentRun) Resume(nextObjective string, now time.Time) error {
	if r == nil {
		return fmt.Errorf("%w: cannot resume a nil run", ErrInvalidTransition)
	}
	switch r.Status {
	case DecisionRunning, DecisionParked, DecisionNeedsUserInput:
	default:
		return fmt.Errorf("%w: run status %s is terminal", ErrInvalidTransition, r.Status)
	}
	if r.Cycle >= r.Limits.MaxCycles {
		return fmt.Errorf("%w: maximum %d cycles reached", ErrBudgetExhausted, r.Limits.MaxCycles)
	}
	r.Status = DecisionRunning
	r.Stage = StageStopCheck
	r.StopReason = ""
	if nextObjective = strings.TrimSpace(nextObjective); nextObjective != "" {
		r.Objective = truncate(nextObjective, 4096)
	}
	r.UpdatedAt = now.UTC()
	r.addEvent(now, "run_resumed", "resumed with a fresh cycle; incomplete execution was not replayed")
	return nil
}

// RecordUsage accounts provider usage for hard or diagnostic budgets.
func (r *AgentRun) RecordUsage(prompt, completion int, now time.Time) {
	if r == nil {
		return
	}
	r.PromptTokens += max(prompt, 0)
	r.CompletionTokens += max(completion, 0)
	r.UpdatedAt = now.UTC()
}

// LatestCycle returns the current cycle or nil before the first cycle.
func (r *AgentRun) LatestCycle() *Cycle {
	if r == nil || len(r.Cycles) == 0 {
		return nil
	}
	return &r.Cycles[len(r.Cycles)-1]
}

func (r *AgentRun) currentCycle(stage Stage) (*Cycle, error) {
	if r == nil || r.Status != DecisionRunning || r.Stage != stage || len(r.Cycles) == 0 {
		return nil, r.transitionError(stage)
	}
	return &r.Cycles[len(r.Cycles)-1], nil
}

func (r *AgentRun) transitionError(want Stage) error {
	if r == nil {
		return fmt.Errorf("%w: nil run cannot enter %s", ErrInvalidTransition, want)
	}
	return fmt.Errorf("%w: run %s is %s/%s, cannot enter %s", ErrInvalidTransition, r.ID, r.Stage, r.Status, want)
}

func (r *AgentRun) addEvent(now time.Time, kind, detail string) {
	e := Event{Time: now.UTC(), RunID: r.ID, Cycle: r.Cycle, Stage: r.Stage, Kind: kind, Detail: truncate(detail, 512)}
	r.Events = append(r.Events, e)
	if len(r.Events) > MaxEvents {
		r.Events = r.Events[len(r.Events)-MaxEvents:]
	}
}

func (r *AgentRun) updateFailureCount(v VerificationResult) {
	if v.Verdict == VerificationPassed {
		r.FailureKey = ""
		r.RepeatedFailures = 0
		return
	}
	key := failureKey(v)
	if key == r.FailureKey {
		r.RepeatedFailures++
	} else {
		r.FailureKey = key
		r.RepeatedFailures = 1
	}
}

func failureKey(v VerificationResult) string {
	return strings.Join([]string{
		string(v.Verdict),
		strings.ToLower(strings.TrimSpace(v.Summary)),
		strings.Join(v.FailedCriteria, "\x00"),
		strings.ToLower(strings.TrimSpace(v.RecommendedNext)),
	}, "\x00")
}

func validVerdict(v VerificationVerdict) bool {
	switch v {
	case VerificationPassed, VerificationFailed, VerificationInconclusive, VerificationBlocked:
		return true
	}
	return false
}

func boundExecution(r *ExecutionResult) {
	r.Summary = truncate(r.Summary, 4096)
	r.SuggestedNext = truncate(r.SuggestedNext, 2048)
	if len(r.ToolCalls) > 128 {
		r.ToolCalls = r.ToolCalls[:128]
	}
	for i := range r.ToolCalls {
		r.ToolCalls[i].ID = truncate(r.ToolCalls[i].ID, 128)
		r.ToolCalls[i].Name = truncate(r.ToolCalls[i].Name, 256)
		r.ToolCalls[i].Summary = truncate(r.ToolCalls[i].Summary, 512)
	}
	r.Artifacts = boundedStrings(r.Artifacts, 64, 512)
	r.ChangedFiles = boundedStrings(r.ChangedFiles, 64, 512)
	if len(r.TestsRun) > 64 {
		r.TestsRun = r.TestsRun[:64]
	}
	if len(r.Errors) > 32 {
		r.Errors = r.Errors[:32]
	}
}

func boundVerification(r *VerificationResult) {
	r.Summary = truncate(r.Summary, 4096)
	r.Evidence = boundedStrings(r.Evidence, 64, 512)
	r.FailedCriteria = boundedStrings(r.FailedCriteria, 64, 512)
	r.RemainingCriteria = boundedStrings(r.RemainingCriteria, 64, 512)
	r.RecommendedNext = truncate(r.RecommendedNext, 2048)
	if r.Confidence < 0 {
		r.Confidence = 0
	}
	if r.Confidence > 1 {
		r.Confidence = 1
	}
}

func boundedStrings(values []string, count, width int) []string {
	if len(values) > count {
		values = values[:count]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, truncate(value, width))
		}
	}
	return out
}

func truncate(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes] + "…"
}

func hasErrorKind(errorsIn []RunError, kinds ...ErrorKind) bool {
	for _, item := range errorsIn {
		for _, kind := range kinds {
			if item.Kind == kind {
				return true
			}
		}
	}
	return false
}

func isTerminal(d Decision) bool {
	return d != DecisionRunning && d != DecisionContinue && d != DecisionRetry
}

var _ error = RunError{}
