package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/tools"
)

// progressLedger tracks tool-call fingerprints across a run so repeated,
// evidence-unchanged calls can be blocked before execution, including when
// they travel alongside fresh calls in a mixed batch. It is the fix for the confirmed gap in
// docs/architecture/v1-audit.md §4.1: no tool-call-level repeated-call or
// no-progress detection existed anywhere in the codebase, in either the
// ordinary tool loop or /agent on. See
// docs/architecture/decisions/0002-live-progress-ledger-and-budget-enforcement.md
// and docs/architecture/v1-agent-runtime.md §3 for the design.
type progressLedger struct {
	threshold int
	entries   map[string]*progressEntry
	root      string
	// blockedStreak counts consecutive rounds where the entire incoming
	// batch was already blocked. A single block is a forcing function —
	// the model gets a chance to change strategy (master-prompt §7.2) —
	// but if the very next round repeats the same blocked pattern instead
	// of adapting, continuing to ask the provider again would reproduce
	// the exact token-burn complaint the ledger exists to prevent, just
	// without the underlying tool actually executing. blockedStreakLimit
	// bounds that.
	blockedStreak int
}

// blockedStreakLimit is deliberately small: one block to signal "try
// something else," a second identical block to conclude the model isn't
// going to.
const blockedStreakLimit = 2

type progressEntry struct {
	repeats    int // consecutive completions with an unchanged result digest
	lastDigest string
}

// toolBatchPlan is an immutable, position-based execution plan. Blocked
// slots receive synthetic results; runnable slots execute in their original
// order. Position, not call ID, is authoritative because fenced-protocol
// calls do not carry IDs.
type toolBatchPlan struct {
	calls   []tools.Call
	blocked []string
}

func newToolBatchPlan(calls []tools.Call) toolBatchPlan {
	return toolBatchPlan{
		calls:   append([]tools.Call{}, calls...),
		blocked: make([]string, len(calls)),
	}
}

func (p *toolBatchPlan) block(index int, reason string) {
	if index >= 0 && index < len(p.blocked) {
		p.blocked[index] = reason
	}
}

func (p toolBatchPlan) blockedCount() int {
	count := 0
	for _, reason := range p.blocked {
		if reason != "" {
			count++
		}
	}
	return count
}

func (p toolBatchPlan) runnableCalls() []tools.Call {
	out := make([]tools.Call, 0, len(p.calls)-p.blockedCount())
	for i, call := range p.calls {
		if p.blocked[i] == "" {
			out = append(out, call)
		}
	}
	return out
}

// mergeResults restores one result per original call and separately returns
// only real execution outcomes for progress observation. Observing synthetic
// block errors would change the digest and accidentally re-enable a stuck
// fingerprint on the next round. statuses is aligned with merged and
// classifies each slot for receipt/evidence purposes (see
// internal/agent.ActionStatus): a ledger block is ActionBlocked, an accepted
// call with no correlated result is ActionUnknown — genuinely unknown
// whether it ran, never assumed to have failed or succeeded — and everything
// else is ActionExecuted.
func (p toolBatchPlan) mergeResults(executed []tools.Result) (merged, observed []tools.Result, statuses []agent.ActionStatus) {
	merged = make([]tools.Result, 0, len(p.calls))
	observed = make([]tools.Result, 0, len(executed))
	statuses = make([]agent.ActionStatus, 0, len(p.calls))
	executedIndex := 0
	for i, call := range p.calls {
		if reason := p.blocked[i]; reason != "" {
			err := fmt.Errorf(
				"%s. Use different arguments, a different approach, or report the observable state",
				reason,
			)
			merged = append(merged, tools.Result{
				Call: call,
				Err:  err,
				// A ledger block never reaches a producer, so its outcome is
				// unknown — paired with agent.ActionBlocked, which is what
				// actually records "the controller withheld execution."
				Meta: tools.ResultMeta{
					Outcome: tools.OutcomeUnknown, Effect: tools.EffectUnknown,
					Error: &tools.ErrorInfo{Code: "repeat_block", Retry: tools.RetryCorrectInput, Message: err.Error()},
				},
			})
			statuses = append(statuses, agent.ActionBlocked)
			continue
		}
		if executedIndex >= len(executed) {
			err := fmt.Errorf("tool result missing for accepted call; it was not reported as completed")
			merged = append(merged, tools.Result{
				Call: call,
				Err:  err,
				Meta: tools.ResultMeta{
					Outcome: tools.OutcomeUnknown, Effect: tools.EffectUnknown,
					Error: &tools.ErrorInfo{Code: "outcome_unknown", Retry: tools.RetryLater, Message: err.Error()},
				},
			})
			statuses = append(statuses, agent.ActionUnknown)
			continue
		}
		result := executed[executedIndex]
		executedIndex++
		result.Call = call
		merged = append(merged, result)
		observed = append(observed, result)
		statuses = append(statuses, agent.ActionExecuted)
	}
	return merged, observed, statuses
}

// defaultProgressThreshold matches agent.max_repeated_failures' default so
// the two related "how many times before we stop" knobs stay consistent
// for anyone reasoning about run behavior.
const defaultProgressThreshold = 3

func newProgressLedger(threshold int, roots ...string) *progressLedger {
	if threshold <= 0 {
		threshold = defaultProgressThreshold
	}
	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}
	return &progressLedger{threshold: threshold, entries: make(map[string]*progressEntry), root: root}
}

func (m *Model) progressRoot() string {
	if m.toolRunner == nil {
		return ""
	}
	return m.toolRunner.Root()
}

// fingerprint canonicalizes a tool call into a stable key: tool identity
// plus the resource it actually acts on, not the raw call text. Two calls
// that differ only in incidental formatting collide to the same
// fingerprint; two calls to a genuinely different resource do not.
func progressFingerprint(c tools.Call) string {
	return progressFingerprintAtRoot("", c)
}

func progressFingerprintAtRoot(root string, c tools.Call) string {
	if c.InputErr != "" {
		return strings.Join([]string{c.Tool, "invalid", digestText(c.InputErr)}, "\x1f")
	}
	if strings.TrimSpace(c.MCPServer) != "" {
		return strings.Join([]string{"mcp", c.MCPServer, c.MCPTool, canonicalizeArgs(c.MCPArgs)}, "\x1f")
	}
	resource := strings.TrimSpace(c.Path)
	switch c.Tool {
	case tools.ToolWebSearch:
		resource = normalizeText(c.Body) + "\x1e" + strconv.Itoa(c.Max) + "\x1e" + strings.TrimSpace(c.Freshness)
	case tools.ToolRunCommand:
		if canonical, ok := tools.CanonicalReadOnlyCommandIdentity(c.Body, root); ok {
			resource = "read-only\x1e" + canonical
		} else {
			resource = "opaque\x1e" + strings.TrimSpace(c.Body)
		}
	case tools.ToolGlob, tools.ToolGrep:
		resource = strings.Join([]string{
			normalizeWorkspacePath(root, resource), strings.TrimSpace(c.ResourceID),
			normalizeText(c.Body), strings.TrimSpace(c.Filter),
			strconv.FormatBool(c.SearchLiteral), searchCaseIdentity(c.SearchCaseSensitive),
			strconv.Itoa(c.SearchContext), strconv.Itoa(c.SearchLimit), strings.TrimSpace(c.SearchCursor),
		}, "\x1e")
	case tools.ToolWriteFile:
		resource = normalizeWorkspacePath(root, resource) + "\x1e" + digestText(c.Body) + "\x1e" + strings.TrimSpace(c.ExpectedResourceID)
	case tools.ToolEditFile:
		resource = normalizeWorkspacePath(root, resource) + "\x1e" + digestText(c.OldText) + "\x1e" + digestText(c.NewText) + "\x1e" + strings.TrimSpace(c.ExpectedResourceID)
	case tools.ToolWebFetch:
		resource = normalizeURL(c.Path) + "\x1e" + strings.TrimSpace(c.Freshness) + "\x1e" + strings.TrimSpace(c.WebCacheMode) + "\x1e" + strconv.Itoa(c.WebCacheMaxAge) + "\x1e" + strings.TrimSpace(c.WebRefreshEpoch)
	case tools.ToolReadFile:
		// A different line range is a different operation, so paginating
		// through a file is never mistaken for a repeated no-progress call.
		start, count, ranged := tools.CanonicalReadRange(c.Offset, c.Limit)
		if strings.TrimSpace(c.ResourceID) != "" {
			resource = "resource:" + strings.TrimSpace(c.ResourceID)
		} else {
			resource = normalizeWorkspacePath(root, resource)
		}
		if ranged {
			resource += "\x1e" + strconv.Itoa(start) + "\x1e" + strconv.Itoa(count)
		}
		if c.ByteOffset != nil {
			resource += "\x1ebyte\x1e" + strconv.FormatInt(*c.ByteOffset, 10)
		}
	case tools.ToolListDir:
		resource = strings.Join([]string{normalizeWorkspacePath(root, resource), strconv.Itoa(c.SearchLimit), strings.TrimSpace(c.SearchCursor)}, "\x1e")
	case tools.ToolSearch:
		resource = normalizeText(c.SearchQuery) + "\x1e" + strconv.Itoa(c.Max)
	default:
		if resource == "" {
			resource = normalizeText(c.Body)
		}
	}
	return strings.Join([]string{c.Tool, resource}, "\x1f")
}

func searchCaseIdentity(v *bool) string {
	if v == nil {
		return "default"
	}
	return strconv.FormatBool(*v)
}

func normalizeWorkspacePath(root, raw string) string {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw)))
	if clean == "." && strings.TrimSpace(raw) == "" {
		return ""
	}
	if root == "" {
		return filepath.ToSlash(clean)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return filepath.ToSlash(clean)
	}
	candidate := clean
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	candidate = filepath.Clean(candidate)
	if resolved, resolveErr := filepath.EvalSymlinks(candidate); resolveErr == nil {
		candidate = resolved
	}
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "outside:" + filepath.ToSlash(candidate)
	}
	return filepath.ToSlash(filepath.Clean(rel))
}

func normalizeText(s string) string {
	return strings.ToLower(normalizeWhitespace(s))
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func normalizeURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	parsed.RawQuery = parsed.Query().Encode()
	return parsed.String()
}

func canonicalizeArgs(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return trimmed
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return trimmed
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return trimmed
	}
	return string(canonical)
}

func digestText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// digest summarizes a completed call's observable outcome: the honest
// "did anything actually change" signal the ledger resets a repeat streak
// on. An error is part of the digest, deliberately, so a command failing
// the same way twice in a row is recognized as no new evidence, while a
// command whose error message changes (different line, different reason)
// is not treated as a repeat.
//
// A Phase 1a-adapted producer's typed Meta (Meta.Outcome != "") is digested
// instead of the formatted text — see progressDigestFromMeta. This is the
// fix for plan §11 rule 9/§20's "changing call ID/time/resource ID is
// irrelevant" progress rule: formatted Output or Err text can carry volatile
// content a producer never intended as evidence (a personal_apps result's
// ObservedAt timestamp, an MCP response echoing a request id), which made
// the old text digest change on every call and defeat repeat detection for
// those producers entirely. The typed digest below carries none of that —
// only stable operation shape (Outcome, Effect, Error.Code/Retry, Coverage
// counts/reasons, Window coordinates, ContentDigest) — so a call repeated
// with no new evidence still digests identically even when its formatted
// text differs only in a timestamp or id. A Result whose Meta was never
// populated (Meta.Outcome == "" — see tools.Result's doc comment) falls back
// to the legacy formatted-text digest unchanged.
func progressDigest(r tools.Result) string {
	if r.Meta.Outcome != "" {
		return progressDigestFromMeta(r.Meta)
	}
	h := sha256.New()
	if r.Err != nil {
		h.Write([]byte("err:"))
		h.Write([]byte(r.Err.Error()))
		h.Write([]byte("\x00detail:"))
		h.Write([]byte(r.Output))
	} else {
		h.Write([]byte("ok:"))
		h.Write([]byte(r.Output))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// progressDigestFromMeta hashes only stable, typed operation shape — never a
// call ID, timestamp, or any other formatted text. Two calls whose Meta is
// otherwise identical digest identically regardless of what varies in their
// raw Output/Err text; two calls whose Meta genuinely differs (a changed
// Outcome, a changed Coverage count, a different Window) digest differently.
func progressDigestFromMeta(m tools.ResultMeta) string {
	h := sha256.New()
	fmt.Fprintf(h, "outcome:%s\x1feffect:%s\x1fprecondition:%s\x1freused:%t", m.Outcome, m.Effect, m.Precondition, m.Reused)
	if m.Error != nil {
		fmt.Fprintf(h, "\x1fcode:%s\x1fretry:%s", m.Error.Code, m.Error.Retry)
	}
	cov := m.Coverage
	fmt.Fprintf(h, "\x1fcoverage:%t,%t,%t,%d,%d",
		cov.SourceComplete, cov.CaptureComplete, cov.PreviewComplete, cov.ObservedBytes, cov.RetainedBytes)
	if cov.TotalBytes != nil {
		fmt.Fprintf(h, ",total_bytes=%d", *cov.TotalBytes)
	}
	if cov.TotalLines != nil {
		fmt.Fprintf(h, ",total_lines=%d", *cov.TotalLines)
	}
	for _, reason := range cov.Reasons {
		fmt.Fprintf(h, ",%s", reason)
	}
	sc := m.Search
	fmt.Fprintf(h, "\x1fsearch:%d,%d,%t,%d,%d,%d", sc.MatchesReturned, sc.MatchesCaptured, sc.TextComplete, sc.FilesEligible, sc.FilesScanned, sc.SourceBytes)
	if sc.MatchesTotal != nil {
		fmt.Fprintf(h, ",matches_total=%d", *sc.MatchesTotal)
	}
	keys := make([]string, 0, len(sc.Skipped))
	for reason := range sc.Skipped {
		keys = append(keys, reason)
	}
	sort.Strings(keys)
	for _, reason := range keys {
		fmt.Fprintf(h, ",skip=%s:%d", reason, sc.Skipped[reason])
	}
	if w := m.Window; w != nil {
		fmt.Fprintf(h, "\x1fwindow:%d,%d,%d,%d,%t", w.StartLine, w.EndLine, w.StartByte, w.EndByte, w.PartialLine)
		if w.NextOffset != nil {
			fmt.Fprintf(h, ",next_offset=%d", *w.NextOffset)
		}
		if w.NextCursor != "" {
			fmt.Fprintf(h, ",next_cursor=%s", w.NextCursor)
		}
	}
	if m.ContentDigest != "" {
		fmt.Fprintf(h, "\x1fcontent_digest:%s", m.ContentDigest)
	}
	if m.SourceDigest != "" {
		fmt.Fprintf(h, "\x1fsource_digest:%s", m.SourceDigest)
	}
	if m.FileVersion != nil {
		fmt.Fprintf(h, "\x1ffile_version:%s,%s,%d,%t", m.FileVersion.Path, m.FileVersion.Digest, m.FileVersion.SizeBytes, m.FileVersion.Complete)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// wouldBlock reports whether a fresh call at this fingerprint has already
// exceeded the repeat threshold, based only on previously observed
// completions — it does not itself count as an attempt.
func (l *progressLedger) wouldBlock(fingerprint string) bool {
	e, ok := l.entries[fingerprint]
	return ok && e.repeats >= l.threshold
}

// observe records a completed call's outcome. A digest matching the last
// one seen for this fingerprint extends the repeat streak (no new
// evidence); a different digest — a changed result, advanced pagination, a
// transient failure that then succeeds — resets it, so legitimate
// repetition (polling, freshness, retries) is never penalized.
func (l *progressLedger) observe(fingerprint, digest string) {
	e, ok := l.entries[fingerprint]
	if !ok {
		l.entries[fingerprint] = &progressEntry{repeats: 1, lastDigest: digest}
		return
	}
	if e.lastDigest == digest {
		e.repeats++
	} else {
		e.repeats = 1
		e.lastDigest = digest
	}
}

// blockBatch reports whether every call in the batch is already blocked,
// and if so, why, and whether this is the streak-limit-th consecutive
// fully-blocked batch (terminal — see blockedStreakLimit).
func (l *progressLedger) blockBatch(calls []tools.Call) (blocked, terminal bool, reason string) {
	plan, terminal := l.planBatch(calls)
	return len(calls) > 0 && plan.blockedCount() == len(calls), terminal, progressBlockReason(plan)
}

// planBatch classifies every call before approvals or execution. Mixed
// batches keep fresh calls runnable while marking only stuck calls blocked.
// The terminal streak remains batch-level: it advances only when every slot
// is blocked, so a batch that can still produce evidence is never terminal.
func (l *progressLedger) planBatch(calls []tools.Call) (toolBatchPlan, bool) {
	plan := newToolBatchPlan(calls)
	if len(calls) == 0 {
		return plan, false
	}
	const reason = "repeated tool call blocked: no new evidence since the last identical call"
	for i, call := range calls {
		if call.Tool == tools.ToolLocalContext {
			continue
		}
		if l.wouldBlock(progressFingerprintAtRoot(l.root, call)) {
			plan.block(i, reason)
		}
	}
	if plan.blockedCount() != len(calls) {
		l.blockedStreak = 0
		return plan, false
	}
	l.blockedStreak++
	return plan, l.blockedStreak >= blockedStreakLimit
}

func progressBlockReason(plan toolBatchPlan) string {
	for _, reason := range plan.blocked {
		if reason != "" {
			return reason
		}
	}
	return ""
}

// observeResults updates the ledger from a batch of completed results. Call
// this once real execution finishes, for both the ordinary tool loop and
// /agent on — they share this ledger via the shared kernel (ADR 0001).
func (l *progressLedger) observeResults(results []tools.Result) {
	for _, r := range results {
		l.observe(progressFingerprintAtRoot(l.root, r.Call), progressDigest(r))
	}
}
