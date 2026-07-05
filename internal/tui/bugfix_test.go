package tui

// Regression tests for bugs found in the 2026-07 review: suggestion priority,
// mid-stream command blocking, provider-switch state sync, health-check
// fallback rules, overlay stomping, and history load adoption.

import (
	"errors"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
)

// Typing "/model" exactly must run /model, not the earlier-registered
// "/models" that happens to share the prefix.
func TestExactCommandNameWinsOverSuggestion(t *testing.T) {
	m := newTestModel(t)
	m.input.SetValue("/model")
	m.updateSuggestions()
	if len(m.sugs) == 0 || m.sugs[0].name != "model" {
		t.Fatalf("exact match should lead suggestions, got %v", func() []string {
			names := make([]string, len(m.sugs))
			for i, c := range m.sugs {
				names[i] = c.name
			}
			return names
		}())
	}

	m.input.SetValue("/model")
	m.updateSuggestions()
	m.runSlashCommand()
	// /model without args reports its own usage line — proof it ran /model.
	if !strings.Contains(m.errText, "usage: /model") {
		t.Errorf("errText = %q, want /model usage message", m.errText)
	}
}

func TestMutatingCommandsBlockedWhileThinking(t *testing.T) {
	m := newTestModel(t)
	m.session.AddUser("hi")
	m.thinking = true

	for _, line := range []string{"/clear", "/provider switch mock", "/model other"} {
		before := len(m.session.Messages)
		runCommand(m, line)
		if m.errText == "" {
			t.Errorf("%s should be rejected while thinking", line)
		}
		if len(m.session.Messages) != before {
			t.Errorf("%s mutated the session while thinking", line)
		}
		m.errText = ""
	}
}

// Switching providers in the TUI must update the config's active provider,
// so cache keys, /doctor, and error messages resolve the right settings.
func TestSwitchProviderSyncsConfig(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Providers = map[string]config.ProviderConfig{
		"mock":  {Type: "mock", DefaultModel: "demo-model"},
		"local": {Type: "openai_compatible", BaseURL: "http://localhost:9999/v1", DefaultModel: "m1"},
	}
	m.cfg.BaseURL = "http://old-override"
	m.cfg.APIKey = "old-key"

	if cmd := m.switchProvider("local"); cmd == nil {
		t.Fatal("switchProvider should schedule a health check")
	}
	if got := m.cfg.ActiveProviderName(); got != "local" {
		t.Errorf("ActiveProviderName = %q, want local", got)
	}
	if m.cfg.BaseURL != "" || m.cfg.APIKey != "" {
		t.Error("launch-time base-url/api-key overrides must be cleared on switch")
	}
	if got := m.cfg.ActiveBaseURL(); got != "http://localhost:9999/v1" {
		t.Errorf("ActiveBaseURL = %q, want the switched provider's URL", got)
	}
}

// A failed mid-session health check must not hijack the session into demo
// mode, and results from a previously active provider must be discarded.
func TestHealthCheckFailureRules(t *testing.T) {
	m := newTestModel(t)
	m.model = "my-model"

	// Stale result: ignored entirely.
	m.Update(healthMsg{err: errors.New("down"), provider: "someone-else", initial: false})
	if m.demoMode || m.errText != "" {
		t.Error("stale health result must be ignored")
	}

	// Mid-session failure: stay on the chosen provider and model.
	m.Update(healthMsg{err: errors.New("down"), provider: m.prov.Name(), initial: false})
	if m.demoMode {
		t.Error("mid-session health failure must not switch to demo mode")
	}
	if m.model != "my-model" {
		t.Errorf("model changed to %q on mid-session health failure", m.model)
	}
	if m.connected {
		t.Error("connected should be false after a failed check")
	}

	// Startup failure: demo fallback is allowed.
	m.Update(healthMsg{err: errors.New("down"), provider: m.prov.Name(), initial: true})
	if !m.demoMode || m.model != "demo-model" {
		t.Error("startup health failure should fall back to demo mode")
	}
}

// Async events (health results, stream progress) must not replace the
// content of an open overlay.
func TestAsyncEventsDoNotStompOverlay(t *testing.T) {
	m := newTestModel(t)
	m.openOverlay("OVERLAY-MARKER")

	m.Update(healthMsg{err: nil, provider: m.prov.Name(), initial: false})
	m.thinking = true
	m.streamBuf.WriteString("delta text")
	m.refreshViewport()

	if !strings.Contains(m.viewport.View(), "OVERLAY-MARKER") {
		t.Error("overlay content was replaced by an async refresh")
	}
	m.thinking = false
	m.closeOverlay()
	if strings.Contains(m.viewport.View(), "OVERLAY-MARKER") {
		t.Error("closing the overlay should restore the chat view")
	}
}

// /history load adopts the loaded session's name and token totals, so later
// saves update the same file instead of duplicating it under a new name.
func TestHistoryLoadAdoptsSession(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()

	saved := history.Session{
		Provider: "mock",
		Model:    "demo-model",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "old question"},
			{Role: provider.RoleAssistant, Content: "old answer"},
		},
		Prompt: 11,
		Reply:  22,
	}
	if _, err := history.Save(m.historyDir, "session-old", saved); err != nil {
		t.Fatal(err)
	}

	runCommand(m, "/history load session-old")
	if m.sessionName != "session-old" {
		t.Errorf("sessionName = %q, want session-old", m.sessionName)
	}
	if m.session.TotalPromptTokens != 11 || m.session.TotalCompletionTokens != 22 {
		t.Errorf("totals = %d/%d, want 11/22", m.session.TotalPromptTokens, m.session.TotalCompletionTokens)
	}

	// Traversal attempts are rejected.
	runCommand(m, "/history load ../escape")
	if m.errText == "" || !strings.Contains(m.errText, "invalid session name") {
		t.Errorf("path traversal should be rejected, errText = %q", m.errText)
	}
}

// --resume/--continue (llmtui chat flags) seed a fresh Model with a saved
// session via Options.ResumeSession, using the same adoption logic as
// /history load above (TestHistoryLoadAdoptsSession).
func TestResumeOptionAdoptsSession(t *testing.T) {
	cfg := &config.Config{
		Chat:    config.ChatConfig{Stream: true, MaxTokens: 128, SystemPrompt: "You are a helpful local assistant."},
		Network: config.NetworkConfig{Timeout: "120s", ConnectTimeout: "10s"},
		Cache:   config.CacheConfig{TTL: "1h", MaxSizeMB: 16},
	}
	saved := history.Session{
		Provider: "mock",
		Model:    "demo-model",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "old question"},
			{Role: provider.RoleAssistant, Content: "old answer"},
		},
		Prompt: 11,
		Reply:  22,
	}
	m := New(Options{
		Config:            cfg,
		Provider:          mock.New(),
		Model:             "demo-model",
		ResumeSession:     &saved,
		ResumeSessionName: "session-old",
	})
	if m.sessionName != "session-old" {
		t.Errorf("sessionName = %q, want session-old", m.sessionName)
	}
	if len(m.session.Messages) != 2 {
		t.Fatalf("Messages = %d, want 2", len(m.session.Messages))
	}
	if m.session.TotalPromptTokens != 11 || m.session.TotalCompletionTokens != 22 {
		t.Errorf("totals = %d/%d, want 11/22", m.session.TotalPromptTokens, m.session.TotalCompletionTokens)
	}
	if m.notice == "" || !strings.Contains(m.notice, "session-old") {
		t.Errorf("notice = %q, want a resume confirmation mentioning session-old", m.notice)
	}
}

// A fresh Model with no ResumeSession must be unaffected: it starts with
// just the configured system prompt and no leftover notice.
func TestNoResumeOptionStartsFreshSession(t *testing.T) {
	m := newTestModel(t)
	if len(m.session.Messages) != 1 || m.session.Messages[0].Role != provider.RoleSystem {
		t.Errorf("Messages = %+v, want just the configured system prompt", m.session.Messages)
	}
	if m.notice != "" {
		t.Errorf("notice = %q, want empty when nothing was resumed", m.notice)
	}
}
