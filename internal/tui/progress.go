package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/patrikcze/llmtui/internal/tools"
)

// progressLedger tracks tool-call fingerprints across a run so a batch that
// only repeats already-seen, evidence-unchanged calls can be blocked
// instead of executed indefinitely. It is the fix for the confirmed gap in
// docs/architecture/v1-audit.md §4.1: no tool-call-level repeated-call or
// no-progress detection existed anywhere in the codebase, in either the
// ordinary tool loop or /agent on. See
// docs/architecture/decisions/0002-live-progress-ledger-and-budget-enforcement.md
// and docs/architecture/v1-agent-runtime.md §3 for the design.
//
// Scope, deliberately narrower than a per-call filter: blockBatch only
// blocks when EVERY call in an incoming batch already has a no-new-evidence
// fingerprint at or past the threshold. The failure mode this fixes
// (docs/architecture/LLMTUI_V1_MASTER_REFACTOR_PROMPT.md §2) presents as a
// sequence of single-call batches — web_search alone, then web_fetch alone,
// then web_search alone again — so an all-blocked-batch check catches it
// without the added complexity of splitting a mixed batch and merging
// partial results back into the async tool-execution pipeline. A mixed
// batch containing at least one fresh call is not blocked; that call's own
// repetition is caught on a later round once it, too, stops producing new
// evidence. This scoping limitation is recorded in
// docs/architecture/v1-migration-plan.md as a candidate follow-up.
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
	if strings.TrimSpace(c.MCPServer) != "" {
		return strings.Join([]string{"mcp", c.MCPServer, c.MCPTool, canonicalizeArgs(c.MCPArgs)}, "\x1f")
	}
	resource := strings.TrimSpace(c.Path)
	switch c.Tool {
	case tools.ToolWebSearch, tools.ToolRunCommand:
		resource = normalizeText(c.Body)
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
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func normalizeURL(u string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(u), "/"))
}

// canonicalizeArgs is intentionally simple (trim + lowercase) rather than a
// full JSON key-sort: MCP argument shapes are arbitrary and unknown to this
// package (see tools.Call's MCPArgs doc comment), so byte-for-byte-modulo-
// whitespace equality is the honest limit of what can be canonicalized
// without a schema.
func canonicalizeArgs(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
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
	if len(calls) == 0 {
		return false, false, ""
	}
	for _, c := range calls {
		if !l.wouldBlock(progressFingerprint(c)) {
			l.blockedStreak = 0
			return false, false, ""
		}
	}
	l.blockedStreak++
	return true, l.blockedStreak >= blockedStreakLimit, "repeated tool call blocked: no new evidence since the last identical call"
}

// observeResults updates the ledger from a batch of completed results. Call
// this once real execution finishes, for both the ordinary tool loop and
// /agent on — they share this ledger via the shared kernel (ADR 0001).
func (l *progressLedger) observeResults(results []tools.Result) {
	for _, r := range results {
		l.observe(progressFingerprint(r.Call), progressDigest(r))
	}
}
