package agent

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// MaxCriteria bounds the pinned acceptance-criteria list for one run.
	MaxCriteria = 12
	// MaxEvidence bounds the run-level structured evidence ledger.
	MaxEvidence = 64
)

// CriterionStatus tracks one acceptance criterion across the whole run.
type CriterionStatus string

// CriterionKind identifies who can authoritatively resolve a criterion.
// Empty/semantic preserves persisted v1 behavior.
type CriterionKind string

const (
	CriterionSemantic    CriterionKind = "semantic"
	CriterionCommandExit CriterionKind = "command_exit"
	CriterionFileState   CriterionKind = "file_state"
	CriterionTestResult  CriterionKind = "test_result"
	CriterionUserInput   CriterionKind = "user_input"
)

const (
	CriterionPending       CriterionStatus = "pending"
	CriterionSatisfied     CriterionStatus = "satisfied"
	CriterionFailed        CriterionStatus = "failed"
	CriterionNotApplicable CriterionStatus = "not_applicable"
)

// ValidCriterionStatus reports whether a status value is one of the four
// documented states. Control data with any other value is malformed.
func ValidCriterionStatus(s CriterionStatus) bool {
	switch s {
	case CriterionPending, CriterionSatisfied, CriterionFailed, CriterionNotApplicable:
		return true
	}
	return false
}

// Criterion is one stable acceptance criterion, pinned near run start and
// carried unchanged for the life of the run; only Status/Note evolve.
type Criterion struct {
	ID           string          `json:"id"`
	Text         string          `json:"text"`
	Status       CriterionStatus `json:"status"`
	Note         string          `json:"note,omitempty"`
	UpdatedCycle int             `json:"updated_cycle,omitempty"`
	Kind         CriterionKind   `json:"kind,omitempty"`
	Target       string          `json:"target,omitempty"`
}

// CriterionSpec defines a criterion before stable IDs are assigned.
type CriterionSpec struct {
	Text   string
	Kind   CriterionKind
	Target string
}

// CriterionUpdate is a per-cycle status change keyed by pinned criterion ID.
type CriterionUpdate struct {
	ID     string          `json:"id"`
	Status CriterionStatus `json:"status"`
	Note   string          `json:"note,omitempty"`
}

// EvidenceKind classifies one structured evidence ledger entry.
type EvidenceKind string

const (
	EvidenceTool        EvidenceKind = "tool"
	EvidenceToolFailure EvidenceKind = "tool_failure"
	EvidenceTest        EvidenceKind = "test"
	EvidenceFile        EvidenceKind = "file"
	EvidenceSemantic    EvidenceKind = "semantic"
	EvidenceError       EvidenceKind = "error"
)

// EvidenceItem is one bounded, objective observation. The ledger holds what
// the runtime saw, not what a model claimed; Summary never contains raw tool
// output or secrets (sources feeding it are already bounded summaries).
type EvidenceItem struct {
	Cycle   int          `json:"cycle"`
	Kind    EvidenceKind `json:"kind"`
	Source  string       `json:"source"`
	Summary string       `json:"summary,omitempty"`
	Success bool         `json:"success"`
}

// PinCriteria establishes the run's stable acceptance criteria exactly once.
// Later calls are ignored so a verifier cannot rewrite the goalposts
// mid-run. IDs are assigned deterministically (c1..cN).
func (r *AgentRun) PinCriteria(texts []string) {
	specs := make([]CriterionSpec, 0, len(texts))
	for _, text := range texts {
		specs = append(specs, CriterionSpec{Text: text, Kind: CriterionSemantic})
	}
	r.PinTypedCriteria(specs)
}

// PinTypedCriteria establishes controller-owned deterministic or semantic
// criteria exactly once.
func (r *AgentRun) PinTypedCriteria(specs []CriterionSpec) {
	if r == nil || len(r.Criteria) > 0 {
		return
	}
	if len(specs) > MaxCriteria {
		specs = specs[:MaxCriteria]
	}
	for _, spec := range specs {
		text := strings.TrimSpace(spec.Text)
		if text == "" {
			continue
		}
		kind := spec.Kind
		if kind == "" {
			kind = CriterionSemantic
		}
		r.Criteria = append(r.Criteria, Criterion{
			ID:     fmt.Sprintf("c%d", len(r.Criteria)+1),
			Text:   truncate(text, 256),
			Status: CriterionPending,
			Kind:   kind,
			Target: truncate(strings.TrimSpace(spec.Target), 256),
		})
	}
}

// HasCriteria reports whether stable criteria have been pinned.
func (r *AgentRun) HasCriteria() bool { return r != nil && len(r.Criteria) > 0 }

// ApplyCriteriaUpdates applies per-ID status changes for one cycle. Unknown
// IDs and invalid statuses are ignored — the pinned set is authoritative and
// a verifier cannot add, remove, or rename criteria through updates.
func (r *AgentRun) ApplyCriteriaUpdates(updates []CriterionUpdate, cycle int) {
	if r == nil || len(r.Criteria) == 0 {
		return
	}
	for _, update := range updates {
		if !ValidCriterionStatus(update.Status) {
			continue
		}
		for i := range r.Criteria {
			if r.Criteria[i].ID == update.ID {
				r.Criteria[i].Status = update.Status
				r.Criteria[i].Note = truncate(strings.TrimSpace(update.Note), 256)
				r.Criteria[i].UpdatedCycle = cycle
				break
			}
		}
	}
}

// UnresolvedCriteria returns pinned criteria still pending or failed — the
// only criteria that may drive replanning.
func (r *AgentRun) UnresolvedCriteria() []Criterion {
	if r == nil {
		return nil
	}
	var out []Criterion
	for _, criterion := range r.Criteria {
		if criterion.Status == CriterionPending || criterion.Status == CriterionFailed {
			out = append(out, criterion)
		}
	}
	return out
}

// UnresolvedSemanticCriteria returns only criteria that require model
// judgment; deterministic and user-owned criteria never enter its prompt.
func (r *AgentRun) UnresolvedSemanticCriteria() []Criterion {
	var out []Criterion
	for _, criterion := range r.UnresolvedCriteria() {
		if criterion.Kind == "" || criterion.Kind == CriterionSemantic {
			out = append(out, criterion)
		}
	}
	return out
}

// ApplyDeterministicCriteria resolves typed criteria exclusively from runtime
// observations. Executor prose is intentionally ignored.
func (r *AgentRun) ApplyDeterministicCriteria(execution ExecutionResult, cycle int) {
	if r == nil {
		return
	}
	for i := range r.Criteria {
		criterion := &r.Criteria[i]
		if criterion.Status == CriterionSatisfied || criterion.Status == CriterionNotApplicable {
			continue
		}
		matched, passed, note := evaluateCriterion(*criterion, execution)
		if !matched {
			continue
		}
		criterion.Status = CriterionFailed
		if passed {
			criterion.Status = CriterionSatisfied
		}
		criterion.Note = truncate(note, 256)
		criterion.UpdatedCycle = cycle
	}
}

func evaluateCriterion(criterion Criterion, execution ExecutionResult) (matched, passed bool, note string) {
	match := func(value string) bool {
		return criterion.Target == "" || criterion.Target == "*" || strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(criterion.Target))
	}
	switch criterion.Kind {
	case CriterionCommandExit:
		for _, call := range execution.ToolCalls {
			if call.Name == "run_command" && match(call.Detail) {
				return true, call.Succeeded, "observed run_command exit"
			}
		}
	case CriterionFileState:
		for _, file := range execution.ChangedFiles {
			if match(file) {
				return true, true, "observed file state change"
			}
		}
	case CriterionTestResult:
		for _, test := range execution.TestsRun {
			if match(test.Name) {
				return true, test.Passed, "observed test result: " + test.Name
			}
		}
	case CriterionUserInput:
		if execution.NeedsUserInput {
			return true, false, "explicit user input required"
		}
	}
	return false, false, ""
}

// unresolvedCriteriaKey is a phrasing-immune fingerprint of the unresolved
// set, used for repeated-failure detection when criteria are pinned.
func (r *AgentRun) unresolvedCriteriaKey() string {
	ids := make([]string, 0, len(r.Criteria))
	for _, criterion := range r.UnresolvedCriteria() {
		ids = append(ids, criterion.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, "\x00")
}

// AppendEvidence appends bounded ledger entries, keeping only the most
// recent MaxEvidence items so run state stays deterministically sized.
func (r *AgentRun) AppendEvidence(items []EvidenceItem) {
	if r == nil || len(items) == 0 {
		return
	}
	for _, item := range items {
		item.Source = truncate(strings.TrimSpace(item.Source), 256)
		item.Summary = truncate(strings.TrimSpace(item.Summary), 256)
		r.Evidence = append(r.Evidence, item)
	}
	if len(r.Evidence) > MaxEvidence {
		r.Evidence = r.Evidence[len(r.Evidence)-MaxEvidence:]
	}
}

// CollectEvidence derives structured ledger entries from one cycle's
// observable execution: one entry per test, per failed tool call, per
// changed file, per typed error, plus one aggregate for successful calls.
func CollectEvidence(cycle int, execution ExecutionResult) []EvidenceItem {
	var items []EvidenceItem
	succeeded := 0
	for _, call := range execution.ToolCalls {
		if call.Succeeded {
			succeeded++
			continue
		}
		items = append(items, EvidenceItem{
			Cycle: cycle, Kind: EvidenceToolFailure, Source: call.Name,
			Summary: string(call.ErrorKind), Success: false,
		})
	}
	if succeeded > 0 {
		items = append(items, EvidenceItem{
			Cycle: cycle, Kind: EvidenceTool, Source: "tool_calls",
			Summary: fmt.Sprintf("%d tool call(s) succeeded", succeeded), Success: true,
		})
	}
	for _, test := range execution.TestsRun {
		items = append(items, EvidenceItem{
			Cycle: cycle, Kind: EvidenceTest, Source: test.Name,
			Summary: test.Summary, Success: test.Passed,
		})
	}
	for _, file := range execution.ChangedFiles {
		items = append(items, EvidenceItem{
			Cycle: cycle, Kind: EvidenceFile, Source: file, Summary: "file written", Success: true,
		})
	}
	for _, runErr := range execution.Errors {
		items = append(items, EvidenceItem{
			Cycle: cycle, Kind: EvidenceError, Source: runErr.Op,
			Summary: string(runErr.Kind), Success: false,
		})
	}
	return items
}
