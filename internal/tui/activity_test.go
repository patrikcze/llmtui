package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// newActivityTestModel wires a model with tools on, MCP auto-approved, and
// one connected mock server whose calls block for delay (0 = instant echo).
func newActivityTestModel(t *testing.T, delay time.Duration) *Model {
	t.Helper()
	m := newTestModel(t)
	m.toolsOn = true
	m.mcpAutoApprove = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	factory := func(c mcp.ServerConfig) (mcp.Client, error) {
		return &mcp.MockClient{ServerName: c.Name, Delay: delay}, nil
	}
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{{
		Name: "jiraWorklog", Transport: mcp.TransportStdio, Command: "x", Enabled: true, Timeout: 5 * time.Second,
	}}, factory)
	if err := m.mcpRegistry.Connect(context.Background(), "jiraWorklog"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	return m
}

func startTestBatch(t *testing.T, m *Model, id string) tea.Cmd {
	t.Helper()
	cmd := m.startToolBatch([]tools.Call{{ID: id, MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: "{}"}})
	if cmd == nil || m.mcpBatchCancel == nil {
		t.Fatal("expected an in-flight async batch")
	}
	return cmd
}

func TestAsyncBatchShowsActivityRegion(t *testing.T) {
	m := newActivityTestModel(t, time.Second)
	hBefore := m.viewport.Height

	startTestBatch(t, m, "c1")
	if m.activity == nil {
		t.Fatal("async batch should set a live activity")
	}
	if got := m.activityHeight(); got != 1 {
		t.Fatalf("activityHeight = %d, want 1", got)
	}
	if m.viewport.Height != hBefore-1 {
		t.Errorf("viewport height = %d, want %d (shrunk by the region)", m.viewport.Height, hBefore-1)
	}
	if !strings.Contains(m.View(), "jiraWorklog: session_start") {
		t.Error("view should show the running call's describe line")
	}
}

func TestActivityClearsWhenResultsLandAndGlyphsSettle(t *testing.T) {
	m := newActivityTestModel(t, 0)
	m.session.AddUser("start a session")
	m.thinking = true
	done := provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{
		{ID: "call_1", Name: "mcp__jiraWorklog__session_start", Arguments: `{}`},
	}}
	_, cmd := m.handleStreamEvent(streamEventMsg{event: done, ok: true})
	if cmd == nil || m.activity == nil {
		t.Fatal("expected an async batch with live activity")
	}
	// While running, the transcript must not duplicate the live region's
	// call line (the static ⚒ form is suppressed for the running batch).
	if strings.Contains(m.View(), "⚒ jiraWorklog: session_start") {
		t.Error("running batch should suppress the transcript's static ⚒ line")
	}

	msg := cmd()
	resultsMsg, ok := msg.(mcpToolResultsMsg)
	if !ok {
		t.Fatalf("cmd() = %T, want mcpToolResultsMsg", msg)
	}
	m.Update(resultsMsg)
	if m.activity != nil {
		t.Fatal("activity should clear when the batch's results land")
	}
	if m.activityHeight() != 0 {
		t.Errorf("activityHeight = %d after settle, want 0", m.activityHeight())
	}
	if !strings.Contains(m.View(), "● jiraWorklog: session_start") {
		t.Error("settled ok call should render with ● in the transcript")
	}
}

func TestActivityClearedOnCancel(t *testing.T) {
	for name, key := range map[string]tea.KeyMsg{
		"esc":    {Type: tea.KeyEsc},
		"ctrl+c": {Type: tea.KeyCtrlC},
	} {
		t.Run(name, func(t *testing.T) {
			m := newActivityTestModel(t, time.Second)
			startTestBatch(t, m, "c1")
			m.Update(key)
			if m.activity != nil {
				t.Error("cancel should clear the live activity")
			}
		})
	}
}

func TestStaleResultsDoNotClearNewBatchActivity(t *testing.T) {
	m := newActivityTestModel(t, time.Second)
	cmdA := startTestBatch(t, m, "a")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // cancel batch A
	startTestBatch(t, m, "b")
	if m.activity == nil {
		t.Fatal("batch B should be live")
	}
	genB := m.activity.gen

	// Batch A's already-launched command still completes (its context was
	// cancelled, not its goroutine) and delivers a stale message.
	staleMsg, ok := cmdA().(mcpToolResultsMsg)
	if !ok {
		t.Fatal("expected batch A's results message")
	}
	m.Update(staleMsg)
	if m.activity == nil || m.activity.gen != genB {
		t.Fatal("stale results must not clear the newer batch's activity")
	}
}

func TestSettledGlyphsFromToolResults(t *testing.T) {
	m := newTestModel(t)
	m.session.AddUser("do things")
	m.session.Messages = append(m.session.Messages,
		provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "c_ok", Name: "read_file", Arguments: `{"path":"a.txt"}`},
			{ID: "c_bad", Name: "write_file", Arguments: `{"path":"b.txt"}`},
		}},
		provider.Message{Role: provider.RoleTool, ToolCallID: "c_ok", ToolName: "read_file", Content: "file contents"},
		provider.Message{Role: provider.RoleTool, ToolCallID: "c_bad", ToolName: "write_file", Content: "error: denied"},
	)
	m.refreshViewport()
	view := m.View()
	// Describe() renders these as "read_file a.txt" and "write b.txt (…)".
	if !strings.Contains(view, "● read_file a.txt") {
		t.Error("ok call should render with ●")
	}
	if !strings.Contains(view, "✗ write b.txt") {
		t.Error("failed call should render with ✗")
	}
	if strings.Contains(view, "⚒ read_file") || strings.Contains(view, "⚒ write b.txt") {
		t.Error("settled calls should not keep the neutral ⚒ glyph")
	}
}

func TestWorkingLineShowsVerbElapsedTokens(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.workingVerb = "Ideating"
	m.streamStart = time.Now().Add(-2 * time.Second)
	m.streamBuf.WriteString(strings.Repeat("x", 400))
	view := m.View()
	for _, want := range []string{"Ideating", "2s", "tokens", "esc to interrupt"} {
		if !strings.Contains(view, want) {
			t.Errorf("working line missing %q", want)
		}
	}
}

func TestWorkingLineDuringMCPBatch(t *testing.T) {
	m := newActivityTestModel(t, time.Second)
	startTestBatch(t, m, "c1")
	if !strings.Contains(m.View(), "Running tools") {
		t.Error("footer should show the tool-batch working line")
	}
}

func TestAnimationsOffOmitsSpinnerTick(t *testing.T) {
	m := newTestModel(t) // newTestModel leaves UI.Animations false
	if initHasSpinnerTick(t, m.Init()) {
		t.Error("animations off must not start the spinner ticker")
	}
	m.cfg.UI.Animations = true
	if !initHasSpinnerTick(t, m.Init()) {
		t.Error("animations on should start the spinner ticker")
	}
}

func initHasSpinnerTick(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init() = %T, want tea.BatchMsg", msg)
	}
	for _, c := range batch {
		if _, ok := c().(spinner.TickMsg); ok {
			return true
		}
	}
	return false
}
