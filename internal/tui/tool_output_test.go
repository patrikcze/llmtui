package tui

import (
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func addToolOutputTestBatch(m *Model) {
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{
			{ID: "first", Name: "run_command", Arguments: `{"command":"echo first"}`},
			{ID: "second", Name: "run_command", Arguments: `{"command":"echo second"}`},
		},
	})
	// Results deliberately arrive in reverse order.
	m.session.AddMessage(provider.Message{
		Role: provider.RoleTool, ToolCallID: "second", ToolName: "run_command", Content: "second output\nsecond detail",
	})
	m.session.AddMessage(provider.Message{
		Role: provider.RoleTool, ToolCallID: "first", ToolName: "run_command", Content: "first output\nfirst detail",
	})
	m.refreshViewport()
}

func clickToolOutput(t *testing.T, m *Model, index int) {
	t.Helper()
	z := renderedZone(t, m, toolOutputZoneID(index))
	m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
}

func TestToolOutputClickExpandsOnlyMatchingResult(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	addToolOutputTestBatch(m)
	if got := ansi.Strip(m.viewport.View()); !strings.Contains(got, "⎿  + 2 lines of output · click to expand") {
		t.Fatalf("missing expandable caption: %s", got)
	}
	clickToolOutput(t, m, 2)
	view := ansi.Strip(m.viewport.View())
	for _, want := range []string{"Arguments: {\"command\":\"echo second\"}", "second detail", "click to collapse"} {
		if !strings.Contains(view, want) {
			t.Errorf("expanded output missing %q: %s", want, view)
		}
	}
	if strings.Contains(view, "first detail") || strings.Contains(view, `Arguments: {"command":"echo first"}`) {
		t.Fatalf("expanded the wrong call: %s", view)
	}
	if m.sel.selecting || m.sel.hasSelection {
		t.Fatal("plain click left text selection active")
	}
	clickToolOutput(t, m, 2)
	if strings.Contains(ansi.Strip(m.viewport.View()), "second detail") {
		t.Fatal("second click did not collapse output or refresh cached transcript")
	}
}

func TestToolOutputClickGuards(t *testing.T) {
	for _, name := range []string{"drag", "right button", "overlay", "approval"} {
		t.Run(name, func(t *testing.T) {
			m := newTestModel(t)
			addToolOutputTestBatch(m)
			z := renderedZone(t, m, toolOutputZoneID(2))
			msg := tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft}
			switch name {
			case "drag":
				m.Update(tea.MouseClickMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})
				msg.X = z.EndX
			case "right button":
				msg.Button = tea.MouseRight
			case "overlay":
				m.overlayOpen = true
			case "approval":
				m.pendingCalls = []tools.Call{{Tool: tools.ToolRunCommand}}
			}
			if _, _, handled := m.updateToolOutputClick(msg); handled {
				t.Fatal("non-toggle interaction expanded a tool result")
			}
			if name == "drag" {
				if _, cmd := m.endSelection(msg); cmd == nil {
					t.Fatal("drag should still produce a copy command")
				}
			}
		})
	}
}

func TestToolOutputGlobalToggleResetsIndividualOverrides(t *testing.T) {
	m := newTestModel(t)
	addToolOutputTestBatch(m)
	clickToolOutput(t, m, 2)
	runCommand(m, "/tools output")
	view := ansi.Strip(m.viewport.View())
	if !strings.Contains(view, "first detail") || !strings.Contains(view, "second detail") {
		t.Fatalf("global toggle did not expand all results: %s", view)
	}
	clickToolOutput(t, m, 2)
	if strings.Contains(ansi.Strip(m.viewport.View()), "second detail") {
		t.Fatal("could not collapse an individual result in full-output mode")
	}
	runCommand(m, "/tools output")
	if view = ansi.Strip(m.viewport.View()); strings.Contains(view, "first detail") || strings.Contains(view, "second detail") {
		t.Fatalf("global toggle did not collapse all results: %s", view)
	}
	clickToolOutput(t, m, 2)
	runCommand(m, "/clear")
	addToolOutputTestBatch(m)
	if strings.Contains(ansi.Strip(m.viewport.View()), "second detail") {
		t.Fatal("new conversation inherited an old expansion")
	}
}

func TestToolOutputExpansionPreservesScrollAndSanitizes(t *testing.T) {
	m := newTestModel(t)
	m.resize(55, 20)
	m.session.AddMessage(provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: "long", Name: "run_command", Arguments: "unsafe\x1b[2Jargument"}},
	})
	m.session.AddMessage(provider.Message{
		Role: provider.RoleTool, ToolCallID: "long", Content: strings.Repeat("line\n", 100) + "\x1b[2Jlast",
	})
	m.refreshViewport()
	offset := m.viewport.YOffset()
	clickToolOutput(t, m, 2)
	if m.viewport.YOffset() != offset {
		t.Fatal("expansion jumped to the end of long output")
	}
	transcript := m.settledTranscriptCached()
	if strings.Contains(transcript, "\x1b[2J") {
		t.Fatal("execution details contain a terminal control sequence")
	}
	if !strings.Contains(ansi.Strip(transcript), "last") {
		t.Fatal("expanded output was truncated")
	}
}

// A zone ID is process-global, so an earlier render with another layout
// leaves bounds behind under the same ID. The click must use this frame's.
func TestToolOutputClickIgnoresStaleZoneFromEarlierRender(t *testing.T) {
	// One P keeps the zone worker from running until this goroutine yields,
	// which is the starved-worker ordering a loaded -race run hits.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))

	earlier := newTestModel(t)
	earlier.resize(100, 40)
	addToolOutputTestBatch(earlier)
	stale := *renderedZone(t, earlier, toolOutputZoneID(2))

	m := newTestModel(t)
	m.resize(55, 20)
	m.session.AddMessage(provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: "only", Name: "run_command", Arguments: `{"command":"echo only"}`}},
	})
	m.session.AddMessage(provider.Message{
		Role: provider.RoleTool, ToolCallID: "only", ToolName: "run_command", Content: "only output\nonly detail",
	})
	m.refreshViewport()

	z := renderedZone(t, m, toolOutputZoneID(2))
	if z.StartY == stale.StartY {
		t.Fatalf("got the earlier render's bounds (row %d) for a caption this frame draws elsewhere", z.StartY)
	}
	clickToolOutput(t, m, 2)
	if !strings.Contains(ansi.Strip(m.viewport.View()), "only detail") {
		t.Fatal("click used the earlier render's bounds and did not expand the result")
	}
}

func TestFencedToolOutputClickShowsExecution(t *testing.T) {
	m := newTestModel(t)
	m.resize(100, 40)
	m.session.AddAssistant("```tool run_command\necho fenced\n```")
	m.session.AddUser(tools.ResultsPrefix + "\n### run_command\nfenced output\nfenced detail")
	m.refreshViewport()
	clickToolOutput(t, m, 2)
	view := ansi.Strip(m.viewport.View())
	for _, want := range []string{"Execution:", "echo fenced", "fenced detail"} {
		if !strings.Contains(view, want) {
			t.Errorf("expanded fenced tool missing %q: %s", want, view)
		}
	}
	clickToolOutput(t, m, 2)
	if strings.Contains(ansi.Strip(m.viewport.View()), "fenced detail") {
		t.Fatal("fenced result did not collapse")
	}
}
