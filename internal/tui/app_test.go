package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	cfg := &config.Config{
		Chat: config.ChatConfig{Stream: true, MaxTokens: 128, SystemPrompt: "You are a helpful local assistant."},
		UI:   config.UIConfig{Markdown: false},
		Memory: config.MemoryConfig{
			Path:        filepath.Join(t.TempDir(), "memory.yaml"),
			MaxSnippets: 10,
		},
		Prompt: config.PromptConfig{
			Mode:                   "balanced",
			IncludeSessionSummary:  true,
			IncludeLocalMemory:     true,
			IncludeModelHints:      true,
			IncludeFormattingHints: true,
		},
		Context: config.ContextConfig{
			Strategy:               "auto",
			ReserveResponseTokens:  512,
			SummarizeAfterMessages: 12,
			KeepLastMessages:       8,
			SummaryMaxTokens:       400,
		},
		Network: config.NetworkConfig{Timeout: "120s", ConnectTimeout: "10s"},
		Cache:   config.CacheConfig{TTL: "1h", MaxSizeMB: 16, CacheStreamedResponses: true},
	}
	m := New(Options{Config: cfg, Provider: mock.New(), Model: "demo-model"})
	m.resize(80, 24)
	return m
}

func TestCtrlVAttachesClipboardImage(t *testing.T) {
	m := newTestModel(t)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("ctrl+v should return a clipboard command for a vision model")
	}
	// Simulate the command result instead of touching the real clipboard.
	m.Update(clipboardImageMsg{img: provider.Image{Data: []byte("png"), MIME: "image/png"}})

	if len(m.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.attachments))
	}
	if !strings.Contains(m.View(), "image 1") {
		t.Error("view should show the attachment chip")
	}
}

func TestCtrlVRefusedForNonVisionModel(t *testing.T) {
	m := newTestModel(t)
	m.model = "qwen3:8b"

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd != nil {
		t.Fatal("ctrl+v should be refused for a non-vision model")
	}
	if !strings.Contains(m.errText, "does not appear to support images") {
		t.Errorf("errText = %q, want vision warning", m.errText)
	}
}

func TestCtrlVAllowedWithForceVision(t *testing.T) {
	m := newTestModel(t)
	m.model = "qwen3:8b"
	m.cfg.Chat.ForceVision = true

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("force_vision should allow image paste")
	}
}

func TestCtrlXRemovesAttachment(t *testing.T) {
	m := newTestModel(t)
	m.attachments = []provider.Image{{Data: []byte("a")}, {Data: []byte("b")}}

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if len(m.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 after ctrl+x", len(m.attachments))
	}
}

func TestSendAttachesImagesToUserMessage(t *testing.T) {
	m := newTestModel(t)
	m.attachments = []provider.Image{{Data: []byte("img"), MIME: "image/png"}}
	m.input.SetValue("what is this?")

	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleUser || len(last.Images) != 1 {
		t.Fatalf("last message = %+v, want user message with 1 image", last)
	}
	if len(m.attachments) != 0 {
		t.Error("attachments should be cleared after send")
	}
}

func TestCtrlYCopiesLastReply(t *testing.T) {
	m := newTestModel(t)

	// Nothing to copy yet.
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if cmd != nil {
		t.Error("ctrl+y with no assistant reply should not return a command")
	}
	if m.notice != "nothing to copy yet" {
		t.Errorf("notice = %q, want nothing-to-copy hint", m.notice)
	}

	m.session.AddAssistant("the **answer**")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if cmd == nil {
		t.Fatal("ctrl+y should return a clipboard write command")
	}
	// Successful copy shows a confirmation notice.
	m.Update(copyResultMsg{chars: 14})
	if !strings.Contains(m.notice, "copied") {
		t.Errorf("notice = %q, want copy confirmation", m.notice)
	}
	if !strings.Contains(m.View(), "copied") {
		t.Error("view should show the copy confirmation")
	}
}

func TestCtrlOTogglesMouseCapture(t *testing.T) {
	m := newTestModel(t)
	if !m.mouseEnabled {
		t.Fatal("mouse should start enabled")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if m.mouseEnabled {
		t.Error("ctrl+o should disable mouse capture")
	}
	if cmd == nil {
		t.Error("toggling should return a mouse command")
	}
	if !strings.Contains(m.notice, "text selection on") {
		t.Errorf("notice = %q, want selection-mode hint", m.notice)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if !m.mouseEnabled {
		t.Error("second ctrl+o should re-enable mouse capture")
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func TestSlashShowsSuggestions(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/")
	if len(m.sugs) == 0 {
		t.Fatal("typing / should show command suggestions")
	}
	if !strings.Contains(m.View(), "/help") {
		t.Error("view should list /help in the popup")
	}

	typeText(m, "he")
	if len(m.sugs) != 1 || m.sugs[0].name != "help" {
		t.Fatalf("suggestions for /he = %+v, want only help", m.sugs)
	}

	// Plain text hides the popup again.
	m.input.Reset()
	typeText(m, "hello")
	if len(m.sugs) != 0 {
		t.Error("plain text should not show suggestions")
	}
}

func TestSuggestionNavigationAndTabComplete(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "/")
	first := m.sugs[m.sugIdx].name

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.sugs[m.sugIdx].name == first {
		t.Error("down should move the selection")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.sugs[m.sugIdx].name != first {
		t.Error("up should move the selection back")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "/"+first+" " {
		t.Errorf("tab completed to %q, want /%s ", got, first)
	}
}

func TestHelpCommandOpensAndClosesOverlay(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/help")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.overlayOpen {
		t.Fatal("/help should open the overlay")
	}
	if !strings.Contains(m.View(), "ctrl+y") {
		t.Error("help overlay should show shortcuts")
	}
	// Full content (scrollable) lists commands further down.
	if help := m.helpOverlay(""); !strings.Contains(help, "/model") || !strings.Contains(help, "/provider") {
		t.Error("help content should list slash commands")
	}
	if m.input.Value() != "" {
		t.Error("input should be cleared after running a command")
	}

	// Typing while the overlay is open is swallowed.
	typeText(m, "x")
	if m.input.Value() != "" {
		t.Error("keys should not reach the input while overlay is open")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlayOpen {
		t.Error("esc should close the overlay")
	}
}

func TestEnterRunsSelectedSuggestion(t *testing.T) {
	m := newTestModel(t)

	// "/st" narrows to stats; enter should run it even though not fully typed.
	typeText(m, "/st")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.overlayOpen {
		t.Fatal("enter on the stats suggestion should open the stats overlay")
	}
	if !strings.Contains(m.View(), "session statistics") {
		t.Error("overlay should show session statistics")
	}
}

func TestModelCommandSwitchesModel(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/model demo-model-mini")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.model != "demo-model-mini" {
		t.Errorf("model = %q, want demo-model-mini", m.model)
	}
	if !strings.Contains(m.notice, "model set to") {
		t.Errorf("notice = %q, want model confirmation", m.notice)
	}
}

func TestProviderCommandSwitchesProvider(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Providers = map[string]config.ProviderConfig{
		"mock": {Type: "mock", DefaultModel: "demo-model"},
	}

	typeText(m, "/provider mock")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.prov.Name() != "mock" {
		t.Errorf("provider = %q, want mock", m.prov.Name())
	}
	if cmd == nil {
		t.Error("switching providers should trigger a health check")
	}

	typeText(m, "/provider nope")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.errText, "not configured") {
		t.Errorf("errText = %q, want not-configured error", m.errText)
	}
}

func TestUnknownCommandShowsError(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/bogus")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.errText, "unknown command /bogus") {
		t.Errorf("errText = %q, want unknown command error", m.errText)
	}
}

func TestEscClearsSlashInput(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "/mod")

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.input.Value() != "" || len(m.sugs) != 0 {
		t.Error("esc should clear the pending command and popup")
	}
}

func TestWrapLines(t *testing.T) {
	tests := []struct {
		value string
		width int
		want  int
	}{
		{"", 40, 1},
		{"short", 40, 1},
		{strings.Repeat("x", 90), 40, 3},
		{"a\nb\nc", 40, 3},
		{strings.Repeat("long line\n", 20), 40, 6}, // capped at maxLines
	}
	for _, tt := range tests {
		if got := wrapLines(tt.value, tt.width, 6); got != tt.want {
			t.Errorf("wrapLines(%d chars, %d) = %d, want %d", len(tt.value), tt.width, got, tt.want)
		}
	}
}

func TestInputBoxGrowsAndShrinks(t *testing.T) {
	m := newTestModel(t)
	if m.inputLines != 1 {
		t.Fatalf("inputLines = %d, want 1 initially", m.inputLines)
	}

	typeText(m, strings.Repeat("word ", 40)) // ~200 chars, wraps at width 72
	if m.inputLines < 2 {
		t.Errorf("inputLines = %d, want growth for long prompt", m.inputLines)
	}

	// Ctrl+J adds explicit newlines.
	before := m.inputLines
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.inputLines <= before && before < 6 {
		t.Errorf("inputLines = %d, want growth after ctrl+j", m.inputLines)
	}

	// Sending resets the box to one row.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.inputLines != 1 {
		t.Errorf("inputLines = %d, want 1 after send", m.inputLines)
	}
}

func TestCtrlSSavesSession(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.session.AddUser("hi")
	m.session.AddAssistant("hello")

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if !strings.Contains(m.notice, "session saved") {
		t.Fatalf("notice = %q, want save confirmation", m.notice)
	}

	metas, err := history.List(m.historyDir)
	if err != nil || len(metas) != 1 {
		t.Fatalf("List = (%v, %v), want one saved session", metas, err)
	}
	if metas[0].Messages != 3 {
		t.Errorf("saved messages = %d, want system + user + assistant", metas[0].Messages)
	}
}

func TestSaveDisabledShowsError(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = ""

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if !strings.Contains(m.errText, "disabled") {
		t.Errorf("errText = %q, want disabled error", m.errText)
	}
}

func TestQuitAutoSaves(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.session.AddUser("hi")

	cmd := m.quit()
	if cmd == nil {
		t.Fatal("quit should return tea.Quit")
	}
	metas, _ := history.List(m.historyDir)
	if len(metas) != 1 {
		t.Errorf("quit should auto-save, found %d sessions", len(metas))
	}

	// Empty sessions are not saved.
	m2 := newTestModel(t)
	m2.historyDir = t.TempDir()
	m2.quit()
	metas, _ = history.List(m2.historyDir)
	if len(metas) != 0 {
		t.Errorf("empty session saved, found %d sessions", len(metas))
	}
}

func TestFinishStreamAppendsUsageRecord(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.input.SetValue("hello")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	m.streamBuf.WriteString("reply")
	m.finishStream(&provider.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8})

	records, err := history.ReadUsage(m.historyDir)
	if err != nil || len(records) != 1 {
		t.Fatalf("ReadUsage = (%v, %v), want one record", records, err)
	}
	if records[0].PromptTokens != 3 || records[0].CompletionTokens != 5 {
		t.Errorf("record = %+v", records[0])
	}
}

func TestHistoryOverlayListsSessions(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.session.AddUser("hi")
	m.saveWithNotice()

	typeText(m, "/history")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.overlayOpen {
		t.Fatal("/history should open an overlay")
	}
	if !strings.Contains(m.historyOverlay(), m.sessionName) {
		t.Error("history overlay should list the saved session")
	}
}

func TestAltEnterInsertsNewline(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "line one")

	m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	typeText(m, "line two")

	if got := m.input.Value(); got != "line one\nline two" {
		t.Errorf("input = %q, want two lines", got)
	}
	if userMessages(m) != 0 {
		t.Error("alt+enter must not send the message")
	}
	if m.inputLines != 2 {
		t.Errorf("inputLines = %d, want 2", m.inputLines)
	}
}

func TestDoubleCtrlCQuits(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.session.AddUser("hi")

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatal("first ctrl+c must not quit")
	}
	if !strings.Contains(m.notice, "again to exit") {
		t.Errorf("notice = %q, want arm hint", m.notice)
	}

	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("second ctrl+c should quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("second ctrl+c returned %T, want tea.QuitMsg", cmd())
	}
	// Quit auto-saved the session.
	metas, _ := history.List(m.historyDir)
	if len(metas) != 1 {
		t.Errorf("quit should auto-save, found %d sessions", len(metas))
	}
}

func TestCtrlCClearsInputFirst(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "draft text")

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.input.Value() != "" {
		t.Error("first ctrl+c should clear the input")
	}
	if !strings.Contains(m.notice, "input cleared") {
		t.Errorf("notice = %q, want input-cleared hint", m.notice)
	}
}

func TestCtrlCStopsGenerationFirst(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("hello")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.thinking {
		t.Fatal("should be thinking")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil {
		t.Fatal("first ctrl+c while thinking must not quit")
	}
	if m.thinking {
		t.Error("first ctrl+c should stop generation")
	}
}

func TestUsageCommandOpensDashboard(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	if err := history.AppendUsage(m.historyDir, history.UsageRecord{
		Time: time.Now(), Provider: "mock", Model: "demo-model",
		PromptTokens: 100, CompletionTokens: 250, DurationMS: 800,
	}); err != nil {
		t.Fatal(err)
	}

	typeText(m, "/usage")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.overlayOpen {
		t.Fatal("/usage should open an overlay")
	}
	content := m.usageOverlay()
	for _, want := range []string{"tokens per day", "activity", "mock/demo-model", "favorite model", "Less", "More"} {
		if !strings.Contains(content, want) {
			t.Errorf("usage overlay missing %q", want)
		}
	}
	if !strings.Contains(content, "350") {
		t.Errorf("usage overlay should show 350 total tokens")
	}
}

func TestEscStopsGeneration(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("hello")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.thinking {
		t.Fatal("model should be thinking after send")
	}
	_ = cmd

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.thinking {
		t.Error("esc should stop generation")
	}
	if m.errText != "generation stopped" {
		t.Errorf("errText = %q, want generation stopped", m.errText)
	}
}

// userMessages counts non-system messages in the session.
func userMessages(m *Model) int {
	n := 0
	for _, msg := range m.session.Messages {
		if msg.Role == provider.RoleUser {
			n++
		}
	}
	return n
}
