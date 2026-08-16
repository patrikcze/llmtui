package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

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
// fingerprint on the next round.
func (p toolBatchPlan) mergeResults(executed []tools.Result) (merged, observed []tools.Result) {
	merged = make([]tools.Result, 0, len(p.calls))
	observed = make([]tools.Result, 0, len(executed))
	executedIndex := 0
	for i, call := range p.calls {
		if reason := p.blocked[i]; reason != "" {
			merged = append(merged, tools.Result{
				Call: call,
				Err: fmt.Errorf(
					"%s. Use different arguments, a different approach, or report the observable state",
					reason,
				),
			})
			continue
		}
		if executedIndex >= len(executed) {
			merged = append(merged, tools.Result{
				Call: call,
				Err:  fmt.Errorf("tool result missing for accepted call; it was not reported as completed"),
			})
			continue
		}
		result := executed[executedIndex]
		executedIndex++
		result.Call = call
		merged = append(merged, result)
		observed = append(observed, result)
	}
	return merged, observed
}

// defaultProgressThreshold matches agent.max_repeated_failures' default so
// the two related "how many times before we stop" knobs stay consistent
// for anyone reasoning about run behavior.
const defaultProgressThreshold = 3

func newProgressLedger(threshold int) *progressLedger {
	if threshold <= 0 {
		threshold = defaultProgressThreshold
	}
	return &progressLedger{threshold: threshold, entries: make(map[string]*progressEntry)}
}

// fingerprint canonicalizes a tool call into a stable key: tool identity
// plus the resource it actually acts on, not the raw call text. Two calls
// that differ only in incidental formatting collide to the same
// fingerprint; two calls to a genuinely different resource do not.
func progressFingerprint(c tools.Call) string {
	if c.InputErr != "" {
		return strings.Join([]string{c.Tool, "invalid", digestText(c.InputErr)}, "\x1f")
	}
	if strings.TrimSpace(c.MCPServer) != "" {
		return strings.Join([]string{"mcp", c.MCPServer, c.MCPTool, canonicalizeArgs(c.MCPArgs)}, "\x1f")
	}
	resource := strings.TrimSpace(c.Path)
	switch c.Tool {
	case tools.ToolWebSearch:
		resource = normalizeText(c.Body) + "\x1e" + strconv.Itoa(c.Max)
	case tools.ToolRunCommand:
		// Shell whitespace can be semantic inside quoted arguments. Only trim
		// the block edges; do not collapse or case-fold the command itself.
		resource = strings.TrimSpace(c.Body)
	case tools.ToolGlob, tools.ToolGrep:
		resource = strings.Join([]string{resource, strings.TrimSpace(c.Body), strings.TrimSpace(c.Filter)}, "\x1e")
	case tools.ToolWriteFile:
		resource += "\x1e" + digestText(c.Body)
	case tools.ToolWebFetch:
		resource = normalizeURL(c.Path)
	default:
		if resource == "" {
			resource = normalizeText(c.Body)
		}
	}
	return strings.Join([]string{c.Tool, resource}, "\x1f")
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
func progressDigest(r tools.Result) string {
	h := sha256.New()
	if r.Err != nil {
		h.Write([]byte("err:"))
		h.Write([]byte(r.Err.Error()))
	} else {
		h.Write([]byte("ok:"))
		h.Write([]byte(r.Output))
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
		if l.wouldBlock(progressFingerprint(call)) {
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
		l.observe(progressFingerprint(r.Call), progressDigest(r))
	}
}
