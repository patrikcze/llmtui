package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	cfg := &config.Config{
		Chat: config.ChatConfig{Stream: true, MaxTokens: 128},
		UI:   config.UIConfig{Markdown: false},
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
	if help := m.helpOverlay(); !strings.Contains(help, "/model") || !strings.Contains(help, "/provider") {
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
