package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/cache"
	"github.com/patrikcze/llmtui/internal/contextmgr"
	"github.com/patrikcze/llmtui/internal/memory"
	"github.com/patrikcze/llmtui/internal/modelprofile"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/provider"
)

// debugInfo captures the last request for /debug last.
type debugInfo struct {
	When        time.Time
	RawMessage  string
	Provider    string
	Model       string
	Profile     string
	PromptMode  string
	Template    string
	Sections    []prompt.Section
	CtxDecision contextmgr.Decision
	CacheStatus string    // hit | miss | disabled | write
	CacheKey    cache.Key // snapshotted at dispatch so mid-stream /model or /provider changes cannot poison the cache
	Temperature float64
	MaxTokens   int
	Stream      bool
	Retries     int
	Duration    time.Duration
	Usage       *provider.Usage
}

// activeProfile resolves the model profile: pinned by /profile set, or
// matched from the model ID in auto mode. Config profiles win over built-ins.
func (m *Model) activeProfile() (modelprofile.Profile, bool) {
	if m.profileMode != "" && m.profileMode != "auto" {
		if p, ok := modelprofile.ByName(m.profiles, m.profileMode); ok {
			return p, true
		}
	}
	return modelprofile.Match(m.profiles, m.model)
}

// contextWindow resolves the window size: config override, then provider
// capabilities, then model profile, then a safe fallback.
// The source string feeds /doctor.
func (m *Model) contextWindow() (tokens int, source string) {
	if m.cfg.Context.MaxContextTokens > 0 {
		return m.cfg.Context.MaxContextTokens, "config"
	}
	if caps := provider.CapabilitiesOf(m.prov); caps.ContextWindowTokens > 0 {
		return caps.ContextWindowTokens, "provider"
	}
	prof, _ := m.activeProfile()
	if prof.ContextWindow > 0 {
		return prof.ContextWindow, "model profile " + prof.Name
	}
	return 8192, "fallback estimate"
}

// effectiveTemperature resolves temperature: template > profile > config.
func (m *Model) effectiveTemperature() float64 {
	if m.template != "" {
		if t, ok := m.cfg.Templates[m.template]; ok && t.Temperature > 0 {
			return t.Temperature
		}
	}
	if prof, matched := m.activeProfile(); matched && prof.PreferredTemperature > 0 {
		return prof.PreferredTemperature
	}
	return m.cfg.Chat.Temperature
}

// effectivePromptMode resolves prompt mode: /prompt mode > template > config.
func (m *Model) effectivePromptMode() string {
	if m.promptMode != "" {
		return m.promptMode
	}
	if m.template != "" {
		if t, ok := m.cfg.Templates[m.template]; ok && prompt.ValidMode(t.PromptMode) {
			return t.PromptMode
		}
	}
	if prompt.ValidMode(m.cfg.Prompt.Mode) {
		return m.cfg.Prompt.Mode
	}
	return prompt.ModeBalanced
}

// applyContext runs the context strategy over the session, updating the
// session summary when summarizing. It returns the messages to include as
// recent conversation (excluding the pending raw message).
func (m *Model) applyContext() ([]provider.Message, contextmgr.Decision) {
	window, _ := m.contextWindow()
	params := contextmgr.Params{
		Strategy:               m.ctxStrategy,
		ContextWindow:          window,
		ReserveResponseTokens:  m.cfg.Context.ReserveResponseTokens,
		SummarizeAfterMessages: m.cfg.Context.SummarizeAfterMessages,
	}
	decision := contextmgr.Decide(m.session.Messages, params)
	m.ctxUsed = decision.Used
	m.ctxWindow = window

	if !decision.Compress {
		_, recent := contextmgr.Split(m.session.Messages, len(m.session.Messages))
		return recent, decision
	}

	older, recent := contextmgr.Split(m.session.Messages, m.cfg.Context.KeepLastMessages)
	if decision.Strategy == contextmgr.StrategySummarize && len(older) > 0 {
		out, err := contextmgr.HeuristicSummarizer{}.Summarize(context.Background(), contextmgr.SummaryInput{
			Messages:  older,
			MaxTokens: m.cfg.Context.SummaryMaxTokens,
		})
		if err == nil && out.Summary != "" {
			if m.summary != "" {
				m.summary += "\n"
			}
			m.summary += out.Summary
		}
	}
	return recent, decision
}

// compose builds the provider-ready messages for a raw user message.
// preview=true composes without touching context state (for /prompt preview).
func (m *Model) compose(raw string, images []provider.Image, preview bool) (prompt.Output, contextmgr.Decision) {
	var (
		recent   []provider.Message
		decision contextmgr.Decision
	)
	if preview {
		window, _ := m.contextWindow()
		decision = contextmgr.Decide(m.session.Messages, contextmgr.Params{
			Strategy:               m.ctxStrategy,
			ContextWindow:          window,
			ReserveResponseTokens:  m.cfg.Context.ReserveResponseTokens,
			SummarizeAfterMessages: m.cfg.Context.SummarizeAfterMessages,
		})
		keep := len(m.session.Messages)
		if decision.Compress {
			keep = m.cfg.Context.KeepLastMessages
		}
		_, recent = contextmgr.Split(m.session.Messages, keep)
	} else {
		recent, decision = m.applyContext()
	}

	prof, _ := m.activeProfile()

	var memSnippets []string
	if m.memEnabled && m.memStore != nil {
		if snippets, err := m.memStore.Load(); err == nil {
			for _, sn := range memory.Relevant(snippets, raw, 3) {
				memSnippets = append(memSnippets, sn.Text)
			}
		}
	}

	systemPrompt := m.cfg.Chat.SystemPrompt
	templatePrompt := ""
	if m.template != "" {
		if t, ok := m.cfg.Templates[m.template]; ok {
			templatePrompt = t.SystemPrompt
		}
	}

	out := prompt.Compose(prompt.Input{
		RawMessage:     raw,
		Images:         images,
		SystemPrompt:   systemPrompt,
		TemplateName:   m.template,
		TemplatePrompt: templatePrompt,
		Mode:           m.effectivePromptMode(),
		HelperText:     m.cfg.Prompt.HelperText,
		ModelHints:     prompt.HintsForProfile(prof.PromptStyle, prof.ReasoningHint),
		SessionSummary: m.summary,
		MemorySnippets: memSnippets,
		RecentMessages: recent,
		Include: prompt.Include{
			SessionSummary:  m.cfg.Prompt.IncludeSessionSummary,
			LocalMemory:     m.cfg.Prompt.IncludeLocalMemory,
			ModelHints:      m.cfg.Prompt.IncludeModelHints,
			FormattingHints: m.cfg.Prompt.IncludeFormattingHints,
		},
	})
	return out, decision
}

// cacheKey builds the cache key for a raw message under current settings.
func (m *Model) cacheKey(raw string) cache.Key {
	_, pc, _ := m.cfg.ActiveProvider()
	return cache.Key{
		Provider:     m.prov.Name(),
		BaseURL:      pc.BaseURL,
		Model:        m.model,
		UserMessage:  raw,
		SystemPrompt: m.cfg.Chat.SystemPrompt,
		PromptMode:   m.effectivePromptMode(),
		Template:     m.template,
		Temperature:  m.effectiveTemperature(),
		TopP:         m.cfg.Chat.TopP,
		MaxTokens:    m.cfg.Chat.MaxTokens,
	}
}

// dispatch sends a raw user message through composition, cache, and the
// provider (with retry). Used by send() and /retry.
func (m *Model) dispatch(raw string, images []provider.Image) tea.Cmd {
	m.lastUserMsg = raw
	m.lastImages = images
	m.sentCount++

	// Cache lookup happens before composition mutates context state.
	key := m.cacheKey(raw)
	if m.responseCache != nil && m.responseCache.Enabled() && len(images) == 0 {
		if entry, ok := m.responseCache.Get(key); ok {
			m.session.AddUser(raw)
			m.session.AddAssistant(entry.Response)
			m.replyCount++
			st := m.session.RecordUsage(provider.Usage{
				PromptTokens:     entry.PromptTokens,
				CompletionTokens: entry.CompletionTokens,
				TotalTokens:      entry.PromptTokens + entry.CompletionTokens,
				Estimated:        entry.Estimated,
			}, 0)
			m.lastTPS = st.TokensPerSec
			m.notice = "cached response"
			m.lastDebug = debugInfo{
				When: time.Now(), RawMessage: raw, Provider: m.prov.Name(), Model: m.model,
				PromptMode: m.effectivePromptMode(), Template: m.template, CacheStatus: "hit",
			}
			m.refreshViewport()
			return nil
		}
	}

	composed, decision := m.compose(raw, images, false)
	m.session.AddUser(raw, images...)
	m.thinking = true
	m.streamBuf.Reset()
	m.reasoningLen = 0
	m.streamStart = time.Now()
	m.errText = ""
	m.refreshViewport()

	prof, _ := m.activeProfile()
	req := provider.ChatRequest{
		Model:       m.model,
		Messages:    composed.Messages,
		Temperature: m.effectiveTemperature(),
		TopP:        m.cfg.Chat.TopP,
		MaxTokens:   m.cfg.Chat.MaxTokens,
		Stream:      m.cfg.StreamEnabled(),
	}

	cacheStatus := "miss"
	if m.responseCache == nil || !m.responseCache.Enabled() {
		cacheStatus = "disabled"
	}
	m.lastDebug = debugInfo{
		When:        time.Now(),
		RawMessage:  raw,
		Provider:    m.prov.Name(),
		Model:       m.model,
		Profile:     prof.Name,
		PromptMode:  m.effectivePromptMode(),
		Template:    m.template,
		Sections:    composed.Sections,
		CtxDecision: decision,
		CacheStatus: cacheStatus,
		CacheKey:    key,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      req.Stream,
	}

	// A streaming reply can legitimately take many minutes on a slow local
	// model. A whole-request deadline would cut a healthy generation off
	// mid-answer, so network.timeout is treated as an *inactivity* window:
	// the watchdog fires only when no token has arrived for that long, and
	// handleStreamEvent resets it on every delta.
	idle := app.RequestTimeout(m.cfg.Network)
	ctx, cancel := context.WithCancelCause(context.Background())
	watchdog := time.AfterFunc(idle, func() { cancel(errStreamIdle) })
	m.streamCtx = ctx
	m.idleWatchdog = watchdog
	m.idleTimeout = idle
	m.cancelStream = func() {
		watchdog.Stop()
		cancel(context.Canceled)
	}
	prov := m.prov
	netCfg := m.cfg.Network
	baseURL := m.cfg.ActiveBaseURL()

	return func() tea.Msg {
		attempts := 1
		if netCfg.Retry.Enabled && netCfg.Retry.MaxAttempts > attempts {
			attempts = netCfg.Retry.MaxAttempts
		}
		var lastErr error
		for attempt := 1; attempt <= attempts; attempt++ {
			stream, err := prov.Chat(ctx, req)
			if err == nil {
				ev, ok := <-stream
				return firstStreamMsg{stream: stream, event: ev, ok: ok, retries: attempt - 1}
			}
			lastErr = err
			if ctx.Err() != nil || !provider.RetryableError(err) {
				break
			}
			select {
			case <-ctx.Done():
				attempt = attempts // stop retrying after cancellation
			case <-time.After(app.RetryBackoff(netCfg)):
			}
		}
		return streamEventMsg{event: provider.ChatEvent{Type: provider.EventError, Err: friendlyError(lastErr, prov.Name(), baseURL)}, ok: true}
	}
}

// friendlyError converts raw network errors into actionable guidance.
func friendlyError(err error, providerName, baseURL string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "no such host") {
		return fmt.Errorf("cannot connect to %s at %s — check that the server is running or change the provider base_url (%v)",
			providerName, baseURL, err)
	}
	if strings.Contains(msg, "context deadline exceeded") {
		return fmt.Errorf("%s did not respond within the configured network.timeout — the model may still be loading (%v)", providerName, err)
	}
	return err
}
