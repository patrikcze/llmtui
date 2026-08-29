package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/cache"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/memoryindex"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/provider"
)

func runCommand(m *Model, line string) {
	m.input.SetValue(line)
	m.updateSuggestions()
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
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

func TestThinkingIsAnAliasForThoughts(t *testing.T) {
	m := newTestModel(t)
	m.showReasoning = false

	runCommand(m, "/thinking show")

	if !m.showReasoning {
		t.Fatal("/thinking show should resolve via alias to /thoughts show")
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
	m.finishStream(&provider.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12}, false)
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

func TestCacheReadFailureIsVisibleAndFallsBackToProvider(t *testing.T) {
	m := newTestModel(t)
	dir := t.TempDir()
	m.responseCache = cache.New(dir, time.Hour, 16, true)
	key := m.cacheKey("hello", nil)
	if err := os.WriteFile(filepath.Join(dir, key.Hash()+".json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt cache entry: %v", err)
	}

	cmd := m.dispatch("hello", nil)
	if cmd == nil {
		t.Fatal("cache corruption must fall back to a provider request")
	}
	if !strings.Contains(m.errText, "cache read failed") {
		t.Errorf("errText = %q, want visible cache read failure", m.errText)
	}
	if m.lastDebug.CacheStatus != "error" {
		t.Errorf("CacheStatus = %q, want error", m.lastDebug.CacheStatus)
	}
	if m.cancelStream != nil {
		m.cancelStream()
	}
}

func TestCacheWriteFailureIsVisible(t *testing.T) {
	m := newTestModel(t)
	cachePath := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(cachePath, []byte("file"), 0o600); err != nil {
		t.Fatalf("write cache path fixture: %v", err)
	}
	m.responseCache = cache.New(cachePath, time.Hour, 16, true)
	m.lastDebug = debugInfo{
		CacheKey:    m.cacheKey("hello", nil),
		CacheStatus: "miss",
		Provider:    m.prov.Name(),
		Model:       m.model,
		Stream:      true,
	}
	m.streamBuf.WriteString("successful provider response")
	m.thinking = true
	m.finishStream(&provider.Usage{PromptTokens: 2, CompletionTokens: 3}, false)

	if !strings.Contains(m.errText, "cache write failed") {
		t.Errorf("errText = %q, want visible cache write failure", m.errText)
	}
	if m.lastDebug.CacheStatus != "write error" {
		t.Errorf("CacheStatus = %q, want write error", m.lastDebug.CacheStatus)
	}
	if got := m.responseCache.Stats().LastError; got == "" {
		t.Error("/cache statistics should retain the last write error")
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

	m.finishStream(&provider.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}, false)

	if entry, ok, err := m.responseCache.Get(keyA); err != nil || !ok {
		t.Fatal("reply should be cached under the dispatch-time key")
	} else if entry.Model != "model-a" {
		t.Errorf("cached entry attributed to %q, want model-a", entry.Model)
	}
	if _, ok, err := m.responseCache.Get(m.cacheKey("hello", nil)); err != nil || ok {
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
		if s.Title == "Active Context" && strings.Contains(s.Content, "concise Go examples") {
			found = true
		}
	}
	if !found {
		t.Error("relevant memory snippet missing from composition")
	}

	runCommand(m, "/memory off")
	out, _ = m.compose("give me a Go example", nil, true)
	for _, s := range out.Sections {
		if s.Title == "Active Context" && strings.Contains(s.Content, "concise Go examples") {
			t.Error("disabled memory must not appear in composition")
		}
	}
}

func TestTypedProjectMemoryCommands(t *testing.T) {
	m := newTestModel(t)
	if m.projectStore == nil {
		t.Fatal("project memory store is not configured")
	}

	cmdMemory(m, "add user Prefer concise Go examples.")
	cmdMemory(m, "add project architecture Use hexagonal architecture for Go services.")
	cmdMemory(m, "add project convention Run gofmt before committing.")
	cmdMemory(m, "add project decision Use PostgreSQL for durable storage.")
	if m.errText != "" {
		t.Fatalf("memory add error = %q", m.errText)
	}

	userRecords, err := m.memStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(userRecords) != 1 || userRecords[0].Text != "Prefer concise Go examples." {
		t.Fatalf("user records = %+v", userRecords)
	}
	projectRecords, err := m.projectStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(projectRecords) != 3 {
		t.Fatalf("project records = %+v", projectRecords)
	}

	projectList := m.memoryListOverlay("project")
	for _, want := range []string{"project_architecture", "project_convention", "project_decision", "PostgreSQL"} {
		if !strings.Contains(projectList, want) {
			t.Errorf("project list missing %q:\n%s", want, projectList)
		}
	}
	if strings.Contains(projectList, "Prefer concise") {
		t.Fatalf("project-only list included user memory:\n%s", projectList)
	}

	cmdMemory(m, "inspect "+projectRecords[2].ID)
	inspect := m.viewport.View()
	for _, want := range []string{"project_decision", "user_authored", "PostgreSQL"} {
		if !strings.Contains(inspect, want) {
			t.Errorf("inspect overlay missing %q:\n%s", want, inspect)
		}
	}

	cmdMemory(m, "remove "+projectRecords[2].ID)
	remaining, err := m.projectStore.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 {
		t.Fatalf("remaining project records = %d, want 2", len(remaining))
	}
}

func TestMemorySearchAndExplainCommands(t *testing.T) {
	m := newTestModel(t)
	if _, err := m.memStore.Add("Prefer PostgreSQL examples in Go."); err != nil {
		t.Fatal(err)
	}
	if _, err := m.projectStore.Add(memoryindex.KindProjectDecision, "Use PostgreSQL for durable storage."); err != nil {
		t.Fatal(err)
	}

	search, err := m.memorySearchOverlay("PostgreSQL storage", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(search, "user_preference") || !strings.Contains(search, "project_decision") {
		t.Fatalf("search did not include both memory tiers:\n%s", search)
	}
	explain, err := m.memorySearchOverlay("PostgreSQL storage", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "lexical") || !strings.Contains(explain, "score") {
		t.Fatalf("explain lacks ranking details:\n%s", explain)
	}

	cmdMemory(m, "search PostgreSQL storage")
	if !m.overlayOpen || !strings.Contains(m.viewport.View(), "memory search") {
		t.Fatal("/memory search did not open its results overlay")
	}
}

func TestMemoryExplainShowsBudgetRejectionsWithoutRejectedText(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Memory.Retrieval.MaxContextTokens = 24
	rejectedText := strings.Repeat("oversized private context ", 40)
	if _, err := m.memStore.Add(rejectedText); err != nil {
		t.Fatal(err)
	}

	explain, err := m.memorySearchOverlay("oversized private context", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "rejected candidates") || !strings.Contains(explain, "total_budget") {
		t.Fatalf("explain omitted budget rejection:\n%s", explain)
	}
	if strings.Contains(explain, rejectedText) {
		t.Fatalf("explain printed full rejected content:\n%s", explain)
	}
}

func TestDebugLastShowsContentFreeRetrievalDiagnostics(t *testing.T) {
	m := newTestModel(t)
	m.lastDebug = debugInfo{
		When: time.Now(),
		MemoryRetrieval: memoryRetrievalDiagnostics{
			Enabled: true, Duration: 2 * time.Millisecond, Selected: 2,
			TotalTokens: 120, MaxTokens: 1800,
			TierTokens:      map[string]int{"project": 70, "source": 50},
			RejectedReasons: map[string]int{"content_hash_dedup": 1, "total_budget": 2},
		},
	}
	overlay := m.debugOverlay()
	for _, want := range []string{"memory retrieval diagnostics", "120 / 1800", "project=70", "source=50", "content_hash_dedup=1", "total_budget=2"} {
		if !strings.Contains(overlay, want) {
			t.Errorf("debug overlay missing %q:\n%s", want, overlay)
		}
	}
	if strings.Contains(overlay, "rejected secret text") {
		t.Fatalf("debug overlay included raw rejected content:\n%s", overlay)
	}
}

func TestMemoryOverlaysSanitizeStoredTerminalContent(t *testing.T) {
	m := newTestModel(t)
	if _, err := m.memStore.Add("safe\x1b]52;c;Y2xpcA==\x07 preference"); err != nil {
		t.Fatal(err)
	}
	overlay := m.memoryListOverlay("user")
	plain := ansi.Strip(overlay)
	if strings.ContainsRune(plain, '\x1b') || strings.ContainsRune(plain, '\x07') || strings.Contains(plain, "Y2xpcA") {
		t.Fatalf("memory overlay retained terminal control bytes: %q", overlay)
	}
}

func TestMemoryListEpisodeAndRunTiers(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	session := history.Session{
		Provider:  "mock",
		Model:     "model",
		ProjectID: m.projectID,
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "remember this episode"}},
	}
	session.Episode = history.BuildEpisode(session)
	if _, err := history.Save(m.historyDir, "saved-episode", session); err != nil {
		t.Fatal(err)
	}
	if got := m.memoryListOverlay("episode"); !strings.Contains(got, "saved-episode") || !strings.Contains(got, "remember this episode") {
		t.Fatalf("episode list omitted saved summary:\n%s", got)
	}
	m.agentLoop.run = &agent.AgentRun{
		ID: "run-memory", Status: agent.DecisionDone, Objective: "ship run memory",
		Evidence: []agent.EvidenceItem{{Cycle: 1, Kind: agent.EvidenceTest, Source: "go test", Summary: "passed", Success: true}},
	}
	if got := m.memoryListOverlay("run"); !strings.Contains(got, "ship run memory") || !strings.Contains(got, "agent_evidence") {
		t.Fatalf("run list omitted bounded current-run state:\n%s", got)
	}
}

func TestKeysCommandEntersInspector(t *testing.T) {
	m := newTestModel(t)
	runCommand(m, "/keys")
	if !m.keys.keysMode {
		t.Fatal("/keys should enter the inspector")
	}
	// Keys are logged, not executed.
	m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if len(m.keys.keyLog) != 1 || !strings.Contains(m.keys.keyLog[0], "ctrl+l") {
		t.Errorf("keyLog = %v", m.keys.keyLog)
	}
	// Shift+enter sequences show up by name.
	m.Update(fakeCSI("27;2;13~"))
	if !strings.Contains(strings.Join(m.keys.keyLog, "|"), "shift+enter") {
		t.Errorf("keyLog = %v, want shift+enter entry", m.keys.keyLog)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.keys.keysMode {
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
	m.cfg.MCP.Servers = map[string]config.MCPServerConfig{
		"jira": {Env: map[string]string{"TOKEN": "mcp-secret-marker"}},
	}
	content := m.configOverlay()
	for _, secret := range []string{"super-secret-key-value", "mcp-secret-marker"} {
		if strings.Contains(content, secret) {
			t.Errorf("config overlay leaked %q", secret)
		}
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

func TestThinkCommand(t *testing.T) {
	m := newTestModel(t)
	runCommand(m, "/think off")
	if m.reasoningMode != "off" {
		t.Fatalf("reasoningMode = %q", m.reasoningMode)
	}
	runCommand(m, "/think auto")
	if m.reasoningMode != "auto" {
		t.Fatalf("reasoningMode = %q", m.reasoningMode)
	}
	runCommand(m, "/think banana")
	if m.errText == "" {
		t.Fatal("invalid mode must set an error")
	}
}

func TestThoughtsCommandDoesNotChangeReasoningMode(t *testing.T) {
	m := newTestModel(t)
	m.reasoningMode = "on"

	runCommand(m, "/thoughts hide")
	if m.showReasoning {
		t.Fatal("/thoughts hide should hide captured reasoning")
	}
	if m.reasoningMode != "on" {
		t.Fatalf("display command changed reasoning mode to %q", m.reasoningMode)
	}

	runCommand(m, "/thoughts toggle")
	if !m.showReasoning {
		t.Fatal("/thoughts toggle should show captured reasoning")
	}

	runCommand(m, "/thoughts banana")
	if !strings.Contains(m.errText, "usage: /thoughts") {
		t.Fatalf("invalid mode error = %q", m.errText)
	}
}
