package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	cfg := &config.Config{
		Chat: config.ChatConfig{
			Stream: true, MaxTokens: 128,
			SystemPrompt: "You are a helpful local assistant.", StripLeakedThinking: true,
			HistoryDir: filepath.Join(t.TempDir(), "history"),
		},
		UI: config.UIConfig{Markdown: false, ShowReasoning: true},
		Memory: config.MemoryConfig{
			Path:        filepath.Join(t.TempDir(), "memory.yaml"),
			MaxSnippets: 10,
			Retrieval: config.MemoryRetrievalConfig{
				Enabled: true, MaxContextTokens: 1800, TopK: 10,
				UserTokens: 256, ProjectTokens: 512, EpisodicTokens: 384,
				AgentTokens: 512, SourceTokens: 768,
			},
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
		// Mirrors the production viper defaults (config.setDefaults) for the
		// two v1 protection toggles, since this harness builds Config
		// directly rather than through viper.
		Agent: config.AgentConfig{EnforceBudgetsLive: true},
		Tools: config.ToolsConfig{NoProgress: config.NoProgressConfig{Enabled: true, Threshold: 3}},
	}
	m := New(Options{Config: cfg, Provider: mock.New(), Model: "demo-model"})
	m.resize(80, 24)
	return m
}

func TestCtrlVAttachesClipboardImage(t *testing.T) {
	m := newTestModel(t)

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+v should return a clipboard command for a vision model")
	}
	// Simulate the command result instead of touching the real clipboard.
	m.Update(clipboardImageMsg{img: provider.Image{Data: []byte("png"), MIME: "image/png"}})

	if len(m.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.attachments))
	}
	if !strings.Contains(m.render(), "image 1") {
		t.Error("view should show the attachment chip")
	}
}

func TestProviderAndTranscriptTerminalContentIsSanitized(t *testing.T) {
	m := newTestModel(t)
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:  provider.EventDelta,
		Delta: "safe\x1b]52;c;Y2xpcA==\x07 text\x1b[2J",
	}, ok: true, gen: m.streamGen})
	if got := m.streamBuf.String(); got != "safe text" {
		t.Fatalf("stream buffer = %q, want sanitized provider text", got)
	}

	m.session.Messages = append(m.session.Messages,
		provider.Message{Role: provider.RoleUser, Content: "user\x1b]0;spoof\x07"},
		provider.Message{Role: provider.RoleTool, ToolName: "mcp", Content: "tool\x1b[2J"},
	)
	m.refreshViewport()
	view := m.viewport.View()
	if strings.Contains(view, "spoof") || strings.ContainsRune(view, '\x07') {
		t.Fatalf("terminal payload survived transcript rendering: %q", view)
	}
}

func TestCtrlVRefusedForNonVisionModel(t *testing.T) {
	m := newTestModel(t)
	m.model = "qwen3:8b"

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
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

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("force_vision should allow image paste")
	}
}

func TestCtrlVUsesCachedBackendVisionCapability(t *testing.T) {
	yes := true
	m := newTestModel(t)
	// "qwen/qwen3.6-27b" matches no heuristic pattern, but a prior ListModels
	// call reported real vision support for it (e.g. LM Studio's
	// /api/v0/models "type": "vlm"); that cached data must win.
	m.model = "qwen/qwen3.6-27b"
	m.cacheVisionInfo([]provider.ModelInfo{{ID: "qwen/qwen3.6-27b", Vision: &yes}})

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("cached backend vision capability should allow image paste even though the ID heuristic misses this model")
	}
}

func TestCtrlVRefusedWhenCachedBackendDataSaysNoVision(t *testing.T) {
	no := false
	m := newTestModel(t)
	// "gpt-4o" matches the heuristic, but real backend data (when available)
	// must take precedence over the guess.
	m.model = "gpt-4o"
	m.cacheVisionInfo([]provider.ModelInfo{{ID: "gpt-4o", Vision: &no}})

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("cached backend vision capability should refuse image paste even though the ID heuristic matches")
	}
}

func TestCtrlXRemovesAttachment(t *testing.T) {
	m := newTestModel(t)
	m.attachments = []provider.Image{{Data: []byte("a")}, {Data: []byte("b")}}

	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if len(m.attachments) != 1 {
		t.Fatalf("attachments = %d, want 1 after ctrl+x", len(m.attachments))
	}
}

func TestSendAttachesImagesToUserMessage(t *testing.T) {
	m := newTestModel(t)
	m.attachments = []provider.Image{{Data: []byte("img"), MIME: "image/png"}}
	m.input.SetValue("what is this?")

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

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
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Error("ctrl+y with no assistant reply should not return a command")
	}
	if m.notice != "nothing to copy yet" {
		t.Errorf("notice = %q, want nothing-to-copy hint", m.notice)
	}

	m.session.AddAssistant("the **answer**")
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+y should return a clipboard write command")
	}
	// Successful copy shows a confirmation notice.
	m.Update(copyResultMsg{chars: 14})
	if !strings.Contains(m.notice, "copied") {
		t.Errorf("notice = %q, want copy confirmation", m.notice)
	}
	if !strings.Contains(m.render(), "copied") {
		t.Error("view should show the copy confirmation")
	}
}

func TestCtrlOTogglesMouseCapture(t *testing.T) {
	m := newTestModel(t)
	if !m.mouseEnabled {
		t.Fatal("mouse should start enabled")
	}

	// v2 removed tea.EnableMouseCellMotion/tea.DisableMouse: mouse mode is
	// now a View() field read fresh every render instead of a Cmd, so the
	// toggle's effect shows up in View().MouseMode rather than a returned
	// tea.Cmd.
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.mouseEnabled {
		t.Error("ctrl+o should disable mouse capture")
	}
	if mode := m.View().MouseMode; mode != tea.MouseModeNone {
		t.Errorf("View().MouseMode = %v, want MouseModeNone after disabling", mode)
	}
	if !strings.Contains(m.notice, "text selection on") {
		t.Errorf("notice = %q, want selection-mode hint", m.notice)
	}

	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !m.mouseEnabled {
		t.Error("second ctrl+o should re-enable mouse capture")
	}
	if mode := m.View().MouseMode; mode != tea.MouseModeCellMotion {
		t.Errorf("View().MouseMode = %v, want MouseModeCellMotion after re-enabling", mode)
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestSlashShowsSuggestions(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/")
	if len(m.sugs) == 0 {
		t.Fatal("typing / should show command suggestions")
	}
	if !strings.Contains(m.render(), "/help") {
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

	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.sugs[m.sugIdx].name == first {
		t.Error("down should move the selection")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.sugs[m.sugIdx].name != first {
		t.Error("up should move the selection back")
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.input.Value(); got != "/"+first+" " {
		t.Errorf("tab completed to %q, want /%s ", got, first)
	}
}

func TestHelpCommandOpensAndClosesOverlay(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/help")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.overlayOpen {
		t.Fatal("/help should open the overlay")
	}
	if !strings.Contains(m.render(), "ctrl+y") {
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

	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.overlayOpen {
		t.Error("esc should close the overlay")
	}
}

func TestEnterRunsSelectedSuggestion(t *testing.T) {
	m := newTestModel(t)

	// "/st" narrows to stats; enter should run it even though not fully typed.
	typeText(m, "/st")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.overlayOpen {
		t.Fatal("enter on the stats suggestion should open the stats overlay")
	}
	if !strings.Contains(m.render(), "session statistics") {
		t.Error("overlay should show session statistics")
	}
}

func TestModelCommandSwitchesModel(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/model demo-model-mini")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.prov.Name() != "mock" {
		t.Errorf("provider = %q, want mock", m.prov.Name())
	}
	if cmd == nil {
		t.Error("switching providers should trigger a health check")
	}

	typeText(m, "/provider nope")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.errText, "not configured") {
		t.Errorf("errText = %q, want not-configured error", m.errText)
	}
}

func TestModelsPickerNavigatesAndSelects(t *testing.T) {
	m := newTestModel(t)
	models := []provider.ModelInfo{
		{ID: "alpha"},
		{ID: "demo-model"},
		{ID: "omega"},
	}

	m.openModelsPicker(models)
	if m.pickerIdx != 1 {
		t.Fatalf("initial picker index = %d, want active model at 1", m.pickerIdx)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	// The marker and the label are separate Render() calls; lipgloss v2
	// always emits color codes (no more auto-disable for a non-tty test
	// process), so strip them before checking the two land next to each
	// other as plain text.
	view := ansi.Strip(m.viewport.View())
	if m.pickerIdx != 2 || !strings.Contains(view, "▸ omega") {
		t.Fatalf("down did not select omega:\n%s", view)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.model != "omega" {
		t.Errorf("model = %q, want omega", m.model)
	}
	if m.overlayOpen {
		t.Error("selecting a model should close the picker")
	}
}

func TestModelsPickerClickSelectsRow(t *testing.T) {
	m := newTestModel(t)
	models := []provider.ModelInfo{
		{ID: "alpha"},
		{ID: "demo-model"},
		{ID: "omega"},
	}
	m.openModelsPicker(models)

	m.View() // triggers zone.Scan(), registering row bounds
	z := waitForZone(t, pickerRowZoneID(2))

	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})

	if m.model != "omega" {
		t.Errorf("model = %q, want omega (clicked row 2)", m.model)
	}
	if m.overlayOpen {
		t.Error("clicking a model should close the picker")
	}
}

func TestProvidersPickerNavigatesAndSelects(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Providers = map[string]config.ProviderConfig{
		"alpha": {Type: "mock", DefaultModel: "alpha-model"},
		"mock":  {Type: "mock", DefaultModel: "demo-model"},
	}

	m.openProvidersPicker()
	if m.pickerIdx != 1 {
		t.Fatalf("initial picker index = %d, want active provider at 1", m.pickerIdx)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.pickerIdx != 0 {
		t.Fatalf("up picker index = %d, want 0", m.pickerIdx)
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.prov.Name() != "mock" || m.cfg.Provider != "alpha" {
		t.Errorf("selected provider = %q (config %q), want alpha", m.prov.Name(), m.cfg.Provider)
	}
	if m.model != "alpha-model" {
		t.Errorf("model = %q, want alpha-model", m.model)
	}
	if cmd == nil {
		t.Error("selecting a provider should trigger a health check")
	}
}

func TestProfilesPickerNavigatesAndPinsSelection(t *testing.T) {
	m := newTestModel(t)
	m.model = "qwen3:8b"
	m.profileMode = "auto"

	runCommand(m, "/profile list")
	if m.pickerKind != pickerProfile || !m.overlayOpen {
		t.Fatal("/profile list should open the profile picker")
	}
	if selected := m.pickerItems[m.pickerIdx]; selected != "qwen" {
		t.Fatalf("initial profile selection = %q, want auto-matched qwen", selected)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	want := m.pickerItems[m.pickerIdx]
	// The marker and the label are separate Render() calls; lipgloss v2
	// always emits color codes (no more auto-disable for a non-tty test
	// process), so strip them before checking the two land next to each
	// other as plain text.
	view := ansi.Strip(m.viewport.View())
	if want == "qwen" || !strings.Contains(view, "▸ "+want) {
		t.Fatalf("down did not move profile selection to %q:\n%s", want, view)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if m.profileMode != want {
		t.Errorf("profileMode = %q, want pinned profile %q", m.profileMode, want)
	}
	if m.overlayOpen || m.pickerKind != pickerNone {
		t.Error("selecting a profile should close and clear the picker")
	}
	if !strings.Contains(m.notice, "profile pinned to "+want) {
		t.Errorf("notice = %q, want profile confirmation", m.notice)
	}
}

func TestProfilePickerClickSelectsRow(t *testing.T) {
	m := newTestModel(t)
	m.model = "qwen3:8b"
	m.profileMode = "auto"

	runCommand(m, "/profile list")
	if m.pickerKind != pickerProfile || !m.overlayOpen {
		t.Fatal("/profile list should open the profile picker")
	}

	// Any row other than the auto-matched default, so a successful click
	// is unambiguous.
	targetIdx := (m.pickerIdx + 1) % len(m.pickerItems)
	target := m.pickerItems[targetIdx]

	m.View() // triggers zone.Scan(), registering row bounds
	z := waitForZone(t, pickerRowZoneID(targetIdx))

	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})

	if m.profileMode != target {
		t.Errorf("profileMode = %q, want clicked profile %q", m.profileMode, target)
	}
	if m.overlayOpen || m.pickerKind != pickerNone {
		t.Error("clicking a profile should close and clear the picker")
	}
	if !strings.Contains(m.notice, "profile pinned to "+target) {
		t.Errorf("notice = %q, want profile confirmation", m.notice)
	}
}

// waitForZone polls zone.Get: Scan() (called from View()) registers zone
// bounds via a background worker rather than synchronously, so a click
// immediately after View() in a test needs to wait for that to land — a
// real Program never hits this since Update always trails View by at least
// one event-loop tick.
func waitForZone(t *testing.T, id string) *zone.ZoneInfo {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if z := zone.Get(id); z != nil {
			return z
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("zone %q never registered", id)
	return nil
}

func TestPickerEscapeCancelsSelection(t *testing.T) {
	m := newTestModel(t)
	m.openModelsPicker([]provider.ModelInfo{{ID: "demo-model"}, {ID: "other"}})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})

	if m.model != "demo-model" {
		t.Errorf("model = %q after cancel, want demo-model", m.model)
	}
	if m.overlayOpen || m.pickerKind != pickerNone {
		t.Error("escape should close and clear the picker")
	}
}

func TestUnknownCommandShowsError(t *testing.T) {
	m := newTestModel(t)

	typeText(m, "/bogus")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(m.errText, "unknown command /bogus") {
		t.Errorf("errText = %q, want unknown command error", m.errText)
	}
}

func TestEscClearsSlashInput(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "/mod")

	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.input.Value() != "" || len(m.sugs) != 0 {
		t.Error("esc should clear the pending command and popup")
	}
}

func TestMouseWheelScrollsChatNotPrompt(t *testing.T) {
	m := newTestModel(t) // 80x24
	// Enough transcript that the chat viewport is scrollable.
	for i := 0; i < 60; i++ {
		m.session.AddAssistant("assistant line of chat output")
	}
	m.refreshViewport()
	m.viewport.GotoBottom()

	// A prompt taller than the input box, so the textarea's own internal
	// viewport is scrollable — the thing that used to scroll along with the
	// chat when the wheel was forwarded to both.
	m.input.SetValue(strings.Repeat("prompt line\n", 20))
	m.syncInputHeight()

	inputBefore := m.input.View()
	vpBefore := m.viewport.YOffset()

	for i := 0; i < 5; i++ {
		m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	}

	if m.viewport.YOffset() == vpBefore {
		t.Errorf("mouse wheel did not scroll the chat viewport (offset stayed %d)", vpBefore)
	}
	if m.input.View() != inputBefore {
		t.Error("mouse wheel scrolled the prompt box; it should scroll only the chat")
	}
}

func TestCtrlUClearsWholePrompt(t *testing.T) {
	m := newTestModel(t)
	// A multi-line prompt, the kind that is tedious to backspace away.
	m.input.SetValue("first line\nsecond line\nthird line")
	m.syncInputHeight()

	m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})

	if m.input.Value() != "" {
		t.Errorf("ctrl+u left content in the box: %q", m.input.Value())
	}
	if m.inputLines != 1 {
		t.Errorf("input box did not shrink back to 1 row: got %d", m.inputLines)
	}
}

func TestCtrlUClearsSlashSuggestions(t *testing.T) {
	m := newTestModel(t)
	typeText(m, "/mod")
	if len(m.sugs) == 0 {
		t.Fatal("expected command suggestions after typing /mod")
	}
	m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if m.input.Value() != "" || len(m.sugs) != 0 {
		t.Errorf("ctrl+u should clear input and suggestions: value=%q sugs=%d", m.input.Value(), len(m.sugs))
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
	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if m.inputLines <= before && before < 6 {
		t.Errorf("inputLines = %d, want growth after ctrl+j", m.inputLines)
	}

	// Sending resets the box to one row.
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.inputLines != 1 {
		t.Errorf("inputLines = %d, want 1 after send", m.inputLines)
	}
}

func TestCtrlSSavesSession(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.session.AddUser("hi")
	m.session.AddAssistant("hello")

	m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
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
	saved, err := history.Load(m.historyDir, m.sessionName)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Episode == nil || saved.Episode.Goal != "hi" || saved.Episode.Outcome != "hello" {
		t.Fatalf("explicit save episode = %+v", saved.Episode)
	}
}

func TestSaveDisabledShowsError(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = ""

	m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
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
	saved, err := history.Load(m.historyDir, m.sessionName)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Episode != nil {
		t.Fatalf("default auto-save captured episode = %+v", saved.Episode)
	}

	// Explicit episodic capture refreshes the summary during auto-save.
	m3 := newTestModel(t)
	m3.historyDir = t.TempDir()
	m3.cfg.Memory.Episodic.Capture = true
	m3.session.AddUser("captured on quit")
	m3.quit()
	saved, err = history.Load(m3.historyDir, m3.sessionName)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Episode == nil || saved.Episode.Goal != "captured on quit" {
		t.Fatalf("opt-in auto-save episode = %+v", saved.Episode)
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

func TestSigQuitMsgTriggersGracefulQuit(t *testing.T) {
	m := newTestModel(t)
	_, cmd := m.Update(sigQuitMsg{})
	if !m.quitting {
		t.Fatal("sigQuitMsg did not start the quit flow")
	}
	if cmd == nil {
		t.Fatal("sigQuitMsg must return the shutdown command")
	}
}

func TestFinishStreamAppendsUsageRecord(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.input.SetValue("hello")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	m.streamBuf.WriteString("reply")
	m.finishStream(&provider.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8}, false)

	records, err := history.ReadUsage(m.historyDir)
	if err != nil || len(records) != 1 {
		t.Fatalf("ReadUsage = (%v, %v), want one record", records, err)
	}
	if records[0].PromptTokens != 3 || records[0].CompletionTokens != 5 {
		t.Errorf("record = %+v", records[0])
	}
}

func TestFinishStreamWarnsOnTruncatedReply(t *testing.T) {
	m := newTestModel(t)
	m.streamBuf.WriteString("partial reply")
	m.finishStream(&provider.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8}, true)

	msgs := m.session.Messages
	if len(msgs) == 0 {
		t.Fatal("no assistant message was recorded")
	}
	last := msgs[len(msgs)-1]
	if !strings.Contains(last.Content, "partial reply") {
		t.Fatalf("assistant content lost the original reply: %q", last.Content)
	}
	if !strings.Contains(last.Content, "cut off by max_tokens") {
		t.Fatalf("assistant content missing truncation notice: %q", last.Content)
	}
}

func TestFinishStreamNoWarningWhenNotTruncated(t *testing.T) {
	m := newTestModel(t)
	m.streamBuf.WriteString("complete reply")
	m.finishStream(&provider.Usage{PromptTokens: 3, CompletionTokens: 5, TotalTokens: 8}, false)

	msgs := m.session.Messages
	if len(msgs) == 0 {
		t.Fatal("no assistant message was recorded")
	}
	last := msgs[len(msgs)-1]
	if strings.Contains(last.Content, "cut off by max_tokens") {
		t.Fatalf("assistant content unexpectedly has truncation notice: %q", last.Content)
	}
}

func TestHistoryOverlayListsSessions(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	m.session.AddUser("hi")
	m.saveWithNotice()

	typeText(m, "/history")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt})
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

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("first ctrl+c must not quit")
	}
	if !strings.Contains(m.notice, "again to exit") {
		t.Errorf("notice = %q, want arm hint", m.notice)
	}

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("second ctrl+c should quit")
	}
	if _, ok := cmd().(quitDoneMsg); !ok {
		t.Errorf("second ctrl+c returned %T, want quitDoneMsg", cmd())
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

	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
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
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.thinking {
		t.Fatal("should be thinking")
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
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
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.thinking {
		t.Fatal("model should be thinking after send")
	}
	_ = cmd

	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.thinking {
		t.Error("esc should stop generation")
	}
	if m.errText != "generation stopped" {
		t.Errorf("errText = %q, want generation stopped", m.errText)
	}
}

func TestStopButtonClickStopsGeneration(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("hello")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.thinking {
		t.Fatal("model should be thinking after send")
	}

	m.View() // triggers zone.Scan(), registering the stop button's bounds
	z := waitForZone(t, stopButtonZoneID)

	m.Update(tea.MouseReleaseMsg{X: z.StartX, Y: z.StartY, Button: tea.MouseLeft})

	if m.thinking {
		t.Error("clicking the stop button should stop generation")
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
