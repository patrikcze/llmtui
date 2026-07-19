package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/terminaltext"
	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/tui/components"
)

// callStatus tracks one activity entry's lifecycle. Under batch-at-once
// result delivery every entry stays callRunning until the whole batch
// settles; the field exists so per-call progress messages can be added later
// without reshaping the renderer.
type callStatus int

const (
	callRunning callStatus = iota
	callOK
	callErr
)

type activityEntry struct {
	call   tools.Call
	status callStatus
}

// toolActivity is the UI-only live view of one in-flight async tool batch.
// It never touches the session, so history save/load is unaffected.
type toolActivity struct {
	entries   []activityEntry
	startedAt time.Time
	gen       int // mcpBatchGen this batch launched under
}

func newToolActivity(calls []tools.Call, gen int) *toolActivity {
	entries := make([]activityEntry, 0, len(calls))
	for _, c := range calls {
		entries = append(entries, activityEntry{call: c, status: callRunning})
	}
	return &toolActivity{entries: entries, startedAt: time.Now(), gen: gen}
}

// maxActivityRows caps the live region so a huge batch can't squeeze the
// transcript out of view; the overflow collapses into one "+N more" line.
const maxActivityRows = 6

// activityHeight is the number of terminal rows the live region occupies,
// so resize()/maxInputLines can budget for it.
func (m *Model) activityHeight() int {
	if m.activity == nil {
		return 0
	}
	if n := len(m.activity.entries); n <= maxActivityRows {
		return n
	}
	return maxActivityRows + 1
}

// renderActivity renders the live region: one spinner-led line per running
// call. It is re-rendered by View() on every spinner tick, which is what
// animates it — the transcript viewport above is never touched.
func (m *Model) renderActivity() string {
	if m.activity == nil {
		return ""
	}
	glyph := components.SpinnerFrame(m.frame, false, m.cfg.UI.Animations)
	entries := m.activity.entries
	overflow := 0
	if len(entries) > maxActivityRows {
		overflow = len(entries) - maxActivityRows
		entries = entries[:maxActivityRows]
	}
	var b strings.Builder
	for i, e := range entries {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(m.theme.Spinner.Render(glyph) + " " + m.theme.SystemNote.Render(terminaltext.Sanitize(e.call.Describe())))
	}
	if overflow > 0 {
		b.WriteString("\n" + m.theme.SystemNote.Render(fmt.Sprintf("  … +%d more call(s)", overflow)))
	}
	return b.String()
}

// workingVerbs is the pool the footer's working line draws from — one verb
// picked per request, Claude-Code style, so the word doesn't churn per tick.
var workingVerbs = []string{
	"Thinking", "Ideating", "Reasoning", "Composing",
	"Percolating", "Pondering", "Distilling", "Brewing",
}
