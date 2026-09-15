package tui

import (
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestSettledTranscriptCacheHitsOnUnrelatedChanges is the Phase 6 exit
// criterion: refreshViewport must not re-render settled history for state
// that only affects the live tail (streaming buffers, notices, errors).
func TestSettledTranscriptCacheHitsOnUnrelatedChanges(t *testing.T) {
	m := newTestModel(t)
	buildBenchSession(m, 20, true)
	m.refreshViewport()

	cached := m.transcriptCache
	key := m.transcriptCacheKey

	m.thinking = true
	m.streamBuf.WriteString("partial answer")
	m.reasoningBuf.WriteString("thinking about it")
	m.notice = "✓ copied last reply"
	m.refreshViewport()

	if m.transcriptCache != cached {
		t.Error("settled transcript cache changed on a live-tail-only update")
	}
	if m.transcriptCacheKey != key {
		t.Errorf("cache key changed on a live-tail-only update: %+v -> %+v", key, m.transcriptCacheKey)
	}
}

// TestSettledTranscriptCacheInvalidatesOnSessionChange covers the mutation
// this cache exists to track correctly: session.Rev() must change (and the
// cache with it) for every way the transcript's message set can change,
// including the in-place Display edit that doesn't touch len(Messages).
func TestSettledTranscriptCacheInvalidatesOnSessionChange(t *testing.T) {
	m := newTestModel(t)
	buildBenchSession(m, 5, false)
	m.refreshViewport()
	before := m.transcriptCache

	m.session.AddUser("one more message")
	m.refreshViewport()
	if m.transcriptCache == before {
		t.Error("appending a message did not invalidate the settled transcript cache")
	}

	// Only a "[tool results]"-prefixed user message renders its Display
	// field (renderToolDiff) — any other message ignores it, so the
	// fixture must match what SetLastDisplay's real caller produces.
	before = m.transcriptCache
	m.session.AddMessage(provider.Message{Role: provider.RoleUser, Content: tools.ResultsPrefix + "\nran write_file"})
	m.refreshViewport()
	beforeDisplay := m.transcriptCache
	if beforeDisplay == before {
		t.Fatal("appending a message did not invalidate the cache (precondition for the Display check below)")
	}

	m.session.SetLastDisplay("Update(file.go) — added 1 line(s)")
	m.refreshViewport()
	if m.transcriptCache == beforeDisplay {
		t.Error("SetLastDisplay (same message count) did not invalidate the settled transcript cache")
	}

	before = m.transcriptCache
	m.session.DropLast()
	m.refreshViewport()
	if m.transcriptCache == before {
		t.Error("DropLast did not invalidate the settled transcript cache")
	}

	before = m.transcriptCache
	m.session.Clear()
	m.refreshViewport()
	if m.transcriptCache == before {
		t.Error("Clear did not invalidate the settled transcript cache")
	}

	before = m.transcriptCache
	m.session.SetMessages([]provider.Message{{Role: provider.RoleUser, Content: "reloaded"}})
	m.refreshViewport()
	if m.transcriptCache == before {
		t.Error("SetMessages did not invalidate the settled transcript cache")
	}
}

// TestSettledTranscriptCacheInvalidatesOnPresentationChange covers every
// non-session input transcriptCacheKey depends on. Each must invalidate the
// cache on its own — an omission here is exactly the bug class this cache
// risks (stale settled content after a setting changes).
func TestSettledTranscriptCacheInvalidatesOnPresentationChange(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*Model) // builds a session shaped to make the toggle visible
		apply func(*Model)
	}{
		{"resize", func(m *Model) { buildBenchSession(m, 10, true) }, func(m *Model) { m.resize(120, 30) }},
		{"markdown toggle", func(m *Model) { buildBenchSession(m, 10, true) }, func(m *Model) { m.cfg.UI.Markdown = !m.cfg.UI.Markdown }},
		{
			"math toggle",
			func(m *Model) {
				m.cfg.UI.Markdown = true
				m.session.AddUser("what is the formula?")
				m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, Content: "The result is $x^2$."})
			},
			func(m *Model) { m.cfg.UI.Math.Enabled = !m.cfg.UI.Math.Enabled },
		},
		{"reasoning visibility toggle", func(m *Model) { buildBenchSession(m, 10, true) }, func(m *Model) { m.showReasoning = !m.showReasoning }},
		{
			"tool output mode toggle",
			func(m *Model) {
				m.session.AddUser("run a tool")
				m.session.AddMessage(provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{ID: "call-1", Name: "read_file", Arguments: `{"path":"a.go"}`},
					},
				})
				m.session.AddMessage(provider.Message{
					Role: provider.RoleTool, ToolCallID: "call-1", ToolName: "read_file",
					Content: "package tui\n\nfunc main() {}\n",
				})
			},
			func(m *Model) { m.toolsShowOutput = !m.toolsShowOutput },
		},
		{"demo mode toggle", func(m *Model) { buildBenchSession(m, 10, true) }, func(m *Model) { m.demoMode = !m.demoMode }},
		{
			"tools-on banner toggle",
			func(m *Model) {
				buildBenchSession(m, 3, false)
				m.toolRunner = &tools.Runner{}
			},
			func(m *Model) { m.toolsOn = !m.toolsOn },
		},
		{
			"live tool batch starting",
			func(m *Model) {
				m.session.AddUser("run a tool")
				m.session.AddMessage(provider.Message{
					Role: provider.RoleAssistant,
					ToolCalls: []provider.ToolCall{
						{ID: "call-1", Name: "read_file", Arguments: `{"path":"a.go"}`},
					},
				})
			},
			func(m *Model) {
				m.activity = newToolActivity([]tools.Call{{ID: "call-1", Tool: tools.ToolReadFile}}, 1)
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newTestModel(t)
			c.setup(m)
			m.refreshViewport()
			before := m.transcriptCache

			c.apply(m)
			m.refreshViewport()

			if m.transcriptCache == before {
				t.Errorf("%s did not invalidate the settled transcript cache", c.name)
			}
		})
	}
}

// TestSettledTranscriptCacheMatchesUncachedRender guards against the cache
// silently diverging from what a full render would produce: the cached
// path must be byte-identical to forcing a fresh render of the same state.
func TestSettledTranscriptCacheMatchesUncachedRender(t *testing.T) {
	m := newTestModel(t)
	buildBenchSession(m, 15, true)

	cached := m.settledTranscriptCached()

	m.transcriptCacheValid = false
	fresh := m.renderSettledTranscript()

	if cached != fresh {
		t.Error("cached settled transcript differs from an uncached render of identical state")
	}
}

func TestStreamingViewportSectionsMatchWholeWidthRender(t *testing.T) {
	m := newTestModel(t)
	buildBenchSession(m, 3, true)
	m.thinking = true
	m.streamBuf.WriteString("live answer")

	settled := m.settledTranscriptCached()
	live := m.renderLiveTail()
	want := lipgloss.NewStyle().Width(m.viewport.Width()).Render(settled + live)
	m.refreshViewport()

	if got := m.viewport.GetContent(); got != want {
		t.Fatalf("section-wise viewport render changed content:\nwant %q\n got %q", want, got)
	}
}
