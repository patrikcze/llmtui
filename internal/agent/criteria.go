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
			if !staleAfterMutation(*criterion, execution, cycle) {
				continue
			}
			// A relevant mutation in a later cycle invalidates this
			// criterion's prior proof: a check supports only the workspace
			// version it inspected. Fall through so this same cycle's own
			// evidence, if any, can immediately re-satisfy it fresh — an
			// edit-then-rerun sequence in one cycle is not stale.
			criterion.Status = CriterionPending
			criterion.Note = "stale: a file changed after this check last passed"
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

// staleAfterMutation reports whether a previously satisfied or
// not-applicable mechanical criterion must be treated as unproven again
// because a later cycle changed a file. A check is evidence about the
// workspace version it inspected, not a permanent fact — see
// .claude/tasks/plans/llmtui-agent-evolution.md §11.3. This package has no
// per-file test-coverage mapping, so invalidation is deliberately
// conservative: any file change in a strictly later cycle invalidates any
// test-result or command-exit criterion, regardless of which file changed.
// File-state criteria are about the file's current content — a further edit
// answers rather than invalidates them, and evaluateCriterion already
// re-checks the latest ChangedFiles each cycle. User-input criteria are
// unaffected: a later file edit does not un-supply an answer already given.
func staleAfterMutation(criterion Criterion, execution ExecutionResult, cycle int) bool {
	if cycle <= criterion.UpdatedCycle || len(execution.ChangedFiles) == 0 {
		return false
	}
	switch criterion.Kind {
	case CriterionTestResult, CriterionCommandExit:
		return true
	default:
		return false
	}
}

func evaluateCriterion(criterion Criterion, execution ExecutionResult) (matched, passed bool, note string) {
	match := func(value string) bool {
		return criterion.Target == "" || criterion.Target == "*" || strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(criterion.Target))
	}
	switch criterion.Kind {
	case CriterionSemantic:
		// A contract model occasionally emits only "Read the file X" for a
		// larger request. That exact, atomic criterion is mechanically proven
		// by a successful read_file call; leaving it to a verifier that cannot
		// inspect redacted contents causes the same read to be retried forever.
		// Combined criteria remain semantic and still require verification.
		for _, call := range execution.ToolCalls {
			if call.Name == "read_file" && call.Succeeded && isExactReadCriterion(criterion.Text, call.Detail) {
				return true, true, "observed read_file success"
			}
		}
	case CriterionCommandExit:
		// Scan from the most recent call backward: a single cycle can already
		// contain several tool rounds (many consecutive turns inside one
		// executor episode), so an earlier pass must not stay authoritative
		// over a later rerun of the same command that failed. The latest
		// matching observation is what the workspace looks like now.
		for i := len(execution.ToolCalls) - 1; i >= 0; i-- {
			call := execution.ToolCalls[i]
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
		// Same latest-observation-wins rule as CriterionCommandExit above.
		for i := len(execution.TestsRun) - 1; i >= 0; i-- {
			test := execution.TestsRun[i]
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

func isExactReadCriterion(text, path string) bool {
	target, ok := ExactReadCriterionTarget(text)
	path = strings.ToLower(strings.TrimSpace(path))
	return ok && path != "" && target == path
}

// ExactReadCriterionTarget extracts the target path from a criterion
// recognized as an atomic "read this exact file" instruction — the same
// narrow grammar isExactReadCriterion's deterministic proof requires ("Read
// [the/file/named] <path>."), shared here so Phase 2's yield-eligibility
// check (agent-execution-harness plan §10) can recognize a *pending*
// exact-read obligation before any tool call has proven it, not only verify
// one after the fact. ok is false for anything not exactly this shape:
// multi-step or ambiguous prose ("inspect", "review", "compare"), a quoted
// shell fragment, or a criterion naming no target at all remain semantic.
func ExactReadCriterionTarget(text string) (target string, ok bool) {
	text = strings.ToLower(strings.TrimSpace(strings.TrimRight(text, ".")))
	if !strings.HasPrefix(text, "read ") {
		return "", false
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "read "))
	for _, prefix := range []string{"the ", "file ", "named "} {
		if strings.HasPrefix(text, prefix) {
			text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
		}
	}
	if text == "" {
		return "", false
	}
	return text, true
}

// ExactReadObligation is one pinned criterion this run's yield-eligibility
// check recognizes as an atomic exact-read instruction not yet proven by the
// given execution.
type ExactReadObligation struct {
	CriterionID string
	// Target is the criterion's own extracted target text (lowercased,
	// trimmed) — controller-owned criterion data, not raw model or tool
	// output. A caller quoting it into a prompt must still frame it as data.
	Target string
}

// PendingExactReadObligations previews, without mutating any criterion
// status, which of r's currently unresolved criteria (CriterionSemantic, or
// the legacy-untyped empty Kind) are atomic exact-read instructions not yet
// proven by execution. It shares evaluateCriterion's own matching logic —
// the same logic ApplyDeterministicCriteria commits at verification — so a
// read that already succeeded earlier this cycle is never reported as still
// outstanding merely because run.Criteria has not been committed yet (see
// docs/architecture/decisions/0013-agent-execution-yield-policy.md). Only
// Phase 2's narrow exact-read grammar is recognized; every other criterion
// kind is left to the existing verifier untouched.
func (r *AgentRun) PendingExactReadObligations(execution ExecutionResult) []ExactReadObligation {
	if r == nil {
		return nil
	}
	var out []ExactReadObligation
	for _, criterion := range r.UnresolvedCriteria() {
		if criterion.Kind != "" && criterion.Kind != CriterionSemantic {
			continue
		}
		target, ok := ExactReadCriterionTarget(criterion.Text)
		if !ok {
			continue
		}
		if matched, passed, _ := evaluateCriterion(criterion, execution); matched && passed {
			continue // already proven earlier this cycle; not outstanding
		}
		out = append(out, ExactReadObligation{CriterionID: criterion.ID, Target: target})
	}
	return out
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
// changed file, per typed error, plus one entry per distinct successful tool
// name. Naming the successful tools (rather than a single "N succeeded"
// aggregate) lets the cross-cycle ledger show a recover-and-proceed
// sequence — e.g. that a valid ask_user and a confirmed write_file followed
// earlier invalid ask_user calls.
func CollectEvidence(cycle int, execution ExecutionResult) []EvidenceItem {
	var items []EvidenceItem
	successCount := map[string]int{}
	var successOrder []string
	for _, call := range execution.ToolCalls {
		if call.Succeeded {
			if successCount[call.Name] == 0 {
				successOrder = append(successOrder, call.Name)
			}
			successCount[call.Name]++
			continue
		}
		items = append(items, EvidenceItem{
			Cycle: cycle, Kind: EvidenceToolFailure, Source: call.Name,
			Summary: string(call.ErrorKind), Success: false,
		})
	}
	for _, name := range successOrder {
		summary := name + " succeeded"
		if name == "ask_user" {
			summary = "ask_user succeeded and a user answer was received"
		}
		if n := successCount[name]; n > 1 {
			summary = fmt.Sprintf("%s succeeded (%d calls)", name, n)
		}
		items = append(items, EvidenceItem{
			Cycle: cycle, Kind: EvidenceTool, Source: name, Summary: summary, Success: true,
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

// UserAnswerCausalFacts returns bounded controller facts about a completed
// ask_user barrier. A later mutation in the same ToolCalls sequence follows
// the answer because the executor cannot resume until that answer arrives.
// The verifier receives this fact separately from model prose so it does not
// mistake one live executor cycle for simultaneous actions.
func UserAnswerCausalFacts(execution ExecutionResult) []string {
	answerReceived := false
	for _, call := range execution.ToolCalls {
		if call.Name == "ask_user" && call.Succeeded {
			answerReceived = true
			continue
		}
		if answerReceived && call.Succeeded && (call.Name == "write_file" || call.Name == "edit_file") {
			return []string{"a user answer was received before the later workspace mutation"}
		}
	}
	return nil
}
