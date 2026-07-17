package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"math/rand/v2"
	"sort"
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
	"github.com/patrikcze/llmtui/internal/rag"
	"github.com/patrikcze/llmtui/internal/tools"
)

// debugInfo captures the last request for /debug last.
type debugInfo struct {
	When       time.Time
	RawMessage string
	Provider   string
	Model      string
	Profile    string
	PromptMode string
	Template   string
	Sections   []prompt.Section
	// Skills are the active skills' qualified IDs at dispatch time.
	Skills        []string
	CtxDecision   contextmgr.Decision
	CacheStatus   string    // hit | miss | disabled | bypass | error | write
	CacheKey      cache.Key // snapshotted at dispatch so mid-stream /model or /provider changes cannot poison the cache
	Temperature   float64
	MaxTokens     int
	Stream        bool
	Retries       int
	Duration      time.Duration
	Usage         *provider.Usage
	Estimate      requestTokenEstimate
	MessageCount  int
	ToolCount     int
	ToolsHash     string
	SummaryActive bool
	ToolCalls     []toolCallDiagnostic
	NativeTools   bool
	WebEnabled    bool
	RAGEnabled    bool
	Reasoning     string
}

type toolCallDiagnostic struct {
	ID            string
	Name          string
	ArgumentBytes int
	ArgumentsJSON bool
	ArgumentsHash string
}

func diagnoseToolCalls(calls []provider.ToolCall) []toolCallDiagnostic {
	if len(calls) == 0 {
		return nil
	}
	out := make([]toolCallDiagnostic, 0, len(calls))
	for _, call := range calls {
		sum := sha256.Sum256([]byte(call.Arguments))
		out = append(out, toolCallDiagnostic{
			ID:            call.ID,
			Name:          call.Name,
			ArgumentBytes: len(call.Arguments),
			ArgumentsJSON: json.Valid([]byte(call.Arguments)),
			ArgumentsHash: hex.EncodeToString(sum[:8]),
		})
	}
	return out
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

type requestTokenEstimate struct {
	System       int
	Messages     int
	Tools        int
	Total        int
	Window       int
	Reserve      int
	OlderCount   int
	RecentCount  int
	SummaryToken int
}

// preparedRequest is an immutable snapshot of everything that influences a
// provider request. The cache key and ChatRequest are both derived from this
// same value so context summarization, RAG, MCP connections, or skill state
// cannot make the two disagree between separate composition passes.
type preparedRequest struct {
	composed   prompt.Output
	decision   contextmgr.Decision
	summary    string
	tools      []provider.ToolSpec
	ragResults []rag.Result
	estimate   requestTokenEstimate
}

type compositionBase struct {
	input      prompt.Input
	ragResults []rag.Result
}

func (m *Model) compositionBase(raw string, images []provider.Image, omitRaw bool) compositionBase {
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
	if m.toolsOn && m.toolRunner != nil {
		instructions := tools.Instructions(m.toolRunner.Root(), m.webOn)
		if m.toolsNative {
			instructions = tools.NativeInstructions(m.toolRunner.Root(), m.webOn)
		} else if m.skillLoadAvailable() {
			instructions += "\n" + tools.SkillInstructions
		}
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + instructions)
	}
	templatePrompt := ""
	if m.template != "" {
		if t, ok := m.cfg.Templates[m.template]; ok {
			templatePrompt = t.SystemPrompt
		}
	}

	var results []rag.Result
	retrieved := ""
	if m.ragOn && m.ragIndex != nil && !omitRaw && strings.TrimSpace(raw) != "" {
		results = m.ragIndex.Search(raw, m.ragTopK())
		if len(results) > 0 {
			retrieved = rag.FormatContext(results, m.ragMaxContextChars())
		}
	}

	return compositionBase{
		input: prompt.Input{
			RawMessage:       raw,
			Images:           images,
			SystemPrompt:     systemPrompt,
			TemplateName:     m.template,
			TemplatePrompt:   templatePrompt,
			Mode:             m.effectivePromptMode(),
			HelperText:       m.cfg.Prompt.HelperText,
			ModelHints:       prompt.HintsForProfile(prof.PromptStyle, prof.ReasoningHint),
			MemorySnippets:   memSnippets,
			RetrievedContext: retrieved,
			Skills:           m.promptSkills(),
			SkillCatalog:     m.promptSkillCatalog(),
			Include: prompt.Include{
				SessionSummary:  m.cfg.Prompt.IncludeSessionSummary,
				LocalMemory:     m.cfg.Prompt.IncludeLocalMemory,
				ModelHints:      m.cfg.Prompt.IncludeModelHints,
				FormattingHints: m.cfg.Prompt.IncludeFormattingHints,
			},
			OmitRaw: omitRaw,
		},
		ragResults: results,
	}
}

func composeFromBase(base compositionBase, recent []provider.Message, summary string) prompt.Output {
	in := base.input
	in.RecentMessages = recent
	in.SessionSummary = summary
	return prompt.Compose(in)
}

func (m *Model) summarizeMessages(messages []provider.Message, maxTokens int) string {
	if len(messages) == 0 || maxTokens <= 0 {
		return ""
	}
	out, err := (contextmgr.HeuristicSummarizer{}).Summarize(context.Background(), contextmgr.SummaryInput{
		Messages:  messages,
		MaxTokens: maxTokens,
	})
	if err != nil {
		return ""
	}
	return out.Summary
}

func estimatePrepared(out prompt.Output, specs []provider.ToolSpec, window, reserve, olderCount, recentCount int) requestTokenEstimate {
	est := requestTokenEstimate{
		Window:      window,
		Reserve:     reserve,
		OlderCount:  olderCount,
		RecentCount: recentCount,
		Tools:       provider.EstimateToolSpecsTokens(specs),
	}
	for _, message := range out.Messages {
		tokens := provider.EstimateMessageTokens(message)
		if message.Role == provider.RoleSystem {
			est.System += tokens
		} else {
			est.Messages += tokens
		}
	}
	est.Total = est.System + est.Messages + est.Tools
	return est
}

// dropOldestGroup removes the oldest request messages while preserving an
// assistant tool-call message together with its immediately following tool
// results. It prevents context fitting from producing a protocol-invalid
// history that starts with role:"tool".
func dropOldestGroup(messages []provider.Message) (dropped, recent []provider.Message) {
	if len(messages) == 0 {
		return nil, nil
	}
	end := 1
	if len(messages[0].ToolCalls) > 0 {
		for end < len(messages) && messages[end].Role == provider.RoleTool {
			end++
		}
	}
	return messages[:end], messages[end:]
}

func (m *Model) prepareRequest(raw string, images []provider.Image, omitRaw bool) (preparedRequest, error) {
	base := m.compositionBase(raw, images, omitRaw)
	specs := m.activeToolSpecs()
	window, _ := m.contextWindow()
	reserve := m.cfg.Context.ReserveResponseTokens

	// The no-history/no-summary composition is the irreducible request. Tool
	// schemas are included because OpenAI-compatible servers count them in the
	// prompt even though they are outside messages[].
	probe := composeFromBase(base, nil, "")
	fixed := estimatePrepared(probe, specs, window, reserve, 0, 0)
	decision := contextmgr.Decide(m.session.Messages, contextmgr.Params{
		Strategy:               m.ctxStrategy,
		ContextWindow:          window,
		ReserveResponseTokens:  reserve,
		SummarizeAfterMessages: m.cfg.Context.SummarizeAfterMessages,
		FixedTokens:            fixed.Total,
	})
	if fixed.Total+reserve > window {
		return preparedRequest{composed: probe, decision: decision, tools: specs, ragResults: base.ragResults, estimate: fixed}, fmt.Errorf(
			"request overhead is too large for the %d-token context window: system/user prompt %d + tool schemas %d + response reserve %d; disable tools/skills/RAG, shorten the prompt, lower the reserve, or select a larger context window",
			window, fixed.System+fixed.Messages, fixed.Tools, reserve)
	}

	keep := len(m.session.Messages)
	if decision.Compress {
		keep = m.cfg.Context.KeepLastMessages
	}
	older, recent := contextmgr.Split(m.session.Messages, keep)
	summary := m.summary
	if decision.Compress && decision.Strategy == contextmgr.StrategySummarize && len(older) > 0 {
		summary = m.summarizeMessages(older, m.cfg.Context.SummaryMaxTokens)
	}

	out := composeFromBase(base, recent, summary)
	est := estimatePrepared(out, specs, window, reserve, len(older), len(recent))
	budget := window - reserve
	for est.Total > budget && len(recent) > 0 && decision.Strategy != contextmgr.StrategyNone {
		var dropped []provider.Message
		dropped, recent = dropOldestGroup(recent)
		older = append(older, dropped...)
		if decision.Strategy == contextmgr.StrategySummarize {
			summary = m.summarizeMessages(older, m.cfg.Context.SummaryMaxTokens)
		}
		out = composeFromBase(base, recent, summary)
		est = estimatePrepared(out, specs, window, reserve, len(older), len(recent))
	}

	// If the generated summary is the last thing keeping an otherwise valid
	// request over budget, rebuild it to fit the exact space that remains.
	if est.Total > budget && decision.Strategy == contextmgr.StrategySummarize && summary != "" {
		withoutSummary := composeFromBase(base, recent, "")
		baseEstimate := estimatePrepared(withoutSummary, specs, window, reserve, len(older), len(recent))
		maxSummary := budget - baseEstimate.Total - 8
		if maxSummary > m.cfg.Context.SummaryMaxTokens {
			maxSummary = m.cfg.Context.SummaryMaxTokens
		}
		summary = m.summarizeMessages(older, maxSummary)
		out = composeFromBase(base, recent, summary)
		est = estimatePrepared(out, specs, window, reserve, len(older), len(recent))
	}
	if est.Total > budget {
		return preparedRequest{composed: out, decision: decision, summary: summary, tools: specs, ragResults: base.ragResults, estimate: est}, fmt.Errorf(
			"estimated request is %d tokens but only %d are available after the response reserve; enable context truncation/summarization or reduce prompt/tool overhead",
			est.Total, budget)
	}
	for _, section := range out.Sections {
		if section.Title == "Session Summary" {
			est.SummaryToken = provider.EstimateTokens(summary)
			break
		}
	}
	return preparedRequest{
		composed:   out,
		decision:   decision,
		summary:    summary,
		tools:      specs,
		ragResults: base.ragResults,
		estimate:   est,
	}, nil
}

func (m *Model) commitPrepared(prepared preparedRequest) {
	m.summary = prepared.summary
	m.ragLast = prepared.ragResults
	m.ctxUsed = prepared.decision.Used
	m.ctxWindow = prepared.estimate.Window
}

// compose builds the provider-ready messages for a raw user message.
// preview=true composes without touching context state (for /prompt preview).
func (m *Model) compose(raw string, images []provider.Image, preview bool) (prompt.Output, contextmgr.Decision) {
	return m.composeWith(raw, images, preview, false)
}

// composeWith adds the omitRaw knob: tool-loop continuations compose the
// session as-is (it already ends with tool results) without a new user turn.
func (m *Model) composeWith(raw string, images []provider.Image, preview, omitRaw bool) (prompt.Output, contextmgr.Decision) {
	prepared, _ := m.prepareRequest(raw, images, omitRaw)
	if !preview {
		m.commitPrepared(prepared)
	}
	return prepared.composed, prepared.decision
}

// cacheKey builds the cache key for a raw message under current settings.
// It uses the fully composed system prompt (tool/RAG/memory instructions
// included) rather than the raw config value, and fingerprints the prior
// conversation, so two requests that differ in either respect never share a
// cache entry. Request preparation is read-only, so building the key never
// mutates context state (session summary or RAG-last-results).
func (m *Model) cacheKey(raw string, images []provider.Image) cache.Key {
	prepared, _ := m.prepareRequest(raw, images, false)
	return m.cacheKeyFromPrepared(raw, prepared)
}

func (m *Model) cacheKeyFromPrepared(raw string, prepared preparedRequest) cache.Key {
	_, pc, _ := m.cfg.ActiveProvider()
	systemPrompt := m.cfg.Chat.SystemPrompt
	if len(prepared.composed.Messages) > 0 && prepared.composed.Messages[0].Role == provider.RoleSystem {
		systemPrompt = prepared.composed.Messages[0].Content
	}
	return cache.Key{
		Provider:     m.prov.Name(),
		BaseURL:      pc.BaseURL,
		Model:        m.model,
		UserMessage:  raw,
		SystemPrompt: systemPrompt,
		PromptMode:   m.effectivePromptMode(),
		Template:     m.template,
		Temperature:  m.effectiveTemperature(),
		TopP:         m.cfg.Chat.TopP,
		MaxTokens:    m.cfg.Chat.MaxTokens,
		HistoryHash:  historyFingerprint(prepared.composed.Messages),
		ToolsHash:    toolSpecsFingerprint(prepared.tools),
		Reasoning:    m.effectiveReasoning(),
		SkillsHash:   m.activeSkillsFingerprint(),
	}
}

// activeSkillsFingerprint hashes the active skill set for the cache key.
func (m *Model) activeSkillsFingerprint() string {
	if m.skillMgr == nil {
		return ""
	}
	return m.skillMgr.FingerprintActive()
}

// historyFingerprint hashes every provider-visible field of every prior
// message. Display is deliberately omitted because it is UI-only and never
// sent to a backend.
func historyFingerprint(msgs []provider.Message) string {
	h := sha256.New()
	for _, msg := range msgs {
		writeFingerprintField(h, []byte(msg.Role))
		writeFingerprintField(h, []byte(msg.Content))
		for _, image := range msg.Images {
			writeFingerprintField(h, []byte(image.MIME))
			writeFingerprintField(h, image.Data)
		}
		writeFingerprintField(h, []byte("images-end"))
		for _, call := range msg.ToolCalls {
			writeFingerprintField(h, []byte(call.ID))
			writeFingerprintField(h, []byte(call.Name))
			writeFingerprintField(h, []byte(call.Arguments))
		}
		writeFingerprintField(h, []byte("tool-calls-end"))
		writeFingerprintField(h, []byte(msg.ToolCallID))
		writeFingerprintField(h, []byte(msg.ToolName))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeFingerprintField(h hash.Hash, field []byte) {
	if _, err := fmt.Fprintf(h, "%d:", len(field)); err != nil {
		panic(fmt.Sprintf("write fingerprint length to SHA-256 hash: %v", err))
	}
	if _, err := h.Write(field); err != nil {
		panic(fmt.Sprintf("write fingerprint field to SHA-256 hash: %v", err))
	}
}

// toolSpecsFingerprint hashes the active tool set so the cache key changes
// whenever which tools are actually offered to the model changes — e.g.
// connecting or disconnecting an MCP server — even though nothing else
// about the request changed. Specs are sorted by name first: server/tool
// listing order isn't guaranteed to be stable across connects.
func toolSpecsFingerprint(specs []provider.ToolSpec) string {
	sorted := make([]provider.ToolSpec, len(specs))
	copy(sorted, specs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	h := sha256.New()
	for _, s := range sorted {
		h.Write([]byte(s.Name))
		h.Write([]byte{0})
		h.Write([]byte(s.Description))
		h.Write([]byte{0})
		h.Write(s.Parameters)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// dispatch sends a raw user message through composition, cache, and the
// provider (with retry). Used by send() and /retry.
func (m *Model) dispatch(raw string, images []provider.Image) tea.Cmd {
	m.lastUserMsg = raw
	m.lastImages = images
	skipCache := m.bypassCache
	m.bypassCache = false

	prepared, prepareErr := m.prepareRequest(raw, images, false)
	if prepareErr != nil {
		m.errText = prepareErr.Error()
		m.lastDebug = debugInfo{
			When: time.Now(), RawMessage: raw, Provider: m.prov.Name(), Model: m.model,
			PromptMode: m.effectivePromptMode(), Template: m.template, CacheStatus: "bypass",
			CtxDecision: prepared.decision, Sections: prepared.composed.Sections,
			Estimate: prepared.estimate, MessageCount: len(prepared.composed.Messages),
			ToolCount: len(prepared.tools), ToolsHash: toolSpecsFingerprint(prepared.tools),
			SummaryActive: prepared.estimate.SummaryToken > 0,
			NativeTools:   m.useNativeTools(), WebEnabled: m.webOn, RAGEnabled: m.ragOn, Reasoning: m.effectiveReasoning(),
		}
		m.endAgentRun()
		m.refreshViewport()
		return nil
	}

	key := m.cacheKeyFromPrepared(raw, prepared)
	var cacheErr error
	if !skipCache && m.responseCache != nil && m.responseCache.Enabled() && len(images) == 0 {
		entry, ok, err := m.responseCache.Get(key)
		if err != nil {
			cacheErr = err
		}
		if ok {
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
				Sections: prepared.composed.Sections, CtxDecision: prepared.decision, CacheKey: key,
				Estimate: prepared.estimate, MessageCount: len(prepared.composed.Messages),
				ToolCount: len(prepared.tools), ToolsHash: toolSpecsFingerprint(prepared.tools),
				SummaryActive: prepared.estimate.SummaryToken > 0,
				NativeTools:   m.useNativeTools(), WebEnabled: m.webOn, RAGEnabled: m.ragOn, Reasoning: m.effectiveReasoning(),
			}
			// The cached answer completed this run; run-scoped skills (which
			// were part of the key) deactivate like on a live final answer.
			m.endAgentRun()
			m.refreshViewport()
			return nil
		}
	}

	m.commitPrepared(prepared)
	m.session.AddUser(raw, images...)
	m.thinking = true
	m.streamBuf.Reset()
	m.reasoningLen = 0
	m.resetThinkFilter()
	m.streamStart = time.Now()
	m.workingVerb = workingVerbs[rand.IntN(len(workingVerbs))]
	m.errText = ""
	if cacheErr != nil {
		m.errText = "cache read failed; provider request continued: " + cacheErr.Error()
	}
	m.refreshViewport()

	prof, _ := m.activeProfile()
	req := m.buildRequestWithTools(prepared.composed.Messages, prepared.tools)

	cacheStatus := "miss"
	if m.responseCache == nil || !m.responseCache.Enabled() {
		cacheStatus = "disabled"
	}
	if skipCache {
		cacheStatus = "bypass"
	}
	if cacheErr != nil {
		cacheStatus = "error"
	}
	m.lastDebug = debugInfo{
		When:          time.Now(),
		RawMessage:    raw,
		Provider:      m.prov.Name(),
		Model:         m.model,
		Profile:       prof.Name,
		PromptMode:    m.effectivePromptMode(),
		Template:      m.template,
		Sections:      prepared.composed.Sections,
		Skills:        m.activeSkillIDs(),
		CtxDecision:   prepared.decision,
		CacheStatus:   cacheStatus,
		CacheKey:      key,
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
		Stream:        req.Stream,
		Estimate:      prepared.estimate,
		MessageCount:  len(prepared.composed.Messages),
		ToolCount:     len(prepared.tools),
		ToolsHash:     toolSpecsFingerprint(prepared.tools),
		SummaryActive: prepared.estimate.SummaryToken > 0,
		NativeTools:   m.useNativeTools(), WebEnabled: m.webOn, RAGEnabled: m.ragOn, Reasoning: m.effectiveReasoning(),
	}

	return m.startRequest(req)
}

// activeToolSpecs assembles every tool spec offered to the model under
// current settings: native workspace tools, web tools, and connected MCP
// servers' tools. buildRequest and cacheKey (Task 5) both call this so a
// request and its cache key can never disagree about which tools were
// actually offered.
func (m *Model) activeToolSpecs() []provider.ToolSpec {
	if !m.useNativeTools() {
		return nil
	}
	specs := tools.Specs()
	if m.webOn {
		specs = append(specs, tools.WebSpecs()...)
	}
	if m.skillLoadAvailable() {
		specs = append(specs, tools.SkillSpecs()...)
	}
	specs = append(specs, mcpToolSpecs(m.mcpRegistry)...)
	return specs
}

// effectiveReasoning resolves the reasoning mode: session override first,
// then config, normalizing anything unknown to "auto".
func (m *Model) effectiveReasoning() string {
	v := m.reasoningMode
	if v == "" {
		v = m.cfg.Chat.Reasoning
	}
	switch v {
	case "on", "off":
		return v
	}
	return "auto"
}

// buildRequest assembles a ChatRequest for the given messages under the
// current settings, offering native tool specs when enabled.
func (m *Model) buildRequest(messages []provider.Message) provider.ChatRequest {
	return m.buildRequestWithTools(messages, m.activeToolSpecs())
}

func (m *Model) buildRequestWithTools(messages []provider.Message, specs []provider.ToolSpec) provider.ChatRequest {
	reasoning := m.effectiveReasoning()
	if reasoning == "auto" {
		reasoning = ""
	}
	return provider.ChatRequest{
		Model:       m.model,
		Messages:    messages,
		Temperature: m.effectiveTemperature(),
		TopP:        m.cfg.Chat.TopP,
		MaxTokens:   m.cfg.Chat.MaxTokens,
		Stream:      m.cfg.StreamEnabled(),
		Tools:       specs,
		Reasoning:   reasoning,
	}
}

// continueChat re-invokes the model after tool results were appended to the
// session (native function-calling protocol). No user message is added and
// the cache is not consulted: the conversation simply continues.
func (m *Model) continueChat() tea.Cmd {
	m.bypassCache = false // consumed: continuations never touch the cache
	prepared, err := m.prepareRequest("", nil, true)
	if err != nil {
		m.errText = err.Error()
		m.endAgentRun()
		m.refreshViewport()
		return nil
	}
	m.commitPrepared(prepared)
	m.thinking = true
	m.streamBuf.Reset()
	m.reasoningLen = 0
	m.resetThinkFilter()
	m.streamStart = time.Now()
	m.workingVerb = workingVerbs[rand.IntN(len(workingVerbs))]
	m.errText = ""
	m.refreshViewport()

	prof, _ := m.activeProfile()
	req := m.buildRequestWithTools(prepared.composed.Messages, prepared.tools)
	m.lastDebug = debugInfo{
		When:          time.Now(),
		RawMessage:    "(tool results continuation)",
		Provider:      m.prov.Name(),
		Model:         m.model,
		Profile:       prof.Name,
		PromptMode:    m.effectivePromptMode(),
		Template:      m.template,
		Sections:      prepared.composed.Sections,
		Skills:        m.activeSkillIDs(),
		CtxDecision:   prepared.decision,
		CacheStatus:   "bypass",
		Temperature:   req.Temperature,
		MaxTokens:     req.MaxTokens,
		Stream:        req.Stream,
		Estimate:      prepared.estimate,
		MessageCount:  len(prepared.composed.Messages),
		ToolCount:     len(prepared.tools),
		ToolsHash:     toolSpecsFingerprint(prepared.tools),
		SummaryActive: prepared.estimate.SummaryToken > 0,
		NativeTools:   m.useNativeTools(), WebEnabled: m.webOn, RAGEnabled: m.ragOn, Reasoning: m.effectiveReasoning(),
	}
	return m.startRequest(req)
}

// startRequest owns the streaming machinery for one provider request:
// inactivity watchdog, retries, and native-tools fallback.
func (m *Model) startRequest(req provider.ChatRequest) tea.Cmd {
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
	m.streamGen++
	gen := m.streamGen
	prov := m.prov
	netCfg := m.cfg.Network
	baseURL := m.cfg.ActiveBaseURL()

	return func() tea.Msg {
		attempts := 1
		if netCfg.Retry.Enabled && netCfg.Retry.MaxAttempts > attempts {
			attempts = netCfg.Retry.MaxAttempts
		}
		fellBack := false
		var lastErr error
		for attempt := 1; attempt <= attempts; attempt++ {
			stream, err := prov.Chat(ctx, req)
			if err == nil {
				ev, ok := <-stream
				return firstStreamMsg{stream: stream, event: ev, ok: ok, retries: attempt - 1, toolsFellBack: fellBack, gen: gen}
			}
			// A backend without native tool support rejects the whole request;
			// retry immediately without the specs (the TUI then switches to
			// the fenced-block protocol for the rest of the session).
			if len(req.Tools) > 0 && ctx.Err() == nil && toolsRejectedError(err) {
				req.Tools = nil
				fellBack = true
				attempt--
				continue
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
		return streamEventMsg{event: provider.ChatEvent{Type: provider.EventError, Err: friendlyError(lastErr, prov.Name(), baseURL)}, ok: true, gen: gen}
	}
}

// toolsRejectedError reports whether a chat error looks like the backend
// refusing native tool declarations (e.g. Ollama's "does not support tools",
// or an OpenAI-compatible 400 mentioning tools).
func toolsRejectedError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "tool") {
		return false
	}
	return strings.Contains(s, "does not support") || strings.Contains(s, "not supported") ||
		strings.Contains(s, "status 400") || strings.Contains(s, "status 422") ||
		strings.Contains(s, "invalid")
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
