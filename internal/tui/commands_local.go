package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/contextmgr"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tui/components"
)

func (m *Model) fail(msg string) tea.Cmd {
	m.errText = msg
	m.refreshViewport()
	return nil
}

func (m *Model) kv(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "  %s %s\n",
		m.theme.StatusBar.Render(fmt.Sprintf("%-18s", key)),
		m.theme.StatusValue.Render(value))
}

func (m *Model) overlayFooter(b *strings.Builder) string {
	b.WriteString("\n" + m.theme.SystemNote.Render("esc to close"))
	return b.String()
}

// --- /provider -------------------------------------------------------------

func cmdProvider(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	switch sub {
	case "", "list":
		m.openOverlay(m.providersOverlay())
		return nil
	case "switch":
		return m.switchProvider(rest)
	default:
		// `/provider ollama` is shorthand for switch.
		return m.switchProvider(sub)
	}
}

// --- /cache ----------------------------------------------------------------

func cmdCache(m *Model, args string) tea.Cmd {
	if m.responseCache == nil {
		return m.fail("cache is not configured (cache.path)")
	}
	sub, _ := splitArgs(args)
	switch sub {
	case "", "stats":
		m.openOverlay(m.cacheOverlay())
	case "clear":
		removed, err := m.responseCache.Clear()
		if err != nil {
			return m.fail("cache clear: " + err.Error())
		}
		m.notice = fmt.Sprintf("cache cleared (%d entries removed)", removed)
	case "on":
		m.responseCache.SetEnabled(true)
		m.notice = "response cache enabled"
	case "off":
		m.responseCache.SetEnabled(false)
		m.notice = "response cache disabled"
	default:
		return m.fail("usage: /cache [stats|clear|on|off]")
	}
	return nil
}

func (m *Model) cacheOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("response cache") + "\n\n")
	s := m.responseCache.Stats()
	state := "off"
	if s.Enabled {
		state = "on"
	}
	m.kv(&b, "state", state)
	m.kv(&b, "entries", fmt.Sprintf("%d", s.Entries))
	m.kv(&b, "size", fmt.Sprintf("%.1f MB of %d MB max", float64(s.SizeBytes)/1024/1024, m.cfg.Cache.MaxSizeMB))
	m.kv(&b, "ttl", m.cfg.Cache.TTL)
	m.kv(&b, "hits / misses", fmt.Sprintf("%d / %d (this session)", s.Hits, s.Misses))
	m.kv(&b, "streamed", fmt.Sprintf("%v (cache.cache_streamed_responses)", m.cfg.Cache.CacheStreamedResponses))
	b.WriteString("\n" + m.theme.StatusBar.Render("  keyed by provider, base URL, model, message, system prompt,\n  prompt mode, template, temperature — never by API keys") + "\n")
	b.WriteString("\n" + m.theme.SystemNote.Render("/cache clear · /cache on|off"))
	return m.overlayFooter(&b)
}

// --- /profile ----------------------------------------------------------------

func cmdProfile(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	switch sub {
	case "", "inspect":
		m.openOverlay(m.profileOverlay())
	case "list":
		m.openOverlay(m.profileListOverlay())
	case "auto":
		m.profileMode = "auto"
		prof, _ := m.activeProfile()
		m.notice = "profile matching set to auto (currently " + prof.Name + ")"
	case "set":
		if rest == "" {
			return m.fail("usage: /profile set <name> (see /profile list)")
		}
		if _, ok := modelprofileByName(m, rest); !ok {
			return m.fail(fmt.Sprintf("no profile named %q (see /profile list)", rest))
		}
		m.profileMode = rest
		m.notice = "profile pinned to " + rest
	default:
		return m.fail("usage: /profile [list|auto|set <name>|inspect]")
	}
	return nil
}

func (m *Model) profileOverlay() string {
	prof, matched := m.activeProfile()
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("model profile") + "\n\n")
	mode := m.profileMode
	if mode == "" {
		mode = "auto"
	}
	m.kv(&b, "mode", mode)
	m.kv(&b, "active profile", prof.Name)
	matchedStr := "no (using defaults)"
	if matched {
		matchedStr = "yes — model " + m.model
	}
	m.kv(&b, "matched", matchedStr)
	m.kv(&b, "context window", fmt.Sprintf("%d tokens", prof.ContextWindow))
	m.kv(&b, "temperature", fmt.Sprintf("%.2f (effective: %.2f)", prof.PreferredTemperature, m.effectiveTemperature()))
	m.kv(&b, "prompt style", prof.PromptStyle)
	m.kv(&b, "JSON mode", fmt.Sprintf("%v", prof.SupportsJSONMode))
	m.kv(&b, "reasoning hint", fmt.Sprintf("%v", prof.ReasoningHint))
	b.WriteString("\n" + m.theme.SystemNote.Render("/profile set <name> · /profile auto · /profile list"))
	return m.overlayFooter(&b)
}

func (m *Model) profileListOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("model profiles") + "\n\n")
	active, _ := m.activeProfile()
	for _, p := range m.profiles {
		marker := "  "
		name := m.theme.StatusValue.Render(fmt.Sprintf("%-10s", p.Name))
		if p.Name == active.Name {
			marker = m.theme.BadgeOK.Render("▸ ")
			name = m.theme.BadgeOK.Render(fmt.Sprintf("%-10s", p.Name))
		}
		fmt.Fprintf(&b, "%s%s %s\n", marker, name,
			m.theme.StatusBar.Render(fmt.Sprintf("ctx %s · temp %.2f · %s · matches: %s",
				components.FormatTokens(p.ContextWindow), p.PreferredTemperature, p.PromptStyle, strings.Join(p.Match, ", "))))
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("custom profiles come from model_profiles in the config"))
	return m.overlayFooter(&b)
}

func modelprofileByName(m *Model, name string) (any, bool) {
	for _, p := range m.profiles {
		if p.Name == name {
			return p, true
		}
	}
	if name == "default" {
		return nil, true
	}
	return nil, false
}

// --- /prompt -----------------------------------------------------------------

func cmdPrompt(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	switch sub {
	case "":
		m.openOverlay(m.promptOverlay())
	case "preview", "composed":
		m.openOverlay(m.promptPreviewOverlay(false))
	case "raw":
		m.openOverlay(m.promptPreviewOverlay(true))
	case "mode":
		if rest == "" {
			m.notice = "prompt mode: " + m.effectivePromptMode() + " (set with /prompt mode minimal|balanced|coding|strict)"
			return nil
		}
		if !prompt.ValidMode(rest) {
			return m.fail("unknown prompt mode " + rest + " (minimal|balanced|coding|strict)")
		}
		m.promptMode = rest
		m.notice = "prompt mode set to " + rest
	default:
		return m.fail("usage: /prompt [preview|raw|composed|mode <m>]")
	}
	return nil
}

func (m *Model) promptOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("prompt composition") + "\n\n")
	m.kv(&b, "mode", m.effectivePromptMode())
	m.kv(&b, "template", orNone(m.template))
	m.kv(&b, "session summary", onOff(m.cfg.Prompt.IncludeSessionSummary))
	m.kv(&b, "local memory", onOff(m.cfg.Prompt.IncludeLocalMemory)+memState(m))
	m.kv(&b, "model hints", onOff(m.cfg.Prompt.IncludeModelHints))
	m.kv(&b, "formatting hints", onOff(m.cfg.Prompt.IncludeFormattingHints))
	b.WriteString("\n" + m.theme.StatusBar.Render("  The raw user message is never rewritten; helpers are separate,\n  inspectable sections. See /prompt preview.") + "\n")
	b.WriteString("\n" + m.theme.SystemNote.Render("/prompt preview · /prompt raw · /prompt mode <m>"))
	return m.overlayFooter(&b)
}

// promptPreviewOverlay shows what the next message would send. rawOnly
// shows just the raw user message part.
func (m *Model) promptPreviewOverlay(rawOnly bool) string {
	pending := strings.TrimSpace(m.input.Value())
	if pending == "" {
		pending = "<your next message>"
	}
	out, decision := m.compose(pending, nil, true)

	var b strings.Builder
	title := "prompt preview (not sent)"
	if rawOnly {
		title = "raw user message (not sent)"
	}
	b.WriteString(m.theme.Badge.Render(title) + "\n\n")

	for _, s := range out.Sections {
		if rawOnly && s.Title != "Raw User Message" {
			continue
		}
		b.WriteString(m.theme.UserLabel.Render(s.Title) + "\n")
		for _, line := range strings.Split(s.Content, "\n") {
			b.WriteString("  " + m.theme.StatusValue.Render(line) + "\n")
		}
		b.WriteString("\n")
	}

	est := contextmgr.EstimateTokens(out.Messages)
	note := fmt.Sprintf("≈ %s tokens · context budget %s", components.FormatTokens(est), components.FormatTokens(decision.Budget))
	if decision.Compress {
		note += " · would compress via " + decision.Strategy
	}
	b.WriteString(m.theme.SystemNote.Render(note))
	return m.overlayFooter(&b)
}

// --- /template -----------------------------------------------------------------

func cmdTemplate(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	switch sub {
	case "", "list":
		m.openOverlay(m.templateOverlay())
	case "use":
		if _, ok := m.cfg.Templates[rest]; !ok {
			return m.fail(fmt.Sprintf("no template named %q (see /template list)", rest))
		}
		m.template = rest
		m.notice = "template set to " + rest
	case "clear":
		m.template = ""
		m.notice = "template cleared"
	case "inspect":
		t, ok := m.cfg.Templates[rest]
		if !ok {
			return m.fail(fmt.Sprintf("no template named %q", rest))
		}
		var b strings.Builder
		b.WriteString(m.theme.Badge.Render("template "+rest) + "\n\n")
		m.kv(&b, "description", t.Description)
		m.kv(&b, "prompt mode", t.PromptMode)
		m.kv(&b, "temperature", fmt.Sprintf("%.2f", t.Temperature))
		b.WriteString("\n" + m.theme.UserLabel.Render("system prompt") + "\n")
		b.WriteString("  " + m.theme.StatusValue.Render(t.SystemPrompt) + "\n")
		m.openOverlay(m.overlayFooter(&b))
	default:
		// `/template golang` is shorthand for use.
		if _, ok := m.cfg.Templates[sub]; ok {
			m.template = sub
			m.notice = "template set to " + sub
			return nil
		}
		return m.fail("usage: /template [list|use <name>|clear|inspect <name>]")
	}
	return nil
}

func (m *Model) templateOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("templates") + "\n\n")
	if len(m.cfg.Templates) == 0 {
		b.WriteString(m.theme.SystemNote.Render("no templates configured — add a templates: section to the config") + "\n")
	}
	names := make([]string, 0, len(m.cfg.Templates))
	for name := range m.cfg.Templates {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := m.cfg.Templates[name]
		marker := "  "
		label := m.theme.StatusValue.Render(fmt.Sprintf("%-12s", name))
		if name == m.template {
			marker = m.theme.BadgeOK.Render("▸ ")
			label = m.theme.BadgeOK.Render(fmt.Sprintf("%-12s", name))
		}
		fmt.Fprintf(&b, "%s%s %s\n", marker, label,
			m.theme.StatusBar.Render(fmt.Sprintf("%s · mode %s · temp %.2f", t.Description, t.PromptMode, t.Temperature)))
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("/template use <name> · /template clear"))
	return m.overlayFooter(&b)
}

// --- /context -----------------------------------------------------------------

func cmdContext(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	switch sub {
	case "":
		m.openOverlay(m.contextOverlay())
	case "summary":
		var b strings.Builder
		b.WriteString(m.theme.Badge.Render("session summary") + "\n\n")
		if m.summary == "" {
			b.WriteString(m.theme.SystemNote.Render("no summary yet — one is built when the conversation grows past the context budget") + "\n")
		} else {
			for _, line := range strings.Split(m.summary, "\n") {
				b.WriteString("  " + m.theme.StatusValue.Render(line) + "\n")
			}
		}
		m.openOverlay(m.overlayFooter(&b))
	case "rebuild":
		older, _ := contextmgr.Split(m.session.Messages, m.cfg.Context.KeepLastMessages)
		if len(older) == 0 {
			return m.fail("nothing old enough to summarize yet")
		}
		out, err := contextmgr.HeuristicSummarizer{}.Summarize(context.Background(), contextmgr.SummaryInput{
			Messages: older, MaxTokens: m.cfg.Context.SummaryMaxTokens,
		})
		if err != nil {
			return m.fail("summarize: " + err.Error())
		}
		m.summary = out.Summary
		m.notice = fmt.Sprintf("summary rebuilt (≈ %s tokens)", components.FormatTokens(provider.EstimateTokens(out.Summary)))
	case "clear-summary":
		m.summary = ""
		m.notice = "session summary cleared"
	case "strategy":
		if rest == "" {
			m.notice = "context strategy: " + m.ctxStrategy + " (none|truncate|summarize|auto)"
			return nil
		}
		if !contextmgr.ValidStrategy(rest) {
			return m.fail("unknown strategy " + rest + " (none|truncate|summarize|auto)")
		}
		m.ctxStrategy = rest
		m.notice = "context strategy set to " + rest
	default:
		return m.fail("usage: /context [summary|rebuild|clear-summary|strategy <s>]")
	}
	return nil
}

func (m *Model) contextOverlay() string {
	window, source := m.contextWindow()
	used := contextmgr.EstimateTokens(m.session.Messages)
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("context") + "\n\n")
	m.kv(&b, "strategy", m.ctxStrategy)
	m.kv(&b, "window", fmt.Sprintf("%s tokens (%s)", components.FormatTokens(window), source))
	m.kv(&b, "used", fmt.Sprintf("%s tokens (estimated)", components.FormatTokens(used)))
	m.kv(&b, "reserve", fmt.Sprintf("%s tokens for the response", components.FormatTokens(m.cfg.Context.ReserveResponseTokens)))
	m.kv(&b, "keep last", fmt.Sprintf("%d messages verbatim", m.cfg.Context.KeepLastMessages))
	m.kv(&b, "summarize after", fmt.Sprintf("%d messages", m.cfg.Context.SummarizeAfterMessages))
	sum := "none"
	if m.summary != "" {
		sum = fmt.Sprintf("active (≈ %s tokens) — /context summary", components.FormatTokens(provider.EstimateTokens(m.summary)))
	}
	m.kv(&b, "summary", sum)

	// Usage bar.
	frac := 0.0
	if window > 0 {
		frac = float64(used) / float64(window)
		if frac > 1 {
			frac = 1
		}
	}
	barWidth := 30
	filled := int(frac * float64(barWidth))
	bar := m.theme.ChartBar.Render(strings.Repeat("█", filled)) + m.theme.StatusBar.Render(strings.Repeat("░", barWidth-filled))
	fmt.Fprintf(&b, "\n  %s %.0f%%\n", bar, frac*100)

	b.WriteString("\n" + m.theme.SystemNote.Render("/context strategy <s> · /context rebuild · /context clear-summary"))
	return m.overlayFooter(&b)
}

// --- /memory -----------------------------------------------------------------

func cmdMemory(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	if m.memStore == nil {
		return m.fail("memory is not configured (memory.path)")
	}
	switch sub {
	case "", "list":
		m.openOverlay(m.memoryOverlay())
	case "on":
		m.memEnabled = true
		m.notice = "local memory enabled for this session"
	case "off":
		m.memEnabled = false
		m.notice = "local memory disabled for this session"
	case "add":
		if rest == "" {
			return m.fail("usage: /memory add <text> — do not store secrets")
		}
		sn, err := m.memStore.Add(rest)
		if err != nil {
			return m.fail("memory add: " + err.Error())
		}
		m.notice = "remembered (" + sn.ID + ")"
	case "remove":
		if err := m.memStore.Remove(rest); err != nil {
			return m.fail(err.Error())
		}
		m.notice = "memory snippet removed"
	case "clear":
		if err := m.memStore.Clear(); err != nil {
			return m.fail(err.Error())
		}
		m.notice = "all memory snippets removed"
	default:
		return m.fail("usage: /memory [on|off|add <text>|list|remove <id>|clear]")
	}
	return nil
}

func (m *Model) memoryOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("local memory") + "\n\n")
	m.kv(&b, "state", onOff(m.memEnabled))
	snippets, err := m.memStore.Load()
	switch {
	case err != nil:
		b.WriteString("\n" + m.theme.ErrorText.Render(err.Error()) + "\n")
	case len(snippets) == 0:
		b.WriteString("\n" + m.theme.SystemNote.Render("no snippets — /memory add <text> (never store secrets)") + "\n")
	default:
		b.WriteString("\n")
		for _, sn := range snippets {
			fmt.Fprintf(&b, "  %s %s\n", m.theme.BadgeOK.Render(sn.ID), m.theme.StatusValue.Render(sn.Text))
		}
	}
	b.WriteString("\n" + m.theme.StatusBar.Render(fmt.Sprintf("  only snippets relevant to your message are added to prompts (max 3);\n  %d snippet limit · stored in %s", m.cfg.Memory.MaxSnippets, m.cfg.Memory.Path)) + "\n")
	b.WriteString("\n" + m.theme.SystemNote.Render("/memory add · /memory remove <id> · /memory on|off"))
	return m.overlayFooter(&b)
}

// --- /doctor -----------------------------------------------------------------

type doctorResultMsg struct{ report string }

func cmdDoctor(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	name := m.prov.Name()
	if sub == "provider" && rest != "" {
		name = rest
	}
	prov := m.prov
	// The report must show the checked provider's own config; the active
	// provider's includes any base-url/api-key overrides.
	pc, configured := m.cfg.Providers[name]
	if name == m.cfg.ActiveProviderName() {
		_, pc, configured = m.cfg.ActiveProvider()
	}
	if name != m.prov.Name() {
		if !configured {
			return m.fail(fmt.Sprintf("provider %q is not configured", name))
		}
		p, err := buildProviderForDoctor(m, name, pc)
		if err != nil {
			return m.fail(err.Error())
		}
		prov = p
	}
	model := m.model
	cfg := m.cfg
	window, source := m.contextWindow()
	m.notice = "running diagnostics…"

	return func() tea.Msg {
		return doctorResultMsg{report: doctorReport(prov, pc, model, cfg, window, source)}
	}
}

func doctorReport(prov provider.Provider, pc config.ProviderConfig, model string, cfg *config.Config, window int, windowSource string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	caps := provider.CapabilitiesOf(prov)
	var lines []string
	add := func(k, v string) { lines = append(lines, fmt.Sprintf("%-18s %s", k, v)) }

	add("provider", prov.Name())
	add("type", pc.Type)
	add("base URL", pc.BaseURL)

	if err := prov.HealthCheck(ctx); err != nil {
		add("status", "✗ "+err.Error())
	} else {
		add("status", "✓ OK")
	}

	if caps.SupportsModelList {
		models, err := prov.ListModels(ctx)
		switch {
		case err != nil:
			add("models", "✗ "+err.Error())
		default:
			found := false
			for _, mi := range models {
				if mi.ID == model {
					found = true
					break
				}
			}
			status := fmt.Sprintf("✓ OK, %d found", len(models))
			if model != "" && !found {
				status += fmt.Sprintf(" — selected model %q NOT in the list", model)
			}
			add("models", status)
		}
	} else {
		add("models", "listing not supported")
	}

	streaming := "✓ supported"
	if !caps.SupportsStreaming {
		streaming = "not supported"
	}
	add("streaming", streaming)

	usage := "reported by provider"
	if !caps.SupportsTokenUsage {
		usage = "not reported — using estimates"
	}
	add("token usage", usage)
	add("context window", fmt.Sprintf("%d from %s", window, windowSource))
	add("timeout", cfg.Network.Timeout+" (connect "+cfg.Network.ConnectTimeout+")")
	retry := "off"
	if cfg.Network.Retry.Enabled {
		retry = fmt.Sprintf("up to %d attempts, %s backoff", cfg.Network.Retry.MaxAttempts, cfg.Network.Retry.Backoff)
	}
	add("retry", retry)
	return strings.Join(lines, "\n")
}

func buildProviderForDoctor(m *Model, name string, pc config.ProviderConfig) (provider.Provider, error) {
	return app.BuildProvider(name, pc, m.cfg.Network)
}

func (m *Model) doctorOverlay(report string) string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("doctor") + "\n\n")
	for _, line := range strings.Split(report, "\n") {
		k, v, _ := strings.Cut(line, " ")
		_ = k
		style := m.theme.StatusValue
		if strings.Contains(v, "✗") || strings.Contains(line, "✗") {
			style = m.theme.ErrorText
		}
		b.WriteString("  " + style.Render(line) + "\n")
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("/doctor provider <name> checks another provider"))
	return m.overlayFooter(&b)
}

// --- /debug -----------------------------------------------------------------

func cmdDebug(m *Model, args string) tea.Cmd {
	sub, _ := splitArgs(args)
	switch sub {
	case "":
		m.notice = "debug mode: " + onOff(m.debugMode) + " — /debug on|off|last"
	case "on":
		m.debugMode = true
		m.notice = "debug mode on (request details shown after each reply)"
	case "off":
		m.debugMode = false
		m.notice = "debug mode off"
	case "last":
		m.openOverlay(m.debugOverlay())
	default:
		return m.fail("usage: /debug [on|off|last]")
	}
	return nil
}

func (m *Model) debugOverlay() string {
	d := m.lastDebug
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("debug — last request") + "\n\n")
	if d.When.IsZero() {
		b.WriteString(m.theme.SystemNote.Render("no request yet") + "\n")
		return m.overlayFooter(&b)
	}
	m.kv(&b, "when", d.When.Format("15:04:05"))
	m.kv(&b, "provider / model", d.Provider+" / "+d.Model)
	m.kv(&b, "profile", d.Profile)
	m.kv(&b, "prompt mode", d.PromptMode)
	m.kv(&b, "template", orNone(d.Template))
	m.kv(&b, "cache", d.CacheStatus)
	m.kv(&b, "temperature", fmt.Sprintf("%.2f", d.Temperature))
	m.kv(&b, "max tokens", fmt.Sprintf("%d", d.MaxTokens))
	m.kv(&b, "stream", fmt.Sprintf("%v", d.Stream))
	m.kv(&b, "retries", fmt.Sprintf("%d", d.Retries))
	if d.Duration > 0 {
		m.kv(&b, "duration", d.Duration.Round(10*time.Millisecond).String())
	}
	if d.Usage != nil {
		est := ""
		if d.Usage.Estimated {
			est = " (estimated)"
		}
		m.kv(&b, "usage", fmt.Sprintf("prompt %d · reply %d%s", d.Usage.PromptTokens, d.Usage.CompletionTokens, est))
	}
	ctxLine := fmt.Sprintf("used %s of %s budget", components.FormatTokens(d.CtxDecision.Used), components.FormatTokens(d.CtxDecision.Budget))
	if d.CtxDecision.Compress {
		ctxLine += " — compressed via " + d.CtxDecision.Strategy
	}
	m.kv(&b, "context", ctxLine)

	if len(d.Sections) > 0 {
		b.WriteString("\n" + m.theme.UserLabel.Render("composed sections") + "\n")
		for _, s := range d.Sections {
			fmt.Fprintf(&b, "  %s %s\n",
				m.theme.StatusValue.Render(fmt.Sprintf("%-22s", s.Title)),
				m.theme.StatusBar.Render(fmt.Sprintf("≈ %s tokens", components.FormatTokens(provider.EstimateTokens(s.Content)))))
		}
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("full section text: /prompt composed"))
	return m.overlayFooter(&b)
}

// --- /keys -----------------------------------------------------------------

func cmdKeys(m *Model, args string) tea.Cmd {
	sub, _ := splitArgs(args)
	switch sub {
	case "":
		m.enterKeysMode(false)
	case "raw":
		m.enterKeysMode(true)
	case "help":
		m.openOverlay(m.helpOverlay("keys"))
	default:
		return m.fail("usage: /keys [raw|help]")
	}
	return nil
}

// --- /config -----------------------------------------------------------------

func cmdConfig(m *Model, args string) tea.Cmd {
	sub, _ := splitArgs(args)
	switch sub {
	case "", "show":
		m.openOverlay(m.configOverlay())
	case "path":
		path := m.cfgPath
		if path == "" {
			if p, err := config.DefaultPath(); err == nil {
				path = p
			}
		}
		m.notice = "config: " + path
	case "reload":
		if m.thinking {
			return m.fail("/config reload is unavailable while a reply is streaming — esc to stop it first")
		}
		v, err := config.NewViper(m.cfgPath)
		if err != nil {
			return m.fail("reload: " + err.Error())
		}
		cfg, err := config.Load(v)
		if err != nil {
			return m.fail("reload: " + err.Error())
		}
		// Keep runtime overrides: CLI flags and in-session /provider switches
		// are not in the file and must survive a reload.
		cfg.Provider, cfg.Model = m.cfg.Provider, m.cfg.Model
		cfg.BaseURL, cfg.APIKey = m.cfg.BaseURL, m.cfg.APIKey
		cfg.Debug, cfg.NoStream = m.cfg.Debug, m.cfg.NoStream
		m.cfg = cfg
		m.rebuildFromConfig()
		m.notice = "configuration reloaded"
		// Rebuild the active provider so base_url/api_key edits take effect;
		// from demo mode this is the user's explicit attempt to reconnect.
		if name, pc, ok := cfg.ActiveProvider(); ok {
			if prov, err := app.BuildProvider(name, pc, cfg.Network); err == nil {
				wasDemo := m.demoMode
				m.prov = prov
				m.demoMode = false
				m.connected = false
				if wasDemo {
					m.model = cfg.ActiveModel()
				}
				return m.checkHealth(wasDemo)
			}
		}
	default:
		return m.fail("usage: /config [path|show|reload]")
	}
	return nil
}

func (m *Model) configOverlay() string {
	shown := *m.cfg
	shown.Providers = make(map[string]config.ProviderConfig, len(m.cfg.Providers))
	for name, pc := range m.cfg.Providers {
		pc.APIKey = config.Redact(pc.APIKey)
		shown.Providers[name] = pc
	}
	shown.APIKey = config.Redact(shown.APIKey)
	out, err := yaml.Marshal(shown)

	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("configuration (secrets redacted)") + "\n\n")
	if err != nil {
		b.WriteString(m.theme.ErrorText.Render(err.Error()))
		return m.overlayFooter(&b)
	}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		b.WriteString("  " + m.theme.StatusValue.Render(line) + "\n")
	}
	return m.overlayFooter(&b)
}

// --- /usage subcommands -------------------------------------------------------

func cmdUsage(m *Model, args string) tea.Cmd {
	sub, _ := splitArgs(args)
	switch sub {
	case "":
		m.openOverlay(m.usageOverlay())
	case "session":
		m.openOverlay(m.statsOverlay())
	case "last":
		m.openOverlay(m.debugOverlay())
	case "reset":
		m.session.Stats = nil
		m.session.TotalPromptTokens = 0
		m.session.TotalCompletionTokens = 0
		m.session.AnyEstimated = false
		m.lastTPS = 0
		m.notice = "session usage counters reset"
	case "export":
		if m.historyDir == "" {
			return m.fail("history saving is disabled (chat.save_history)")
		}
		records, err := history.ReadUsage(m.historyDir)
		if err != nil {
			return m.fail("export: " + err.Error())
		}
		path := filepath.Join(m.historyDir, "usage-export-"+time.Now().Format("20060102-150405")+".json")
		data, err := json.MarshalIndent(records, "", "  ")
		if err != nil {
			return m.fail("export: " + err.Error())
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return m.fail("export: " + err.Error())
		}
		m.notice = "usage exported to " + path
	default:
		return m.fail("usage: /usage [session|last|reset|export]")
	}
	return nil
}

// --- /history subcommands --------------------------------------------------

func cmdHistory(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	switch sub {
	case "":
		m.openOverlay(m.historyOverlay())
	case "save":
		m.saveWithNotice()
	case "clear":
		if m.thinking {
			return m.fail("/history clear is unavailable while a reply is streaming — esc to stop it first")
		}
		m.session.Clear()
		m.summary = ""
		m.refreshViewport()
		m.notice = "conversation cleared"
	case "load":
		if m.thinking {
			return m.fail("/history load is unavailable while a reply is streaming — esc to stop it first")
		}
		if rest == "" || m.historyDir == "" {
			return m.fail("usage: /history load <name> (see /history)")
		}
		s, err := history.Load(m.historyDir, rest)
		if err != nil {
			return m.fail(err.Error())
		}
		// Adopt the loaded session wholesale: its name (so saves update the
		// same file instead of duplicating it) and its token totals.
		m.session.Messages = s.Messages
		m.session.Stats = nil
		m.session.TotalPromptTokens = s.Prompt
		m.session.TotalCompletionTokens = s.Reply
		m.session.AnyEstimated = s.Estimated
		m.sessionName = rest
		m.summary = ""
		m.refreshViewport()
		m.notice = fmt.Sprintf("loaded %s (%d messages, %s/%s)", rest, len(s.Messages), s.Provider, s.Model)
	case "search":
		if rest == "" || m.historyDir == "" {
			return m.fail("usage: /history search <query>")
		}
		m.openOverlay(m.historySearchOverlay(rest))
	case "export":
		format, _ := splitArgs(rest)
		return m.exportHistory(format)
	default:
		return m.fail("usage: /history [load <name>|search <q>|export markdown|json|save|clear]")
	}
	return nil
}

func (m *Model) historySearchOverlay(query string) string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("history search: "+query) + "\n\n")
	metas, err := history.List(m.historyDir)
	if err != nil {
		b.WriteString(m.theme.ErrorText.Render(err.Error()))
		return m.overlayFooter(&b)
	}
	q := strings.ToLower(query)
	found := 0
	for _, meta := range metas {
		s, err := history.Load(m.historyDir, meta.Name)
		if err != nil {
			continue
		}
		for _, msg := range s.Messages {
			if idx := strings.Index(strings.ToLower(msg.Content), q); idx >= 0 {
				found++
				excerpt := excerptAround(msg.Content, idx, 70)
				fmt.Fprintf(&b, "  %s %s\n    %s\n",
					m.theme.BadgeOK.Render(meta.Name),
					m.theme.StatusBar.Render(string(msg.Role)),
					m.theme.StatusValue.Render(excerpt))
				break // one hit per session is enough for the list
			}
		}
	}
	if found == 0 {
		b.WriteString(m.theme.SystemNote.Render("no matches") + "\n")
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("/history load <name> restores a session"))
	return m.overlayFooter(&b)
}

func excerptAround(s string, idx, width int) string {
	start := idx - width/2
	if start < 0 {
		start = 0
	}
	end := start + width
	if end > len(s) {
		end = len(s)
	}
	// Snap the byte window to rune boundaries so multibyte characters at the
	// edges never render as mojibake.
	for start > 0 && !utf8.RuneStart(s[start]) {
		start--
	}
	for end < len(s) && !utf8.RuneStart(s[end]) {
		end++
	}
	return "…" + strings.ReplaceAll(s[start:end], "\n", " ") + "…"
}

func (m *Model) exportHistory(format string) tea.Cmd {
	if m.historyDir == "" {
		return m.fail("history saving is disabled (chat.save_history)")
	}
	var (
		data []byte
		ext  string
		err  error
	)
	switch format {
	case "markdown", "md":
		ext = "md"
		var b strings.Builder
		fmt.Fprintf(&b, "# llmtui session %s\n\nprovider: %s · model: %s\n\n", m.sessionName, m.prov.Name(), m.model)
		for _, msg := range m.session.Messages {
			if msg.Role == provider.RoleSystem {
				continue
			}
			fmt.Fprintf(&b, "## %s\n\n%s\n\n", msg.Role, msg.Content)
		}
		data = []byte(b.String())
	case "json":
		ext = "json"
		data, err = json.MarshalIndent(m.sessionRecord(), "", "  ")
		if err != nil {
			return m.fail("export: " + err.Error())
		}
	default:
		return m.fail("usage: /history export markdown|json")
	}
	path := filepath.Join(m.historyDir, m.sessionName+"."+ext)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return m.fail("export: " + err.Error())
	}
	m.notice = "exported to " + path
	return nil
}

// --- shared helpers ----------------------------------------------------------

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func memState(m *Model) string {
	if m.memEnabled {
		return " · session: on"
	}
	return " · session: off"
}
