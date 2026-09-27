package agent

import (
	"crypto/sha256"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxCriteria bounds the pinned acceptance-criteria list for one run.
	MaxCriteria = 12
	// MaxEvidence bounds the run-level structured evidence ledger.
	MaxEvidence = 64
	// MaxReadObservations bounds one execution's ordered read-coverage
	// receipt list, matching MaxEvidence's append-and-trim shape.
	MaxReadObservations = 32
	// CriterionAssessmentVersion is the version of the optional, evaluation-only
	// criterion assessment metadata attached to a pinned criterion.
	CriterionAssessmentVersion = 1
	// CriterionAssessmentMaxBytes bounds each free-text assessment field.
	CriterionAssessmentMaxBytes = 256
)

// CriterionStatus tracks one acceptance criterion across the whole run.
type CriterionStatus string

// CriterionKind identifies who can authoritatively resolve a criterion.
// Every pinned criterion is semantic: the contract phase pins criteria as
// semantic, the verifier resolves them, and the one mechanical proof is
// exact-read coverage (evaluateCriterion). Empty means semantic for records
// persisted before the field existed. Earlier builds also defined
// command_exit, file_state, test_result, and user_input kinds, but nothing in
// the contract-first flow could ever create them (audit P2-6); decodeRun
// normalizes any such legacy kind to semantic so it stays resolvable.
type CriterionKind string

// CriterionSemantic is the only criterion kind.
const CriterionSemantic CriterionKind = "semantic"

const (
	CriterionPending       CriterionStatus = "pending"
	CriterionSatisfied     CriterionStatus = "satisfied"
	CriterionFailed        CriterionStatus = "failed"
	CriterionNotApplicable CriterionStatus = "not_applicable"
)

// CriterionAssessmentEvidenceKind identifies the bounded evidence projection
// a future shadow assessor may inspect. It never changes criterion authority.
type CriterionAssessmentEvidenceKind string

const (
	CriterionAssessmentReceipts  CriterionAssessmentEvidenceKind = "receipts"
	CriterionAssessmentLocalRead CriterionAssessmentEvidenceKind = "local_read"
)

// CriterionAssessmentSpec is optional metadata compiled from a task contract.
// It is deliberately neutral: no threshold, status, action, or controller
// instruction is persisted here. Phase 2 stores it for evaluation only.
type CriterionAssessmentSpec struct {
	Version      int                             `json:"version"`
	Proposition  string                          `json:"proposition"`
	EvidenceKind CriterionAssessmentEvidenceKind `json:"evidence_kind"`
	Target       string                          `json:"target,omitempty"`
}

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
	ID           string                   `json:"id"`
	Text         string                   `json:"text"`
	Status       CriterionStatus          `json:"status"`
	Note         string                   `json:"note,omitempty"`
	UpdatedCycle int                      `json:"updated_cycle,omitempty"`
	Kind         CriterionKind            `json:"kind,omitempty"`
	Assessment   *CriterionAssessmentSpec `json:"assessment,omitempty"`
}

// CriterionSpec defines a criterion before stable IDs are assigned.
type CriterionSpec struct {
	Text       string
	Assessment *CriterionAssessmentSpec
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
		specs = append(specs, CriterionSpec{Text: text})
	}
	_ = r.pinTypedCriteria(specs)
}

// PinTypedCriteriaWithAssessments pins criteria and optional assessment
// metadata atomically. Invalid metadata leaves the run unchanged.
func (r *AgentRun) PinTypedCriteriaWithAssessments(specs []CriterionSpec) error {
	return r.pinTypedCriteria(specs)
}

func (r *AgentRun) pinTypedCriteria(specs []CriterionSpec) error {
	if r == nil || len(r.Criteria) > 0 {
		return nil
	}
	if err := validateCriterionSpecs(specs); err != nil {
		return err
	}
	if len(specs) > MaxCriteria {
		for _, spec := range specs[MaxCriteria:] {
			if spec.Assessment != nil {
				return fmt.Errorf("%w: assessment criterion index exceeds maximum %d", ErrMalformedControl, MaxCriteria)
			}
		}
		specs = specs[:MaxCriteria]
	}
	for _, spec := range specs {
		text := strings.TrimSpace(spec.Text)
		if text == "" {
			continue
		}
		criterion := Criterion{
			ID:     fmt.Sprintf("c%d", len(r.Criteria)+1),
			Text:   truncate(text, 256),
			Status: CriterionPending,
			Kind:   CriterionSemantic,
		}
		if spec.Assessment != nil {
			assessment := canonicalCriterionAssessment(*spec.Assessment)
			criterion.Assessment = &assessment
		}
		r.Criteria = append(r.Criteria, criterion)
	}
	return nil
}

// ValidateCriterionAssessmentSpec validates one optional assessment without
// changing it. Callers that pin it use the canonical copy produced by the
// controller-owned pinning path.
func ValidateCriterionAssessmentSpec(spec CriterionAssessmentSpec) error {
	_, err := normalizeCriterionAssessment(spec)
	return err
}

func validateCriterionSpecs(specs []CriterionSpec) error {
	for _, spec := range specs {
		if spec.Assessment == nil {
			continue
		}
		if len([]byte(strings.TrimSpace(spec.Text))) > CriterionAssessmentMaxBytes {
			return fmt.Errorf("%w: assessment source criterion is too long", ErrMalformedControl)
		}
		if _, err := normalizeCriterionAssessment(*spec.Assessment); err != nil {
			return err
		}
	}
	return nil
}

func normalizeCriterionAssessment(spec CriterionAssessmentSpec) (CriterionAssessmentSpec, error) {
	if spec.Version != CriterionAssessmentVersion {
		return CriterionAssessmentSpec{}, fmt.Errorf("%w: unsupported criterion assessment version %d", ErrMalformedControl, spec.Version)
	}
	spec.Proposition = strings.TrimSpace(spec.Proposition)
	if spec.Proposition == "" || !utf8.ValidString(spec.Proposition) || len([]byte(spec.Proposition)) > CriterionAssessmentMaxBytes {
		return CriterionAssessmentSpec{}, fmt.Errorf("%w: invalid criterion assessment proposition", ErrMalformedControl)
	}
	switch spec.EvidenceKind {
	case CriterionAssessmentReceipts:
	case CriterionAssessmentLocalRead:
		if err := validateLocalReadTarget(spec.Target); err != nil {
			return CriterionAssessmentSpec{}, err
		}
	default:
		return CriterionAssessmentSpec{}, fmt.Errorf("%w: unsupported criterion assessment evidence kind %q", ErrMalformedControl, spec.EvidenceKind)
	}
	spec.Target = strings.TrimSpace(spec.Target)
	if !utf8.ValidString(spec.Target) || len([]byte(spec.Target)) > CriterionAssessmentMaxBytes {
		return CriterionAssessmentSpec{}, fmt.Errorf("%w: invalid criterion assessment target", ErrMalformedControl)
	}
	if spec.Target != "" && containsUnsafeAssessmentTarget(spec.Target) {
		return CriterionAssessmentSpec{}, fmt.Errorf("%w: unsafe criterion assessment target", ErrMalformedControl)
	}
	return spec, nil
}

func canonicalCriterionAssessment(spec CriterionAssessmentSpec) CriterionAssessmentSpec {
	normalized, _ := normalizeCriterionAssessment(spec)
	return normalized
}

func validateLocalReadTarget(target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("%w: local_read assessment requires a target", ErrMalformedControl)
	}
	if strings.HasPrefix(target, "/") || strings.HasPrefix(target, "\\") || strings.Contains(target, ":") {
		return fmt.Errorf("%w: local_read assessment target must be workspace-relative", ErrMalformedControl)
	}
	parts := strings.FieldsFunc(target, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) == 0 {
		return fmt.Errorf("%w: local_read assessment target is empty", ErrMalformedControl)
	}
	for _, part := range parts {
		if part == ".." || part == "." {
			return fmt.Errorf("%w: local_read assessment target must be literal", ErrMalformedControl)
		}
	}
	return nil
}

func containsUnsafeAssessmentTarget(target string) bool {
	return strings.ContainsAny(target, "*?[]{};|&$`<>\x00\n\r") ||
		strings.Contains(target, "://")
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
			out = append(out, cloneCriterion(criterion))
		}
	}
	return out
}

func cloneCriterion(criterion Criterion) Criterion {
	if criterion.Assessment != nil {
		assessment := *criterion.Assessment
		criterion.Assessment = &assessment
	}
	return criterion
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

// ApplyDeterministicCriteria resolves criteria that runtime observations
// alone can prove — today only an atomic exact-read criterion whose
// delivered windows cover the whole file (see evaluateCriterion). It never
// marks anything failed, never touches a criterion already resolved, and
// ignores executor prose; every other criterion is left to the verifier.
func (r *AgentRun) ApplyDeterministicCriteria(execution ExecutionResult, cycle int) {
	if r == nil {
		return
	}
	for i := range r.Criteria {
		criterion := &r.Criteria[i]
		if criterion.Status == CriterionSatisfied || criterion.Status == CriterionNotApplicable {
			continue
		}
		if matched, passed, note := evaluateCriterion(*criterion, execution); matched && passed {
			criterion.Status = CriterionSatisfied
			criterion.Note = truncate(note, 256)
			criterion.UpdatedCycle = cycle
		}
	}
}

// evaluateCriterion reports whether runtime observations prove criterion.
// A contract model occasionally emits only "Read the file X" for a larger
// request. That exact, atomic criterion is mechanically proven once the
// delivered read windows for that target, unioned within one consistent
// source version, cover the whole file — not merely that some read_file call
// touching it succeeded (a narrow windowed read of a large file previously
// satisfied this criterion without the model ever having seen most of the
// content; see harness plan §4 finding #3). Every other criterion needs the
// verifier's judgment.
func evaluateCriterion(criterion Criterion, execution ExecutionResult) (matched, passed bool, note string) {
	if criterion.Kind != "" && criterion.Kind != CriterionSemantic {
		return false, false, ""
	}
	if target, ok := ExactReadCriterionTarget(criterion.Text); ok && readCoverageComplete(target, execution.ReadObservations) {
		return true, true, "observed full read coverage"
	}
	return false, false, ""
}

// readInterval is a half-open-free, 1-based inclusive [start, end] delivered
// line range used only by readCoverageComplete's local interval merge.
type readInterval struct{ start, end int64 }

// CanonicalTarget is the single comparison form for a read target, shared
// by exact-read criterion targets and delivered read observations so the
// two always compare in one spelling (audit P2-5: "./src/x.go" never matched
// a criterion naming "src/x.go"). It lowercases (the historical rule on both
// sides), converts backslashes to slashes, cleans the path, and drops a
// leading "./". It is purely lexical — no filesystem access; making an
// absolute in-workspace path relative is the caller's job. A target that
// is empty or escapes upward ("..") returns "" and never matches anything.
func CanonicalTarget(target string) string {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return ""
	}
	target = path.Clean(strings.ReplaceAll(target, "\\", "/"))
	target = strings.TrimPrefix(target, "./")
	if target == "." || target == ".." || strings.HasPrefix(target, "../") {
		return ""
	}
	return target
}

// sameReadTarget reports whether two targets name the same resource in
// CanonicalTarget form.
func sameReadTarget(a, b string) bool {
	ca := CanonicalTarget(a)
	return ca != "" && ca == CanonicalTarget(b)
}

// ReadCoverage reports target's contiguous delivered line coverage from line
// 1 and whether it rests on one consistent source version with a known
// total. It is the monotonic progress measure for exact-read obligations.
func ReadCoverage(target string, observations []ReadObservation) (covered int64, ok bool) {
	covered, _, haveTotal, consistent := readCoverageState(target, observations)
	return covered, consistent && haveTotal
}

// readCoverageComplete reports whether target's ReadObservations, unioned
// within one consistent source version, span the whole known file — the
// coverage-aware replacement for "some call touching this path succeeded"
// (harness plan §4 finding #3, §10 "Ordered evidence freshness
// prerequisite"). It is deliberately conservative: no observations, an
// unknown total line count, a gap between delivered windows, or observations
// split across more than one non-empty source digest for the same target all
// return false — mixing two versions of a file, or trusting a partial view,
// must never be reported as complete coverage.
func readCoverageComplete(target string, observations []ReadObservation) bool {
	covered, total, haveTotal, consistent := readCoverageState(target, observations)
	if !consistent || !haveTotal {
		return false // no stable version/total established: never a whole-file claim
	}
	if total == 0 {
		return true // an empty file has nothing left to cover once its size is known
	}
	return covered >= total
}

// readCoverageState computes target's contiguous line coverage from line 1
// (covered), its known total line count (total, haveTotal), and whether
// every relevant observation agreed on source version and total (consistent
// — false the instant two different non-empty digests, or two different
// non-nil TotalLines, are seen for the same target; mixing versions must
// never contribute to a coverage claim). It underlies both
// readCoverageComplete and NextReadOffset so the two can never disagree
// about what "covered so far" means.
func readCoverageState(target string, observations []ReadObservation) (covered, total int64, haveTotal, consistent bool) {
	consistent = true
	if CanonicalTarget(target) == "" {
		return 0, 0, false, false
	}
	var digest string
	var intervals []readInterval
	for _, ob := range observations {
		if !sameReadTarget(ob.Target, target) {
			continue
		}
		if ob.SourceDigest != "" {
			if digest == "" {
				digest = ob.SourceDigest
			} else if digest != ob.SourceDigest {
				consistent = false
			}
		}
		if ob.TotalLines != nil {
			if haveTotal && *ob.TotalLines != total {
				consistent = false
			}
			total, haveTotal = *ob.TotalLines, true
		}
		if ob.StartLine > 0 && ob.EndLine >= ob.StartLine {
			intervals = append(intervals, readInterval{ob.StartLine, ob.EndLine})
		}
	}
	if !consistent {
		return 0, total, haveTotal, false
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
	for _, iv := range intervals {
		if iv.start > covered+1 {
			break // gap: stop at the contiguous frontier from line 1
		}
		if iv.end > covered {
			covered = iv.end
		}
	}
	return covered, total, haveTotal, true
}

// NextReadOffset reports the next 1-based line to request for target, given
// its currently observed coverage, so a controller-owned continuation
// directive can name a precise read_file offset/limit (harness plan §9's
// example, §12) instead of a vague "use the offered tool again". ok is false
// when no useful hint can be derived — no observations yet, mixed source
// versions/totals, or coverage is already complete — the caller falls back
// to its own generic phrasing in that case. limit spans only the first gap,
// stopping before an already observed window. The adapter must additionally
// cap it at its read tool's maximum page size.
func NextReadOffset(target string, observations []ReadObservation) (offset, limit int64, ok bool) {
	covered, total, haveTotal, consistent := readCoverageState(target, observations)
	if !consistent || !haveTotal || total <= 0 || covered >= total {
		return 0, 0, false
	}
	offset, limit = covered+1, total-covered
	for _, ob := range observations {
		if sameReadTarget(ob.Target, target) && ob.StartLine > offset && ob.EndLine >= ob.StartLine {
			limit = min(limit, ob.StartLine-offset)
		}
	}
	return offset, limit, true
}

// ReadCoverageProgressDigest fingerprints delivered coverage of exact-read
// criteria. Duplicate windows, reordered receipts, and sequence numbers do
// not change it. Unknown totals and mixed source versions cannot count as
// progress. The checkpoint owns the previous digest; no second ledger is used.
func ReadCoverageProgressDigest(criteria []Criterion, observations []ReadObservation) string {
	h := sha256.New()
	haveCoverage := false
	for _, criterion := range criteria {
		target, ok := ExactReadCriterionTarget(criterion.Text)
		if !ok || (criterion.Kind != "" && criterion.Kind != CriterionSemantic) {
			continue
		}
		_, total, known, consistent := readCoverageState(target, observations)
		if !consistent {
			return ""
		}
		if !known {
			continue
		}
		var intervals []readInterval
		for _, ob := range observations {
			if sameReadTarget(ob.Target, target) && ob.StartLine > 0 && ob.EndLine >= ob.StartLine {
				intervals = append(intervals, readInterval{ob.StartLine, min(ob.EndLine, total)})
			}
		}
		sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
		fmt.Fprintf(h, "%q:%d:", criterion.ID, total)
		var start, end int64
		for _, iv := range intervals {
			if iv.start > iv.end {
				continue
			}
			if start == 0 {
				start, end = iv.start, iv.end
			} else if iv.start <= end+1 {
				end = max(end, iv.end)
			} else {
				fmt.Fprintf(h, "%d-%d;", start, end)
				start, end = iv.start, iv.end
			}
		}
		fmt.Fprintf(h, "%d-%d;", start, end)
		haveCoverage = haveCoverage || end > 0 || total == 0
	}
	if !haveCoverage {
		return ""
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// AppendReadObservation records one delivered read window in execution's
// bounded coverage receipts. Windows are kept per target, not as a plain
// append log (audit P2-5): a window overlapping or adjacent to an existing
// one for the same target and source version is merged into it, so rereads
// and paging never consume retention; a window from a newer source version
// (a different non-empty digest) supersedes that target's older-version
// windows, whose coverage no longer describes the file. When more than
// MaxReadObservations windows remain, the whole target least recently read
// is dropped — never a single window out of a target still being read,
// which previously re-opened gaps in files already fully covered. Only if
// one target alone exceeds the bound is its oldest window dropped. Each
// stored window's Sequence is the monotonic receipt order of its latest
// contribution.
func AppendReadObservation(execution *ExecutionResult, ob ReadObservation) {
	if execution == nil {
		return
	}
	if canonical := CanonicalTarget(ob.Target); canonical != "" {
		ob.Target = canonical
	}
	next := 1
	for _, existing := range execution.ReadObservations {
		next = max(next, existing.Sequence+1)
	}
	kept := execution.ReadObservations[:0:0]
	for _, existing := range execution.ReadObservations {
		if !sameReadTarget(existing.Target, ob.Target) {
			kept = append(kept, existing)
			continue
		}
		if ob.SourceDigest != "" && existing.SourceDigest != "" && existing.SourceDigest != ob.SourceDigest {
			continue // superseded by a newer version of the same source
		}
		if existing.SourceDigest == ob.SourceDigest && windowsTouch(existing, ob) {
			ob.StartLine = min(ob.StartLine, existing.StartLine)
			ob.EndLine = max(ob.EndLine, existing.EndLine)
			if ob.TotalLines == nil {
				ob.TotalLines = existing.TotalLines
			}
			continue // merged into ob
		}
		kept = append(kept, existing)
	}
	ob.Sequence = next
	kept = append(kept, ob)
	for len(kept) > MaxReadObservations {
		kept = evictReadObservations(kept)
	}
	execution.ReadObservations = kept
}

// windowsTouch reports whether two line windows overlap or are adjacent.
// A window without a valid line range never merges.
func windowsTouch(a, b ReadObservation) bool {
	if a.StartLine <= 0 || a.EndLine < a.StartLine || b.StartLine <= 0 || b.EndLine < b.StartLine {
		return false
	}
	return a.StartLine <= b.EndLine+1 && b.StartLine <= a.EndLine+1
}

// evictReadObservations drops every window of the target whose most recent
// read is oldest. If that would remove the target of the newest window (a
// single target exceeds the bound on its own), it drops only the oldest
// window instead.
func evictReadObservations(obs []ReadObservation) []ReadObservation {
	latest := map[string]int{}
	for _, ob := range obs {
		key := CanonicalTarget(ob.Target)
		latest[key] = max(latest[key], ob.Sequence)
	}
	victim, oldest := "", 0
	for key, seq := range latest {
		if victim == "" && oldest == 0 || seq < oldest || (seq == oldest && key < victim) {
			victim, oldest = key, seq
		}
	}
	newest := CanonicalTarget(obs[len(obs)-1].Target)
	out := obs[:0:0]
	if victim == newest {
		oldestIndex := 0
		for i, ob := range obs {
			if ob.Sequence < obs[oldestIndex].Sequence {
				oldestIndex = i
			}
		}
		return append(append(out, obs[:oldestIndex]...), obs[oldestIndex+1:]...)
	}
	for _, ob := range obs {
		if CanonicalTarget(ob.Target) != victim {
			out = append(out, ob)
		}
	}
	return out
}

// ExactReadCriterionTarget extracts the target path from a criterion
// recognized as an atomic "read this exact file" instruction, shared so both
// evaluateCriterion's deterministic proof and Phase 2's yield-eligibility
// check (agent-execution-harness plan §10) recognize the identical narrow
// grammar — one before any tool call has proven it, the other after. ok is
// false for anything not exactly this shape: multi-step or ambiguous prose
// ("inspect", "review", "compare"), a quoted shell fragment, or a criterion
// naming no target at all remain semantic.
//
// Two surface forms are recognized:
//   - Natural language: "Read [the/file/named/entire/content of/contents of]
//     <path>.", with the bracketed filler words stripped in any order/
//     repetition — see the "entire content of" case below.
//   - Compact shorthand: "read_files:<path>" or "read_file:<path>", observed
//     in the wild from a contract-establishing model (gemma-4-e4b via
//     LM Studio) that consistently produces this machine-readable style
//     instead of a sentence for otherwise-identical requests — see
//     docs/architecture/decisions/0013-agent-execution-yield-policy.md's
//     Phase 4d update. Only one target per criterion is recognized; a
//     shorthand value naming more than one path (a comma, say) simply never
//     matches a real Call.Path later and the criterion stays semantic,
//     exactly like any other unrecognized shape — never a false positive.
func ExactReadCriterionTarget(text string) (target string, ok bool) {
	text = strings.ToLower(strings.TrimSpace(strings.TrimRight(text, ".")))
	for _, prefix := range []string{"read_files:", "read_file:"} {
		if rest, matched := strings.CutPrefix(text, prefix); matched {
			if rest = CanonicalTarget(rest); rest == "" {
				return "", false
			}
			return rest, true
		}
	}
	if !strings.HasPrefix(text, "read ") {
		return "", false
	}
	text = strings.TrimSpace(strings.TrimPrefix(text, "read "))
	// Strip filler words to a fixpoint, not just one pass: a real contract
	// model produced "Read the entire content of readings_a.txt." (observed
	// in the wild), where "the " alone left "entire content of
	// readings_a.txt" as a garbled target that could never match a real
	// Call.Path — so the coverage-aware proof stayed permanently unreachable
	// even after the file was genuinely read in full. Repeating the strip
	// handles any order/combination of this closed, evidence-driven filler
	// vocabulary without needing a fixed phrase order.
	for changed := true; changed; {
		changed = false
		for _, prefix := range []string{"the ", "file ", "named ", "entire ", "content of ", "contents of "} {
			if strings.HasPrefix(text, prefix) {
				text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
				changed = true
			}
		}
	}
	for _, suffix := range []string{" in full", " completely", " fully"} {
		text = strings.TrimSpace(strings.TrimSuffix(text, suffix))
	}
	if len(text) >= 2 && ((text[0] == '`' && text[len(text)-1] == '`') || (text[0] == '"' && text[len(text)-1] == '"')) {
		text = text[1 : len(text)-1]
	}
	if text = CanonicalTarget(text); text == "" {
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

// CriterionFact is one controller-computed, mechanical observation about a
// pinned criterion — never model prose, and never a status. It exists so
// the executor and the verifier can see, before verification, what the
// runtime has already observed toward a criterion (audit P2-6, limitations
// A/H), without the controller making any semantic judgment.
type CriterionFact struct {
	CriterionID string
	Fact        string
}

// CriterionReadFacts reports delivered read coverage for each unresolved
// atomic exact-read criterion. Only this narrow grammar gets facts: linking
// arbitrary evidence to free-text criteria would require the semantic
// judgment that belongs to the verifier. The target in each fact is the
// criterion's own pinned, canonical target text.
func (r *AgentRun) CriterionReadFacts(execution ExecutionResult) []CriterionFact {
	if r == nil {
		return nil
	}
	var facts []CriterionFact
	for _, criterion := range r.UnresolvedCriteria() {
		if criterion.Kind != "" && criterion.Kind != CriterionSemantic {
			continue
		}
		target, ok := ExactReadCriterionTarget(criterion.Text)
		if !ok {
			continue
		}
		seen := false
		for _, ob := range execution.ReadObservations {
			if strings.EqualFold(strings.TrimSpace(ob.Target), target) {
				seen = true
				break
			}
		}
		covered, total, haveTotal, consistent := readCoverageState(target, execution.ReadObservations)
		var fact string
		switch {
		case !seen:
			fact = fmt.Sprintf("no delivered read of %q yet this cycle", target)
		case !consistent:
			fact = fmt.Sprintf("delivered reads of %q span different file versions; coverage is not established", target)
		case haveTotal && covered >= total:
			fact = fmt.Sprintf("all %d lines of %q delivered", total, target)
		case haveTotal:
			fact = fmt.Sprintf("lines 1-%d of %d of %q delivered contiguously; the rest is not yet read", covered, total, target)
		default:
			fact = fmt.Sprintf("lines 1-%d of %q delivered; total line count not yet known", covered, target)
		}
		facts = append(facts, CriterionFact{CriterionID: criterion.ID, Fact: truncate(fact, 256)})
	}
	return facts
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
