package tui

import (
	"fmt"
	"strings"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/terminaltext"
	"github.com/patrikcze/llmtui/internal/tools"
)

// A native tool batch can return more than the next request has room for.
// prepareRequest can only drop *older* history groups, so when the batch's
// own results are too large the continuation used to fail the whole run
// ("estimated request is … tokens but only … are available"). Instead, the
// newest results are bounded to what fits before they are recorded or
// appended: each keeps its head and says it was cut, a read keeps whole
// lines, and its window, coverage and file version describe only what was
// delivered. The run ledger, the session and every later request therefore
// agree, and a cut read can never prove coverage of lines the model did not
// see. When even an emptied batch cannot fit (the fixed prompt itself is
// too large), nothing changes and the request fails as before.

// toolResultTruncationMarker ends a result cut to fit the context window.
const toolResultTruncationMarker = "[truncated to fit the context window — reread with offset/limit]"

// minBoundedResultBytes is the smallest head kept of a result that is cut.
const minBoundedResultBytes = 256

// maxBoundingPasses bounds the estimate-and-cut loop.
const maxBoundingPasses = 6

// boundToolResultsToBudget returns results cut, newest first, so that the
// continuation carrying them fits the context budget. It leaves results
// unchanged when they already fit, for fenced-protocol results (they travel
// in a user message, not a continuation), and when cutting cannot help.
func (m *Model) boundToolResultsToBudget(results []tools.Result) []tools.Result {
	if len(results) == 0 || results[0].Call.ID == "" || !m.useNativeTools() {
		return results
	}
	candidate := results
	for pass := 0; pass < maxBoundingPasses; pass++ {
		excess, ok := m.continuationExcessTokens(candidate)
		if !ok || excess <= 0 {
			return candidate
		}
		// provider.EstimateTokens counts four bytes per token; cut a little
		// more than the excess so a pass rarely falls just short.
		next, changed := cutNewestResults(candidate, excess*4+excess/2+64)
		if !changed {
			return candidate
		}
		candidate = next
	}
	return candidate
}

// continuationExcessTokens estimates how many tokens the continuation that
// would carry results exceeds the budget by, including the runtime sections
// those results will add once recorded (retained observations, entity
// previews). ok is false when the request cannot be estimated.
func (m *Model) continuationExcessTokens(results []tools.Result) (excess int, ok bool) {
	original := m.session.Messages
	messages := append(append([]provider.Message(nil), original...), tools.NativeResults(results)...)
	m.session.SetMessages(messages)
	prepared, _ := m.prepareRequest("", nil, true)
	m.session.SetMessages(original)
	if prepared.estimate.Window <= 0 {
		return 0, false
	}
	budget := prepared.estimate.Window - prepared.estimate.Reserve
	return prepared.estimate.Total + m.runtimeGrowthTokens(results) - budget, true
}

// runtimeGrowthTokens bounds what recording results will add to the next
// request beyond their own output: up to maxDirectiveObservations retained
// excerpts in the agent directive, the entity/capture reference lines
// appended to each result, and the new entities' previews in Entity Context
// (capped at that section's own budget). It errs on the high side: an
// overestimate only cuts a little more, an underestimate fails the run.
func (m *Model) runtimeGrowthTokens(results []tools.Result) int {
	growth := 0
	if m.agentRunActive() {
		shown := 0
		for i := len(results) - 1; i >= 0 && shown < maxDirectiveObservations; i-- {
			if r := results[i]; r.Err == nil && isObservableReadTool(r.Call.Tool) && strings.TrimSpace(r.Output) != "" {
				growth += min(len(r.Output), agent.MaxObservationExcerpt) + 64
				shown++
			}
		}
	}
	if m.entitiesEnabled() {
		// registerResultEntities appends reference lines to each result's
		// output: a header plus one line per registered entity, and one per
		// published capture.
		for _, r := range results {
			if len(r.Entities) > 0 {
				growth += 96 + 160*len(r.Entities)
			}
			if len(r.Captures) > 0 && m.outputStorageEnabled() {
				growth += 128 + 224*len(r.Captures)
			}
		}
	}
	if m.entityToolsAvailable() {
		entities := 0
		for _, r := range results {
			for _, candidate := range r.Entities {
				// The registry falls back to the payload for an empty
				// preview and caps it (entity.Registry.Put).
				preview := len(candidate.Preview)
				if preview == 0 && !candidate.Redacted {
					preview = len(candidate.Payload)
				}
				entities += min(preview, entity.DefaultMaxPreviewBytes) + 256
			}
		}
		growth += min(entities, m.entityContextTokenBudget()*4)
	}
	return growth / 4
}

// cutNewestResults removes about cutBytes from the successful results,
// newest first, keeping at least minBoundedResultBytes of each.
func cutNewestResults(results []tools.Result, cutBytes int) ([]tools.Result, bool) {
	out := append([]tools.Result(nil), results...)
	changed := false
	for i := len(out) - 1; i >= 0 && cutBytes > 0; i-- {
		r := out[i]
		spare := len(r.Output) - minBoundedResultBytes
		if r.Err != nil || spare <= 0 {
			continue
		}
		cut := min(spare, cutBytes)
		bounded, ok := boundResult(r, len(r.Output)-cut)
		if !ok {
			continue
		}
		cutBytes -= len(r.Output) - len(bounded.Output)
		out[i] = bounded
		changed = true
	}
	return out, changed
}

// boundResult keeps at most keepBytes of r's output and marks it cut.
func boundResult(r tools.Result, keepBytes int) (tools.Result, bool) {
	if keepBytes >= len(r.Output) {
		return r, false
	}
	if r.Call.Tool == tools.ToolReadFile {
		return boundReadResult(r, keepBytes)
	}
	head, _ := terminaltext.TruncateBytes(r.Output, max(keepBytes, 0))
	r.Output = strings.TrimRight(head, "\n") + "\n" + toolResultTruncationMarker
	markPartial(&r)
	return r, true
}

// boundReadResult cuts a read_file result on a line boundary and rewrites
// its window, header and coverage to the lines actually kept.
func boundReadResult(r tools.Result, keepBytes int) (tools.Result, bool) {
	header, body := "", r.Output
	if strings.HasPrefix(body, "[read_file:") {
		if end := strings.Index(body, "\n\n"); end >= 0 {
			header, body = body[:end], body[end+2:]
		}
	}
	start := int64(1)
	if r.Meta.Window != nil && r.Meta.Window.StartLine > 0 {
		start = r.Meta.Window.StartLine
	}
	budget := keepBytes - len(header)
	kept, lines := 0, int64(0)
	for kept < len(body) {
		next := strings.IndexByte(body[kept:], '\n')
		if next < 0 || kept+next+1 > budget {
			break
		}
		kept += next + 1
		lines++
	}
	if kept >= len(body) {
		return r, false
	}
	end := start + lines - 1
	window := tools.Window{StartLine: start, EndLine: end}
	if r.Meta.Window != nil {
		window = *r.Meta.Window
		window.EndLine = end
		window.EndByte = window.StartByte + int64(kept)
	}
	next := end + 1
	window.NextOffset = &next
	window.NextByteOffset = nil
	window.PartialLine = false

	var b strings.Builder
	total := "?"
	if t := r.Meta.Coverage.TotalLines; t != nil {
		total = fmt.Sprint(*t)
	}
	if lines > 0 {
		fmt.Fprintf(&b, "[read_file: %s lines %d-%d of %s, next_offset=%d; truncated to fit the context window]\n\n", r.Call.Path, start, end, total, next)
		b.WriteString(body[:kept])
		r.Meta.Window = &window
	} else {
		fmt.Fprintf(&b, "[read_file: %s — no complete line fit in the context window]\n\n", r.Call.Path)
		// Nothing was delivered, so no window may claim coverage.
		r.Meta.Window = nil
		r.Meta.Coverage.TotalLines = nil
	}
	fmt.Fprintf(&b, "[truncated to fit the context window — reread with offset=%d and a smaller limit]", next)
	r.Output = b.String()
	markPartial(&r)
	return r, true
}

// markPartial records that a result was cut: it is a partial observation,
// its preview is incomplete, and a file version it carries was not fully
// delivered, so it cannot bind a later edit as observed.
func markPartial(r *tools.Result) {
	r.Meta.Outcome = tools.OutcomePartial
	r.Meta.Coverage.PreviewComplete = false
	r.Meta.Coverage.Reasons = append(append([]string(nil), r.Meta.Coverage.Reasons...), "context_window")
	if v := r.Meta.FileVersion; v != nil {
		copied := *v
		copied.Complete = false
		r.Meta.FileVersion = &copied
	}
}
