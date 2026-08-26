package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/cache"
	"github.com/patrikcze/llmtui/internal/contextmgr"
	"github.com/patrikcze/llmtui/internal/memory"
	"github.com/patrikcze/llmtui/internal/memoryindex"
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
	AgentRunID    string
	AgentCycle    int
	AgentStage    string
	AgentStatus   string
	AgentVerdict  string
	// MemoryHits is the merged, ranked hit list from the unified
	// memoryindex.Retriever built in compositionBase (both KindUserPreference
	// and KindSourceChunk hits). It is observability only — /debug last shows
	// it, nothing else consumes it — and is independent of MemorySnippets/
	// RetrievedContext, which are still sourced for byte-identical legacy
	// output per Global Constraint 2.
	MemoryHits      []memoryindex.Hit
	MemoryRetrieval memoryRetrievalDiagnostics
}

type memoryRetrievalDiagnostics struct {
	Enabled         bool
	Duration        time.Duration
	Selected        int
	TotalTokens     int
	MaxTokens       int
	TierTokens      map[string]int
	KindCounts      map[string]int
	RejectedReasons map[string]int
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

// isEmbeddedProvider reports whether the currently active provider is the
// in-process embedded provider, as opposed to a remote host (Ollama, LM
// Studio, or any other OpenAI-compatible server) that applies its own chat
// template and may have its own independent defaults.
func (m *Model) isEmbeddedProvider() bool {
	pc, ok := m.cfg.Providers[m.cfg.Provider]
	return ok && pc.Type == "embedded"
}

// contextWindow resolves the window size: config override, then provider
// capabilities, then model profile, then a safe fallback.
// The source string feeds /doctor.
func (m *Model) contextWindow() (tokens int, source string) {
	return m.contextWindowFor(m.prov, m.model)
}

func (m *Model) contextWindowFor(prov provider.Provider, model string) (tokens int, source string) {
	if m.cfg.Context.MaxContextTokens > 0 {
		return m.cfg.Context.MaxContextTokens, "config"
	}
	if caps := provider.CapabilitiesFor(prov, model); caps.ContextWindowTokens > 0 {
		return caps.ContextWindowTokens, "provider"
	}
	var prof modelprofile.Profile
	if m.profileMode != "" && m.profileMode != "auto" {
		prof, _ = modelprofile.ByName(m.profiles, m.profileMode)
	} else {
		prof, _ = modelprofile.Match(m.profiles, model)
	}
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
	composed prompt.Output
	decision contextmgr.Decision
	summary  string
	// agentScoped means summary was derived from only the active verified run.
	// It must not overwrite the session-wide summary used by ordinary chat.
	agentScoped bool
	tools       []provider.ToolSpec
	ragResults  []rag.Result
	memoryHits  []memoryindex.Hit
	memoryDiag  memoryRetrievalDiagnostics
	estimate    requestTokenEstimate
}

const compactedContinuationAnchor = "[Compacted continuation] Continue the original request " +
	"using the session summary and tool results above."

type compositionBase struct {
	input      prompt.Input
	ragResults []rag.Result
	// memoryHits is the selected ranked result used by ActiveContext and
	// threaded through preparedRequest into debugInfo. Direct legacy fields
	// remain independently populated for diagnostics and configured fallback.
	memoryHits []memoryindex.Hit
	memoryDiag memoryRetrievalDiagnostics
}

// compositionRAGSource builds the memoryindex.RAGSource wrapper used both to
// register RAG into the unified Retriever (for the debug hit list) and to
// call SearchRaw directly (for the legacy-identical RetrievedContext/
// ragResults output) — see Global Constraint 2 in the plan: normalized Hit
// scores must never be the source of the legacy RAG output.
func (m *Model) compositionRAGSource() memoryindex.RAGSource {
	return memoryindex.RAGSource{
		Index: func() *rag.Index { return m.ragIndex },
		TopK:  m.ragTopK,
	}
}

func (m *Model) compositionBase(raw string, images []provider.Image, omitRaw bool) compositionBase {
	prof, _ := m.activeProfile()

	// ragActive mirrors today's exact RAG guard. It gates both registering
	// RAGSource into the unified Retriever below and the direct SearchRaw
	// call further down that produces the legacy-identical RAG output.
	ragActive := m.ragOn && m.ragIndex != nil && !omitRaw && strings.TrimSpace(raw) != ""
	ragSource := m.compositionRAGSource()
	projectSource := memoryindex.ProjectSource{Store: m.projectStore, TopK: 3}
	episodeSource := memoryindex.EpisodeSource{Dir: m.historyDir, TopK: 3}

	var sources []memoryindex.Source
	localMemoryActive := m.memEnabled && m.cfg.Prompt.IncludeLocalMemory
	if localMemoryActive && m.memStore != nil {
		// Mirror today's exact guard: only register the user-memory source
		// when memory is enabled and a store exists.
		sources = append(sources, memoryindex.UserSource{Snippets: m.memStore.Load})
	}
	if localMemoryActive && m.projectStore != nil {
		sources = append(sources, projectSource)
	}
	if localMemoryActive && m.historyDir != "" {
		sources = append(sources, episodeSource)
	}
	if m.agentRunActive() {
		sources = append(sources, m.agentRunMemorySource())
	}
	if ragActive {
		sources = append(sources, ragSource)
	}

	var memoryHits []memoryindex.Hit
	memoryDiag := memoryRetrievalDiagnostics{
		Enabled:   m.cfg.Memory.Retrieval.Enabled,
		MaxTokens: m.memoryRetrievalPolicy().MaxTokens,
	}
	if len(sources) > 0 && m.cfg.Memory.Retrieval.Enabled {
		// compositionBase has no request-scoped context available today (its
		// callers — prepareRequest, dispatch, continueChat — don't carry one
		// either), so this in-process, non-blocking lookup uses Background.
		retriever := memoryindex.NewRetriever(sources...)
		started := time.Now()
		if result, err := retriever.SearchDetailed(context.Background(), memoryindex.Query{
			Text:      m.memoryQueryText(raw),
			ProjectID: m.projectID,
			SessionID: m.sessionName,
			RunID:     m.agentRunID(),
			Now:       time.Now().UTC(),
		}, m.memoryRetrievalPolicy()); err == nil {
			memoryHits = result.Hits
			memoryDiag = buildMemoryRetrievalDiagnostics(result, time.Since(started), m.memoryRetrievalPolicy().MaxTokens)
		}
	}
	activeContext := activeContextRecords(memoryHits)

	episodeMemory := []prompt.MemoryRecord{}
	if m.memEnabled && m.historyDir != "" {
		query := memoryindex.Query{
			Text:      raw,
			ProjectID: m.projectID,
			SessionID: m.sessionName,
			Kinds:     []memoryindex.Kind{memoryindex.KindEpisode},
			TopK:      3,
		}
		if hits, err := episodeSource.Search(context.Background(), query); err == nil {
			episodeMemory = make([]prompt.MemoryRecord, 0, len(hits))
			for _, hit := range hits {
				episodeMemory = append(episodeMemory, prompt.MemoryRecord{
					ID:        hit.Item.ID,
					Kind:      string(hit.Item.Kind),
					Scope:     string(hit.Item.Scope),
					Source:    "session:" + hit.Item.SessionID,
					Trust:     string(hit.Item.Trust),
					Text:      hit.Item.Text,
					UpdatedAt: hit.Item.UpdatedAt,
				})
			}
		}
	}

	// Deliberately a second, independent lookup, not derived from the merged/
	// deduped Retriever hits above: Retriever.Search dedupes hits sharing a
	// ContentHash, and memory.Store.Add does not dedupe by text — two
	// snippets with identical text get distinct IDs, and memory.Relevant can
	// rank both into its top-3. Routing MemorySnippets through the merged
	// Retriever output would let that dedup silently drop one of them. This
	// mirrors the RAG path's SearchRaw call below (see Global Constraint 2):
	// memSnippets must always equal memory.Relevant's own output, in order,
	// independent of Retriever's merge/dedup/sort semantics.
	var memSnippets []string
	if m.memEnabled && m.memStore != nil {
		if snippets, err := m.memStore.Load(); err == nil {
			for _, sn := range memory.Relevant(snippets, raw, 3) {
				memSnippets = append(memSnippets, sn.Text)
			}
		}
	}

	// Project memory has no legacy output contract, but it still uses a direct
	// ProjectSource lookup so cross-source ContentHash deduplication cannot
	// silently decide which typed project records reach the prompt.
	projectMemory := []prompt.MemoryRecord{}
	if m.memEnabled && m.projectStore != nil {
		query := memoryindex.Query{
			Text:      raw,
			ProjectID: m.projectID,
			TopK:      3,
		}
		if hits, err := projectSource.Search(context.Background(), query); err == nil {
			projectMemory = make([]prompt.MemoryRecord, 0, len(hits))
			for _, hit := range hits {
				projectMemory = append(projectMemory, prompt.MemoryRecord{
					ID:        hit.Item.ID,
					Kind:      string(hit.Item.Kind),
					Scope:     string(hit.Item.Scope),
					Source:    "project:" + shortMemoryID(hit.Item.ProjectID),
					Trust:     string(hit.Item.Trust),
					Text:      hit.Item.Text,
					UpdatedAt: hit.Item.UpdatedAt,
				})
			}
		}
	}

	systemPrompt := m.cfg.Chat.SystemPrompt
	if m.toolsOn && m.toolRunner != nil {
		instructions := tools.Instructions(m.toolRunner.Root(), m.webOn)
		if m.toolsNative {
			instructions = tools.NativeInstructions(m.toolRunner.Root(), m.webOn)
		} else {
			if m.skillLoadAvailable() {
				instructions += "\n" + tools.SkillInstructions
			}
			instructions += m.fencedDynamicToolInstructions()
		}
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + instructions)
	}
	templatePrompt := ""
	if m.template != "" {
		if t, ok := m.cfg.Templates[m.template]; ok {
			templatePrompt = t.SystemPrompt
		}
	}

	// Deliberately a second Index.Search call, not derived from the
	// normalized Hits above: SearchRaw returns the unmodified []rag.Result
	// the legacy path relied on, which is what Global Constraint 2 requires
	// for RetrievedContext/base.ragResults/m.ragLast/"retrieved snippets
	// (RAG)" byte-identical output. The normalized Hits feed only the new
	// debug ranked-hit list.
	var results []rag.Result
	retrieved := ""
	if ragActive {
		results = ragSource.SearchRaw(raw)
		if len(results) > 0 {
			retrieved = rag.FormatContext(results, m.ragMaxContextChars())
		}
	}

	return compositionBase{
		input: prompt.Input{
			RawMessage:       raw,
			Images:           images,
			SystemPrompt:     systemPrompt,
			AgentDirective:   m.agentDirective(),
			TemplateName:     m.template,
			TemplatePrompt:   templatePrompt,
			Mode:             m.effectivePromptMode(),
			HelperText:       m.cfg.Prompt.HelperText,
			ModelHints:       prompt.HintsForProfile(prof.PromptStyle, prof.ReasoningHint),
			MemorySnippets:   memSnippets,
			ProjectMemory:    projectMemory,
			EpisodeMemory:    episodeMemory,
			ActiveContext:    activeContext,
			UseActiveContext: m.cfg.Memory.Retrieval.Enabled,
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
		memoryHits: memoryHits,
		memoryDiag: memoryDiag,
	}
}

func buildMemoryRetrievalDiagnostics(
	result memoryindex.RetrievalResult,
	duration time.Duration,
	maxTokens int,
) memoryRetrievalDiagnostics {
	diagnostic := memoryRetrievalDiagnostics{
		Enabled: true, Duration: duration, Selected: len(result.Hits),
		TotalTokens: result.TotalTokens, MaxTokens: maxTokens,
		TierTokens: map[string]int{}, KindCounts: map[string]int{}, RejectedReasons: map[string]int{},
	}
	for _, hit := range result.Hits {
		diagnostic.TierTokens[memoryTierName(hit.Item.Kind)] += hit.Tokens
		diagnostic.KindCounts[string(hit.Item.Kind)]++
	}
	for _, rejected := range result.Rejected {
		diagnostic.RejectedReasons[rejected.Reason]++
	}
	return diagnostic
}

func memoryTierName(kind memoryindex.Kind) string {
	switch kind {
	case memoryindex.KindUserPreference:
		return "user"
	case memoryindex.KindProjectArchitecture, memoryindex.KindProjectConvention, memoryindex.KindProjectDecision:
		return "project"
	case memoryindex.KindEpisode:
		return "episode"
	case memoryindex.KindAgentObjective, memoryindex.KindAgentCriterion, memoryindex.KindAgentFailure, memoryindex.KindAgentEvidence:
		return "agent"
	case memoryindex.KindSourceChunk:
		return "source"
	default:
		return "other"
	}
}

func (m *Model) memoryQueryText(raw string) string {
	parts := []string{raw}
	if m.agentRunActive() && m.agentLoop != nil && m.agentLoop.run != nil {
		parts = append(parts, m.agentLoop.run.Objective)
		for _, criterion := range m.agentLoop.run.UnresolvedCriteria() {
			parts = append(parts, criterion.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (m *Model) memoryRetrievalPolicy() memoryindex.RetrievalPolicy {
	cfg := m.cfg.Memory.Retrieval
	policy := memoryindex.RetrievalPolicy{
		TopK: cfg.TopK, MaxTokens: cfg.MaxContextTokens,
		UserTokens: cfg.UserTokens, ProjectTokens: cfg.ProjectTokens,
		EpisodeTokens: cfg.EpisodicTokens, AgentTokens: cfg.AgentTokens,
		SourceTokens: cfg.SourceTokens,
	}
	if policy.TopK <= 0 {
		policy.TopK = 10
	}
	if policy.MaxTokens <= 0 {
		policy.MaxTokens = 1800
	}
	if policy.UserTokens <= 0 {
		policy.UserTokens = 256
	}
	if policy.ProjectTokens <= 0 {
		policy.ProjectTokens = 512
	}
	if policy.EpisodeTokens <= 0 {
		policy.EpisodeTokens = 384
	}
	if policy.AgentTokens <= 0 {
		policy.AgentTokens = 512
	}
	if policy.SourceTokens <= 0 {
		policy.SourceTokens = 768
	}
	return policy
}

func activeContextRecords(hits []memoryindex.Hit) []prompt.MemoryRecord {
	records := make([]prompt.MemoryRecord, 0, len(hits))
	for _, hit := range hits {
		text := hit.Item.Text
		if hit.Item.Summary != "" {
			text = hit.Item.Summary
		}
		source := "unknown"
		switch hit.Item.Kind {
		case memoryindex.KindUserPreference:
			source = "user"
		case memoryindex.KindProjectArchitecture, memoryindex.KindProjectConvention, memoryindex.KindProjectDecision:
			source = "project:" + shortMemoryID(hit.Item.ProjectID)
			if hit.Item.Source.RunID != "" {
				source += "/run:" + hit.Item.Source.RunID
				if hit.Item.Source.Cycle > 0 {
					source += fmt.Sprintf("/cycle:%d", hit.Item.Source.Cycle)
				}
			}
		case memoryindex.KindEpisode:
			source = "session:" + hit.Item.SessionID
		case memoryindex.KindAgentObjective, memoryindex.KindAgentCriterion, memoryindex.KindAgentFailure, memoryindex.KindAgentEvidence:
			source = "run:" + hit.Item.RunID
			if hit.Item.Source.Cycle > 0 {
				source += fmt.Sprintf("/cycle:%d", hit.Item.Source.Cycle)
			}
		case memoryindex.KindSourceChunk:
			source = fmt.Sprintf("%s:%d-%d", hit.Item.Source.Path, hit.Item.Source.StartLine, hit.Item.Source.EndLine)
		}
		records = append(records, prompt.MemoryRecord{
			ID: hit.Item.ID, Kind: string(hit.Item.Kind), Scope: string(hit.Item.Scope),
			Source: source, Trust: string(hit.Item.Trust), Text: text, UpdatedAt: hit.Item.UpdatedAt,
		})
	}
	return records
}

func shortMemoryID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
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

// oldestGroupEnd returns the exclusive end of the first complete conversation
// group. User turns include their following assistant/tool work up to the next
// user; assistant tool calls include all immediately following tool results.
func oldestGroupEnd(messages []provider.Message) int {
	if len(messages) == 0 {
		return 0
	}
	end := 1
	if messages[0].Role == provider.RoleUser {
		for end < len(messages) && messages[end].Role != provider.RoleUser {
			end++
		}
		return end
	}
	if messages[0].Role == provider.RoleAssistant && len(messages[0].ToolCalls) > 0 {
		for end < len(messages) && messages[end].Role == provider.RoleTool {
			end++
		}
	}
	return end
}

func latestUserMessageIndex(messages []provider.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleUser {
			return i
		}
	}
	return -1
}

// dropOldestGroup removes one complete group. During a native tool
// continuation it keeps the active user at the front and refuses to discard
// the newest assistant/tool group, so context fitting retains both the task
// anchor and the freshest evidence.
func dropOldestGroup(
	messages []provider.Message,
	preserveActiveTurn bool,
) (dropped, recent []provider.Message, ok bool) {
	if len(messages) == 0 {
		return []provider.Message{}, []provider.Message{}, false
	}
	if !preserveActiveTurn {
		end := oldestGroupEnd(messages)
		return append([]provider.Message{}, messages[:end]...),
			append([]provider.Message{}, messages[end:]...), true
	}

	start := 0
	if latestUserMessageIndex(messages) == 0 {
		start = 1
	}
	end := start + oldestGroupEnd(messages[start:])
	if end <= start || end >= len(messages) {
		return []provider.Message{}, append([]provider.Message{}, messages...), false
	}

	dropped = append([]provider.Message{}, messages[start:end]...)
	recent = make([]provider.Message, 0, len(messages)-len(dropped))
	recent = append(recent, messages[:start]...)
	recent = append(recent, messages[end:]...)
	return dropped, recent, true
}

func hasUserMessage(messages []provider.Message) bool {
	return latestUserMessageIndex(messages) >= 0
}

func withCompactedContinuationAnchor(messages []provider.Message) []provider.Message {
	if hasUserMessage(messages) {
		return messages
	}
	anchored := make([]provider.Message, 0, len(messages)+1)
	anchored = append(anchored, provider.Message{
		Role:    provider.RoleUser,
		Content: compactedContinuationAnchor,
	})
	anchored = append(anchored, messages...)
	return anchored
}

func compactLatestUserMessage(messages []provider.Message) (provider.Message, []provider.Message, bool) {
	index := latestUserMessageIndex(messages)
	if index < 0 || messages[index].Content == compactedContinuationAnchor || len(messages[index].Images) > 0 {
		return provider.Message{}, messages, false
	}
	original := messages[index]
	compacted := append([]provider.Message{}, messages...)
	compacted[index] = provider.Message{
		Role:    provider.RoleUser,
		Content: compactedContinuationAnchor,
	}
	return original, compacted, true
}

// requestHistory returns the provider context for one request. A verified
// run's first cycle keeps prior human prompts and final answers so requests
// such as "write that to a file" still work, while completed tool protocol
// messages and controller turns stay display-only. Once the verifier starts a
// retry cycle, only messages produced by the current run are eligible.
func (m *Model) requestHistory() (messages []provider.Message, summary string, agentScoped bool) {
	messages = m.session.Messages
	summary = m.summary
	if !m.agentRunActive() {
		return messages, summary, false
	}
	start := m.agentLoop.historyStart
	if start < 0 || start > len(messages) {
		start = 0
	}
	if m.agentLoop.run.Cycle <= 1 {
		prior := projectCompletedAgentHistory(messages[:start])
		summary := ""
		if m.agentLoop.run.StartContextCaptured {
			prior = make([]provider.Message, 0, len(m.agentLoop.run.StartTurns))
			for _, turn := range m.agentLoop.run.StartTurns {
				role := provider.Role(turn.Role)
				if role != provider.RoleUser && role != provider.RoleAssistant {
					continue
				}
				prior = append(prior, provider.Message{Role: role, Content: turn.Content})
			}
			summary = m.agentLoop.run.StartSummary
		}
		current := append([]provider.Message(nil), messages[start:]...)
		return append(prior, current...), summary, true
	}
	return projectPriorCyclesInRun(messages, start, m.agentLoop.cycleBoundaries), "", true
}

// projectPriorCyclesInRun returns a multi-cycle run's messages from runStart
// onward with every COMPLETED cycle's raw tool-call/tool-result exchange
// projected away via projectCompletedAgentHistory — the executor already has
// each completed cycle's outcome from the bounded run.Memory recap in its
// system prompt (agentDirective), so resending that cycle's full tool
// traffic verbatim on every later cycle only grows context without adding
// information the executor doesn't already have. Only the current,
// in-progress cycle (the final segment, bounded by cycleBoundaries) is kept
// verbatim, since the executor still needs full fidelity on what it just did
// this cycle. If cycleBoundaries is empty (e.g. a run resumed from persisted
// storage into a fresh in-memory loop state, which never recorded them),
// this degrades to returning the full range unprojected — the same
// behavior as before this projection existed.
func projectPriorCyclesInRun(messages []provider.Message, runStart int, cycleBoundaries []int) []provider.Message {
	result := make([]provider.Message, 0, len(messages)-runStart)
	segmentStart := runStart
	for _, boundary := range cycleBoundaries {
		if boundary <= segmentStart || boundary > len(messages) {
			continue
		}
		result = append(result, projectCompletedAgentHistory(messages[segmentStart:boundary])...)
		segmentStart = boundary
	}
	return append(result, messages[segmentStart:]...)
}

func projectCompletedAgentHistory(messages []provider.Message) []provider.Message {
	projected := make([]provider.Message, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case provider.RoleTool:
			continue
		case provider.RoleUser:
			if message.Content == agentContinueDirective || strings.HasPrefix(message.Content, tools.ResultsPrefix) {
				continue
			}
		case provider.RoleAssistant:
			if len(message.ToolCalls) > 0 || len(tools.Parse(message.Content)) > 0 {
				continue
			}
		}
		projected = append(projected, message)
	}
	return projected
}

// projectNativeToolHistoryForFencedProtocol removes native function-calling
// objects from history after a provider/model falls back to the prompt-based
// protocol. Keeping assistant.tool_calls and role:"tool" messages can leave
// the backend's chat template in native tool mode even when the new request
// offers no tool schemas. The textual projection preserves the evidence the
// model gathered while making the protocol transition unambiguous.
func projectNativeToolHistoryForFencedProtocol(messages []provider.Message) []provider.Message {
	projected := make([]provider.Message, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		if message.Role != provider.RoleAssistant || len(message.ToolCalls) == 0 {
			if message.Role == provider.RoleTool {
				projected = append(projected, textualNativeToolResults(nil, []provider.Message{message}))
			} else {
				projected = append(projected, message)
			}
			continue
		}

		calls := message.ToolCalls
		if strings.TrimSpace(message.Content) != "" {
			message.ToolCalls = nil
			projected = append(projected, message)
		}
		end := index + 1
		for end < len(messages) && messages[end].Role == provider.RoleTool {
			end++
		}
		projected = append(projected, textualNativeToolResults(calls, messages[index+1:end]))
		index = end - 1
	}
	return projected
}

func textualNativeToolResults(calls []provider.ToolCall, results []provider.Message) provider.Message {
	var content strings.Builder
	content.WriteString(tools.ResultsPrefix)
	content.WriteString("\n\nPrevious native tool exchange, projected into text after protocol fallback:")
	resultsByID := make(map[string]provider.Message, len(results))
	for _, result := range results {
		resultsByID[result.ToolCallID] = result
	}
	for _, call := range calls {
		fmt.Fprintf(&content, "\n\n### %s", call.Name)
		if call.Arguments != "" {
			content.WriteString("\narguments: ")
			content.WriteString(call.Arguments)
		}
		if result, ok := resultsByID[call.ID]; ok {
			content.WriteByte('\n')
			content.WriteString(result.Content)
			delete(resultsByID, call.ID)
		}
	}
	for _, result := range results {
		if _, ok := resultsByID[result.ToolCallID]; !ok {
			continue
		}
		name := result.ToolName
		if name == "" {
			name = "tool"
		}
		fmt.Fprintf(&content, "\n\n### %s\n%s", name, result.Content)
		delete(resultsByID, result.ToolCallID)
	}
	return provider.Message{Role: provider.RoleUser, Content: content.String()}
}

func (m *Model) prepareRequest(raw string, images []provider.Image, omitRaw bool) (preparedRequest, error) {
	base := m.compositionBase(raw, images, omitRaw)
	specs := m.activeToolSpecs()
	window, _ := m.contextWindow()
	reserve := m.cfg.Context.ReserveResponseTokens
	historyMessages, existingSummary, agentScoped := m.requestHistory()
	if !m.toolsNative {
		historyMessages = projectNativeToolHistoryForFencedProtocol(historyMessages)
	}

	// The no-history/no-summary composition is the irreducible request. Tool
	// schemas are included because OpenAI-compatible servers count them in the
	// prompt even though they are outside messages[].
	probe := composeFromBase(base, nil, "")
	fixed := estimatePrepared(probe, specs, window, reserve, 0, 0)
	decision := contextmgr.Decide(historyMessages, contextmgr.Params{
		Strategy:               m.ctxStrategy,
		ContextWindow:          window,
		ReserveResponseTokens:  reserve,
		SummarizeAfterMessages: m.cfg.Context.SummarizeAfterMessages,
		FixedTokens:            fixed.Total,
	})
	if fixed.Total+reserve > window {
		return preparedRequest{composed: probe, decision: decision, agentScoped: agentScoped, tools: specs, ragResults: base.ragResults, memoryHits: base.memoryHits, memoryDiag: base.memoryDiag, estimate: fixed}, fmt.Errorf(
			"request overhead is too large for the %d-token context window: system/user prompt %d + tool schemas %d + response reserve %d; disable tools/skills/RAG, shorten the prompt, lower the reserve, or select a larger context window",
			window, fixed.System+fixed.Messages, fixed.Tools, reserve)
	}

	keep := len(historyMessages)
	if decision.Compress {
		keep = m.cfg.Context.KeepLastMessages
	}
	older, recent := contextmgr.Split(historyMessages, keep)
	toolContinuation := omitRaw && len(recent) > 0 && recent[len(recent)-1].Role == provider.RoleTool
	if toolContinuation {
		recent = withCompactedContinuationAnchor(recent)
	}
	summary := existingSummary
	if decision.Compress && decision.Strategy == contextmgr.StrategySummarize && len(older) > 0 {
		summary = m.summarizeMessages(older, m.cfg.Context.SummaryMaxTokens)
	}

	out := composeFromBase(base, recent, summary)
	est := estimatePrepared(out, specs, window, reserve, len(older), len(recent))
	budget := window - reserve
	for est.Total > budget && len(recent) > 0 && decision.Strategy != contextmgr.StrategyNone {
		dropped, next, ok := dropOldestGroup(recent, toolContinuation)
		if !ok {
			break
		}
		recent = next
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

	// If the irreducible active turn still does not fit, replace only its
	// oversized text user message with a bounded anchor. The original request
	// moves into the summary; image turns fail explicitly instead of silently
	// dropping visual input.
	if est.Total > budget && toolContinuation && decision.Strategy != contextmgr.StrategyNone {
		original, compacted, ok := compactLatestUserMessage(recent)
		if ok {
			recent = compacted
			older = append(older, original)
			base.input.Include.SessionSummary = true
			summary = m.summarizeMessages(older, m.cfg.Context.SummaryMaxTokens)
			out = composeFromBase(base, recent, summary)
			est = estimatePrepared(out, specs, window, reserve, len(older), len(recent))
			if est.Total > budget && summary != "" {
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
		}
	}
	if est.Total > budget {
		return preparedRequest{composed: out, decision: decision, summary: summary, agentScoped: agentScoped, tools: specs, ragResults: base.ragResults, memoryHits: base.memoryHits, memoryDiag: base.memoryDiag, estimate: est}, fmt.Errorf(
			"estimated request is %d tokens but only %d are available after the response reserve; enable context truncation/summarization or reduce prompt/tool overhead",
			est.Total, budget)
	}
	if toolContinuation && !hasUserMessage(out.Messages) {
		return preparedRequest{
				composed: out, decision: decision, summary: summary, agentScoped: agentScoped, tools: specs,
				ragResults: base.ragResults, memoryHits: base.memoryHits, memoryDiag: base.memoryDiag, estimate: est,
			},
			errors.New("tool continuation has no user anchor after context selection")
	}
	for _, section := range out.Sections {
		if section.Title == "Session Summary" {
			est.SummaryToken = provider.EstimateTokens(summary)
			break
		}
	}
	return preparedRequest{
		composed:    out,
		decision:    decision,
		summary:     summary,
		agentScoped: agentScoped,
		tools:       specs,
		ragResults:  base.ragResults,
		memoryHits:  base.memoryHits,
		memoryDiag:  base.memoryDiag,
		estimate:    est,
	}, nil
}

func (m *Model) commitPrepared(prepared preparedRequest) {
	if !prepared.agentScoped {
		m.summary = prepared.summary
	}
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
		RuntimeID:    provider.RuntimeFingerprintOf(m.prov),
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
	defer m.syncAgentDebug()
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
			MemoryHits: prepared.memoryHits, MemoryRetrieval: prepared.memoryDiag,
		}
		m.failVerifiedRun(prepareErr)
		m.endAgentRun()
		m.refreshViewport()
		return m.persistAgentRun()
	}
	if m.agentRunActive() && prepared.decision.Compress {
		m.agentLoop.run.RecordContextCompression(prepared.decision.Strategy, prepared.decision.Used, prepared.decision.Budget, time.Now())
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
				MemoryHits: prepared.memoryHits, MemoryRetrieval: prepared.memoryDiag,
			}
			// The cached answer completed this run; run-scoped skills (which
			// were part of the key) deactivate like on a live final answer.
			m.endAgentRun()
			m.refreshViewport()
			return nil
		}
	}
	req := m.buildRequestWithTools(prepared.composed.Messages, prepared.tools)
	if exceeded, reason := m.agentModelRequestBudgetExceeded("executor", prepared.estimate.Total, req.MaxTokens); exceeded {
		return m.terminateAgentModelRequestBudget(reason)
	}

	m.commitPrepared(prepared)
	m.session.AddUser(raw, images...)
	m.thinking = true
	m.streamBuf.Reset()
	m.reasoningLen = 0
	m.reasoningBuf.Reset()
	m.reasoningStart = time.Time{}
	m.reasoningEnd = time.Time{}
	m.filteredReasoningLen = 0
	m.progressText = ""
	m.resetThinkFilter()
	m.workingVerb = workingVerbs[rand.IntN(len(workingVerbs))]
	m.errText = ""
	if cacheErr != nil {
		m.errText = "cache read failed; provider request continued: " + cacheErr.Error()
	}
	m.refreshViewport()

	prof, _ := m.activeProfile()
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
		MemoryHits: prepared.memoryHits, MemoryRetrieval: prepared.memoryDiag,
	}

	return m.startRequest(req)
}

// activeToolSpecs returns the exact native snapshot offered to the next
// provider request. The HTTP registry mirrors this same visible set.
func (m *Model) activeToolSpecs() []provider.ToolSpec {
	if !m.useNativeTools() {
		return nil
	}
	return m.modelVisibleToolSpecs()
}

// eligibleToolSpecs is the authoritative full catalog the current session
// may use. Visibility can narrow this set but can never add to it.
func (m *Model) eligibleToolSpecs() []provider.ToolSpec {
	if !m.toolsOn || m.toolRunner == nil {
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

func (m *Model) modelVisibleToolSpecs() []provider.ToolSpec {
	eligible := m.eligibleToolSpecs()
	if !m.toolDiscoveryActive(eligible) {
		return eligible
	}
	visible := make([]provider.ToolSpec, 0, len(eligible))
	for _, spec := range eligible {
		if _, _, dynamic := tools.SplitMCPToolName(spec.Name); !dynamic || m.disclosedTools[spec.Name] {
			visible = append(visible, spec)
		}
	}
	return visible
}

// activeToolNames extracts just the names from a tool spec list, for callers
// (the agent verifier) that need to know what's available without the full
// JSON Schema parameter definitions.
func activeToolNames(specs []provider.ToolSpec) []string {
	if len(specs) == 0 {
		return nil
	}
	names := make([]string, len(specs))
	for i, spec := range specs {
		names[i] = spec.Name
	}
	return names
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
		// The embedded provider renders the model's own GGUF chat template
		// directly, with no separate host-level default (unlike LM Studio's
		// own "Enable Thinking" toggle, or a remote server's own default).
		// Omitting enable_thinking there is not neutral: a template that
		// only checks "enable_thinking is defined and enable_thinking"
		// (verified against Gemma 4's real metadata template) treats the
		// omitted variable as off. Use the model profile's ReasoningHint —
		// already correct for models known to default to thinking — so
		// "auto" resolves to the model's actual intended default instead of
		// always landing on off.
		if m.isEmbeddedProvider() {
			if prof, ok := m.activeProfile(); ok && prof.ReasoningHint {
				reasoning = "on"
			}
		}
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
	defer m.syncAgentDebug()
	m.bypassCache = false // consumed: continuations never touch the cache
	prepared, err := m.prepareRequest("", nil, true)
	if err != nil {
		m.errText = err.Error()
		m.failVerifiedRun(err)
		m.endAgentRun()
		m.refreshViewport()
		return m.persistAgentRun()
	}
	req := m.buildRequestWithTools(prepared.composed.Messages, prepared.tools)
	if exceeded, reason := m.agentModelRequestBudgetExceeded("continuation", prepared.estimate.Total, req.MaxTokens); exceeded {
		return m.terminateAgentModelRequestBudget(reason)
	}
	m.commitPrepared(prepared)
	m.thinking = true
	m.streamBuf.Reset()
	m.reasoningLen = 0
	m.reasoningBuf.Reset()
	m.reasoningStart = time.Time{}
	m.reasoningEnd = time.Time{}
	m.filteredReasoningLen = 0
	m.progressText = ""
	m.resetThinkFilter()
	m.workingVerb = workingVerbs[rand.IntN(len(workingVerbs))]
	m.errText = ""
	m.refreshViewport()

	prof, _ := m.activeProfile()
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
		MemoryHits: prepared.memoryHits, MemoryRetrieval: prepared.memoryDiag,
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
	ctx, gen, err := m.beginStream(m.agentContext(), idle)
	if err != nil {
		m.thinking = false
		m.errText = err.Error()
		m.complete(turnOutcomeExecutionFailure)
		m.failVerifiedRun(err)
		m.endAgentRun()
		m.refreshViewport()
		return m.persistAgentRun()
	}
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
				select {
				case ev, ok := <-stream:
					return firstStreamMsg{stream: stream, event: ev, ok: ok, retries: attempt - 1, toolsFellBack: fellBack, gen: gen}
				case <-ctx.Done():
					cause := context.Cause(ctx)
					if cause == nil {
						cause = ctx.Err()
					}
					cancelGen := gen
					if errors.Is(cause, context.Canceled) {
						// User cancellation already finalized the UI state. Mark
						// this synthetic first event stale so it cannot overwrite it.
						cancelGen--
					}
					return firstStreamMsg{stream: stream, event: provider.ChatEvent{Type: provider.EventError, Err: cause}, ok: true, retries: attempt - 1, toolsFellBack: fellBack, gen: cancelGen}
				}
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
