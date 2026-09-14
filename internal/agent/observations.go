package agent

import (
	"fmt"
	"strings"
)

// This file is the Phase 3 observation-view layer: a small, bounded,
// process-local record of what a tool call actually returned, kept so a
// later cycle can recall it after cross-cycle projection (see
// internal/tui's projectCompletedAgentHistory) has removed the raw
// transcript message. It is deliberately not part of AgentRun's persisted
// schema — see .claude/tasks/plans/llmtui-agent-evolution.md §10: "Do not
// persist raw observations in the first release." A resumed run starts with
// an empty cache, which is always a safe (if less informative) default.

const (
	// MaxObservationExcerpt bounds one retained observation's text. This is
	// a proof fragment for recall across cycles, not a transcript copy.
	MaxObservationExcerpt = 512
	// MaxObservations bounds the cache so per-run memory stays fixed
	// regardless of run length; oldest entries are evicted first.
	MaxObservations = 32
)

// ObservationView is one bounded, deterministic record of a tool call's
// observable output.
type ObservationView struct {
	// ID is an opaque, cache-local identifier ("o1", "o2", ...), unrelated
	// to any provider tool-call ID or run-level receipt.
	ID string
	// ResourceKey is the same tool+resource identity ToolCallRecord uses for
	// recovery attribution (tool name plus its dedup-relevant detail — see
	// ToolCallRecord's doc comment), so a caller can look up the observation
	// behind a specific evidence entry.
	ResourceKey string
	Tool        string
	Detail      string
	Cycle       int
	Excerpt     string
	TotalBytes  int
	Truncated   bool
	Success     bool
}

// ObservationCache is a small, bounded, insertion-ordered store of the most
// recent observation views for one run. It is safe for use only from the
// single goroutine that owns the run (matching every other agentLoopState
// field) — it has no internal locking.
type ObservationCache struct {
	items []ObservationView
	seq   int
}

// NewObservationCache returns an empty cache.
func NewObservationCache() *ObservationCache {
	return &ObservationCache{}
}

// Put records one observation, truncating its text to MaxObservationExcerpt
// and evicting the oldest entry once the cache is full. It returns the
// assigned ID and, when an entry was evicted to make room, that entry's
// view — callers use this to maintain an explicit omission manifest naming
// what became unavailable, rather than silently losing it. A nil receiver is
// a no-op, matching this package's other bounded collections.
func (c *ObservationCache) Put(tool, detail string, cycle int, text string, success bool) (id string, evictedView ObservationView, evicted bool) {
	if c == nil {
		return "", ObservationView{}, false
	}
	c.seq++
	view := ObservationView{
		ID:          fmt.Sprintf("o%d", c.seq),
		ResourceKey: resourceKeyFor(tool, detail),
		Tool:        tool,
		Detail:      detail,
		Cycle:       cycle,
		Excerpt:     truncate(text, MaxObservationExcerpt),
		TotalBytes:  len(text),
		Truncated:   len(text) > MaxObservationExcerpt,
		Success:     success,
	}
	c.items = append(c.items, view)
	if len(c.items) > MaxObservations {
		evictedView = c.items[0]
		evicted = true
		c.items = c.items[1:]
	}
	return view.ID, evictedView, evicted
}

// Latest returns the most recently recorded view for resourceKey, if any.
func (c *ObservationCache) Latest(resourceKey string) (ObservationView, bool) {
	if c == nil {
		return ObservationView{}, false
	}
	for i := len(c.items) - 1; i >= 0; i-- {
		if c.items[i].ResourceKey == resourceKey {
			return c.items[i], true
		}
	}
	return ObservationView{}, false
}

// Recent returns up to n most-recently recorded views, newest first.
func (c *ObservationCache) Recent(n int) []ObservationView {
	if c == nil || n <= 0 {
		return nil
	}
	if n > len(c.items) {
		n = len(c.items)
	}
	out := make([]ObservationView, n)
	for i := range out {
		out[i] = c.items[len(c.items)-1-i]
	}
	return out
}

// ResourceLabel renders "tool(detail)" for display — human-readable, unlike
// ResourceKey, which joins the same two fields with an internal separator
// byte not meant to appear in a prompt.
func (v ObservationView) ResourceLabel() string {
	return v.Tool + "(" + v.Detail + ")"
}

// FormatExcerpt renders one view as a single bounded line, safe to embed in
// a labeled prompt section: "tool(detail) [cycle N]: excerpt" with an
// explicit "...truncated" marker when the source was longer than what was
// retained, so a model is never told a partial view is the complete result.
func (v ObservationView) FormatExcerpt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [cycle %d]: %s", v.ResourceLabel(), v.Cycle, v.Excerpt)
	if v.Truncated {
		b.WriteString(" …truncated")
	}
	return b.String()
}
