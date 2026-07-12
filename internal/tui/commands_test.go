package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/cache"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/provider"
)

func runCommand(m *Model, line string) {
	m.input.SetValue(line)
	m.updateSuggestions()
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

func TestCommandAliases(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()

	// /exit is an alias for /quit.
	m.input.SetValue("/exit")
	cmd := m.runSlashCommand()
	if cmd == nil {
		t.Fatal("/exit should resolve via alias to /quit")
	}
	if _, ok := cmd().(quitDoneMsg); !ok {
		t.Error("/exit should quit")
	}
}

func TestHelpGroupsByCategory(t *testing.T) {
	m := newTestModel(t)
	help := m.helpOverlay("")
	for _, cat := range []string{"chat", "provider", "model", "prompt", "context", "cache", "memory", "diagnostics", "session"} {
		if !strings.Contains(help, cat) {
			t.Errorf("help missing category %q", cat)
		}
	}
	// /help cache filters to the cache category.
	filtered := m.helpOverlay("cache")
	if !strings.Contains(filtered, "/cache") || strings.Contains(filtered, "/memory") {
		t.Error("/help cache should show only cache commands")
	}
}

func TestCacheCommands(t *testing.T) {
	m := newTestModel(t)
	m.responseCache = cache.New(t.TempDir(), time.Hour, 16, true)

	runCommand(m, "/cache off")
	if m.responseCache.Enabled() {
		t.Error("/cache off should disable the cache")
	}
	runCommand(m, "/cache on")
	if !m.responseCache.Enabled() {
		t.Error("/cache on should enable the cache")
	}
	runCommand(m, "/cache")
	if !m.overlayOpen || !strings.Contains(m.cacheOverlay(), "hits / misses") {
		t.Error("/cache should open the stats overlay")
	}
}

func TestCachedResponseRoundTrip(t *testing.T) {
	m := newTestModel(t)
	m.responseCache = cache.New(t.TempDir(), time.Hour, 16, true)

	// Simulate a completed exchange writing to the cache. dispatch snapshots
	// the cache key and attribution into lastDebug; finishStream uses only
	// that snapshot.
	m.lastUserMsg = "what is Go?"
	m.lastDebug = debugInfo{
		CacheKey: m.cacheKey("what is Go?", nil),
		Provider: m.prov.Name(),
		Model:    m.model,
		Stream:   m.cfg.StreamEnabled(),
	}
	m.streamBuf.WriteString("Go is a language.")
	m.thinking = true
	m.finishStream(&provider.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12})
	if m.lastDebug.CacheStatus != "write" {
		t.Fatalf("CacheStatus = %q, want write", m.lastDebug.CacheStatus)
	}

	// The cache key includes a fingerprint of the prior conversation, so a
	// repeat of the same message only hits cache under the same history this
	// simulated write used (empty). finishStream appended the simulated
	// assistant reply on its own (this test never called the real dispatch,
	// which would have added a matching user turn first); clear it here so
	// the repeat below sees the same empty history the write did.
	m.session.Clear()

	// The same message now answers from cache without a provider call.
	before := len(m.session.Messages)
	cmd := m.dispatch("what is Go?", nil)
	if cmd != nil {
		t.Fatal("cache hit should not dispatch a provider request")
	}
	if m.notice != "cached response" {
		t.Errorf("notice = %q, want cached response", m.notice)
	}
	if len(m.session.Messages) != before+2 {
		t.Error("cache hit should append user + assistant messages")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Content != "Go is a language." {
		t.Errorf("cached reply = %q", last.Content)
	}
}

// Switching the model while a reply streams must not store that reply under
// the new model's cache key (cache poisoning) or misattribute it.
func TestMidStreamModelSwitchDoesNotPoisonCache(t *testing.T) {
	m := newTestModel(t)
	m.responseCache = cache.New(t.TempDir(), time.Hour, 16, true)

	m.model = "model-a"
	keyA := m.cacheKey("hello", nil)
	m.lastUserMsg = "hello"
	m.lastDebug = debugInfo{CacheKey: keyA, Provider: m.prov.Name(), Model: "model-a", Stream: m.cfg.StreamEnabled()}
	m.streamBuf.WriteString("answer from model-a")
	m.thinking = true

	// /model is blocked while thinking; even a direct switch must not leak
	// into the finished exchange.
	runCommand(m, "/model model-b")
	if m.errText == "" {
		t.Error("/model should be rejected while a reply is streaming")
	}
	m.model = "model-b" // simulate any other path that changes the model

	m.finishStream(&provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3})

	if entry, ok := m.responseCache.Get(keyA); !ok {
		t.Fatal("reply should be cached under the dispatch-time key")
	} else if entry.Model != "model-a" {
		t.Errorf("cached entry attributed to %q, want model-a", entry.Model)
	}
	if _, ok := m.responseCache.Get(m.cacheKey("hello", nil)); ok {
		t.Error("reply must not be cached under the new model's key")
	}
}

func TestPromptModeCommand(t *testing.T) {
	m := newTestModel(t)
	runCommand(m, "/prompt mode strict")
	if m.effectivePromptMode() != prompt.ModeStrict {
		t.Errorf("mode = %q, want strict", m.effectivePromptMode())
	}
	runCommand(m, "/prompt mode bogus")
	if !strings.Contains(m.errText, "unknown prompt mode") {
		t.Errorf("errText = %q, want unknown mode error", m.errText)
	}
}

func TestPromptPreviewShowsSectionsWithoutSending(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("explain channels")
	before := len(m.session.Messages)

	content := m.promptPreviewOverlay(false)
	for _, want := range []string{"Raw User Message", "explain channels", "System Prompt"} {
		if !strings.Contains(content, want) {
			t.Errorf("preview missing %q", want)
		}
	}
	if len(m.session.Messages) != before {
		t.Error("preview must not modify the session")
	}
	if m.summary != "" {
		t.Error("preview must not build a summary")
	}
}

func TestProfileCommands(t *testing.T) {
	m := newTestModel(t)
	m.model = "qwen3:8b"

	prof, _ := m.activeProfile()
	if prof.Name != "qwen" {
		t.Fatalf("auto profile = %s, want qwen", prof.Name)
	}

	runCommand(m, "/profile set llama")
	prof, _ = m.activeProfile()
	if prof.Name != "llama" {
		t.Errorf("pinned profile = %s, want llama", prof.Name)
	}

	runCommand(m, "/profile auto")
	prof, _ = m.activeProfile()
	if prof.Name != "qwen" {
		t.Errorf("auto profile = %s, want qwen again", prof.Name)
	}

	runCommand(m, "/profile set nope")
	if !strings.Contains(m.errText, "no profile named") {
		t.Errorf("errText = %q", m.errText)
	}
}

func TestTemplateCommands(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Templates = map[string]config.TemplateConfig{
		"golang": {Description: "Go", SystemPrompt: "You are a Go expert.", PromptMode: "coding", Temperature: 0.25},
	}

	runCommand(m, "/template use golang")
	if m.template != "golang" {
		t.Fatalf("template = %q, want golang", m.template)
	}
	if m.effectivePromptMode() != "coding" {
		t.Errorf("template should set prompt mode, got %q", m.effectivePromptMode())
	}
	if m.effectiveTemperature() != 0.25 {
		t.Errorf("template should set temperature, got %v", m.effectiveTemperature())
	}

	runCommand(m, "/template clear")
	if m.template != "" {
		t.Error("/template clear should unset the template")
	}
}

func TestContextStrategyCommand(t *testing.T) {
	m := newTestModel(t)
	runCommand(m, "/context strategy summarize")
	if m.ctxStrategy != "summarize" {
		t.Errorf("strategy = %q, want summarize", m.ctxStrategy)
	}
	runCommand(m, "/context strategy bogus")
	if !strings.Contains(m.errText, "unknown strategy") {
		t.Errorf("errText = %q", m.errText)
	}
}

func TestMemoryCommands(t *testing.T) {
	m := newTestModel(t)
	// memStore configured in newTestModel via config path; ensure it exists
	if m.memStore == nil {
		t.Skip("memory store not configured in test model")
	}
	runCommand(m, "/memory add Prefer concise Go examples.")
	if !strings.Contains(m.notice, "remembered") {
		t.Fatalf("notice = %q", m.notice)
	}
	snippets, _ := m.memStore.Load()
	if len(snippets) != 1 {
		t.Fatalf("snippets = %d, want 1", len(snippets))
	}

	runCommand(m, "/memory on")
	if !m.memEnabled {
		t.Error("/memory on should enable")
	}

	// With memory enabled, a relevant message pulls the snippet into the prompt.
	out, _ := m.compose("give me a Go example", nil, true)
	found := false
	for _, s := range out.Sections {
		if s.Title == "Relevant Memory" && strings.Contains(s.Content, "concise Go examples") {
			found = true
		}
	}
	if !found {
		t.Error("relevant memory snippet missing from composition")
	}

	runCommand(m, "/memory off")
	out, _ = m.compose("give me a Go example", nil, true)
	for _, s := range out.Sections {
		if s.Title == "Relevant Memory" {
			t.Error("disabled memory must not appear in composition")
		}
	}
}

func TestKeysCommandEntersInspector(t *testing.T) {
	m := newTestModel(t)
	runCommand(m, "/keys")
	if !m.keysMode {
		t.Fatal("/keys should enter the inspector")
	}
	// Keys are logged, not executed.
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlL})
	if len(m.keyLog) != 1 || !strings.Contains(m.keyLog[0], "ctrl+l") {
		t.Errorf("keyLog = %v", m.keyLog)
	}
	// Shift+enter sequences show up by name.
	m.Update(fakeCSI("27;2;13~"))
	if !strings.Contains(strings.Join(m.keyLog, "|"), "shift+enter") {
		t.Errorf("keyLog = %v, want shift+enter entry", m.keyLog)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.keysMode {
		t.Error("esc should exit the inspector")
	}
}

func TestRetryCommand(t *testing.T) {
	m := newTestModel(t)

	runCommand(m, "/retry")
	if !strings.Contains(m.errText, "nothing to retry") {
		t.Errorf("errText = %q", m.errText)
	}

	m.lastUserMsg = "hello again"
	cmd := m.retryLast()
	if cmd == nil {
		t.Fatal("retry should dispatch a request")
	}
	if !m.thinking {
		t.Error("retry should start thinking")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Content != "hello again" {
		t.Errorf("retried message = %q", last.Content)
	}
}

func TestConfigCommandRedactsSecrets(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Providers = map[string]config.ProviderConfig{
		"lmstudio": {Type: "openai_compatible", APIKey: "super-secret-key-value"},
	}
	content := m.configOverlay()
	if strings.Contains(content, "super-secret-key-value") {
		t.Error("config overlay must redact API keys")
	}
}

func TestUsageResetCommand(t *testing.T) {
	m := newTestModel(t)
	m.session.RecordUsage(provider.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10}, time.Second)

	runCommand(m, "/usage reset")
	if m.session.TotalTokens() != 0 || len(m.session.Stats) != 0 {
		t.Error("/usage reset should clear session counters")
	}
}

func TestDebugCommands(t *testing.T) {
	m := newTestModel(t)
	runCommand(m, "/debug on")
	if !m.debugMode {
		t.Error("/debug on should enable debug mode")
	}
	runCommand(m, "/debug last")
	if !m.overlayOpen {
		t.Error("/debug last should open the overlay")
	}
	if !strings.Contains(m.debugOverlay(), "no request yet") {
		t.Error("empty debug overlay should say so")
	}
}
