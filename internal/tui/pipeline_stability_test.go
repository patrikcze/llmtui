package tui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
)

func seedSummarizableConversation(m *Model) {
	for i := 0; i < 4; i++ {
		m.session.AddUser("user request " + strings.Repeat("detail ", 20))
		m.session.AddAssistant("assistant response " + strings.Repeat("result ", 20))
	}
}

func TestComposeSummaryIsIdempotentForUnchangedHistory(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Context.SummarizeAfterMessages = 2
	m.cfg.Context.KeepLastMessages = 2
	seedSummarizableConversation(m)

	_, decision := m.compose("next", nil, false)
	if !decision.Compress {
		t.Fatal("test setup did not trigger context compression")
	}
	first := m.summary
	if first == "" {
		t.Fatal("test setup did not produce a summary")
	}

	m.compose("next", nil, false)
	if m.summary != first {
		t.Fatalf("unchanged history grew summary from %d to %d bytes", len(first), len(m.summary))
	}
}

func TestCacheKeySystemPromptMatchesDispatchedComposition(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Context.SummarizeAfterMessages = 2
	m.cfg.Context.KeepLastMessages = 2
	seedSummarizableConversation(m)

	key := m.cacheKey("next", nil)
	composed, _ := m.compose("next", nil, false)
	if len(composed.Messages) == 0 || composed.Messages[0].Role != provider.RoleSystem {
		t.Fatal("composition did not start with a system message")
	}
	if key.SystemPrompt != composed.Messages[0].Content {
		t.Fatal("cache key system prompt differs from the provider-bound composition")
	}
}

func TestPreparedToolSnapshotFeedsCacheAndRequest(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.mcpRegistry = newConnectedMCPRegistry(t, "jira", []mcp.Tool{{
		Server: "jira", Name: "lookup", Schema: json.RawMessage(`{"type":"object"}`),
	}}, nil)

	prepared, err := m.prepareRequest("hello", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	key := m.cacheKeyFromPrepared("hello", prepared)
	req := m.buildRequestWithTools(prepared.composed.Messages, prepared.tools)
	if !reflect.DeepEqual(req.Tools, prepared.tools) {
		t.Fatal("provider request did not use the prepared tool snapshot")
	}
	if key.ToolsHash != toolSpecsFingerprint(req.Tools) {
		t.Fatal("cache key and provider request fingerprint different tool snapshots")
	}
}

func TestPrepareRejectsIrreducibleToolOverheadBeforeDispatch(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.cfg.Context.MaxContextTokens = 200
	m.cfg.Context.ReserveResponseTokens = 100
	m.mcpRegistry = newConnectedMCPRegistry(t, "large", []mcp.Tool{{
		Server:      "large",
		Name:        "lookup",
		Description: strings.Repeat("large tool description ", 100),
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	}}, nil)

	before := len(m.session.Messages)
	if cmd := m.dispatch("hello", nil); cmd != nil {
		t.Fatal("oversized fixed request should not contact the provider")
	}
	if len(m.session.Messages) != before {
		t.Fatal("rejected request mutated the conversation")
	}
	if !strings.Contains(m.errText, "request overhead is too large") || !strings.Contains(m.errText, "tool schemas") {
		t.Fatalf("unexpected error: %q", m.errText)
	}
}

func benchmarkToolRounds(rounds int, resultSize int) []provider.Message {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "run the benchmark"}}
	for i := 1; i <= rounds; i++ {
		id := fmt.Sprintf("call_%d", i)
		messages = append(messages,
			provider.Message{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{{
					ID: id, Name: "read_file", Arguments: `{"path":"input.txt"}`,
				}},
			},
			provider.Message{
				Role:       provider.RoleTool,
				Content:    fmt.Sprintf("result_%d %s", i, strings.Repeat("x", resultSize)),
				ToolCallID: id,
				ToolName:   "read_file",
			},
		)
	}
	return messages
}

func TestPrepareToolContinuationPreservesUserAcrossSevenRounds(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Context.MaxContextTokens = 100_000
	m.session.Messages = benchmarkToolRounds(7, 16)

	prepared, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.decision.Compress {
		t.Fatal("test setup did not trigger count-based compression")
	}
	if !hasMessageRole(prepared.composed.Messages, provider.RoleUser) {
		t.Fatal("tool continuation lost its user anchor")
	}
	got := prepared.composed.Messages[len(prepared.composed.Messages)-1]
	if got.Role != provider.RoleTool || !strings.Contains(got.Content, "result_7") {
		t.Fatalf("last message = %+v, want newest tool result", got)
	}
}

func TestPrepareBudgetTrimmingKeepsUserAndNewestToolResult(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Context.Strategy = "summarize"
	m.cfg.Context.MaxContextTokens = 1_000
	m.cfg.Context.ReserveResponseTokens = 100
	m.cfg.Context.SummarizeAfterMessages = 1
	m.cfg.Context.KeepLastMessages = 8
	m.cfg.Context.SummaryMaxTokens = 80
	m.cfg.Prompt.IncludeFormattingHints = false
	m.cfg.Prompt.IncludeModelHints = false
	m.session.Messages = benchmarkToolRounds(3, 1_200)

	prepared, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMessageRole(prepared.composed.Messages, provider.RoleUser) {
		t.Fatal("hard-budget trimming lost its user anchor")
	}
	toolResults := 0
	for _, message := range prepared.composed.Messages {
		if message.Role != provider.RoleTool {
			continue
		}
		toolResults++
		if strings.Contains(message.Content, "result_1") {
			t.Fatal("oldest tool group was retained instead of summarized")
		}
	}
	if toolResults == 0 {
		t.Fatal("hard-budget trimming discarded every tool result")
	}
	last := prepared.composed.Messages[len(prepared.composed.Messages)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "result_3") {
		t.Fatalf("last message = %+v, want newest tool result", last)
	}
}

func TestPrepareToolContinuationAddsCompactedAnchorWhenHistoryHasNoUser(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Context.MaxContextTokens = 100_000
	m.session.Messages = benchmarkToolRounds(1, 16)[1:]

	prepared, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range prepared.composed.Messages {
		if message.Role == provider.RoleUser && message.Content == compactedContinuationAnchor {
			return
		}
	}
	t.Fatal("user-less tool continuation did not receive a compacted anchor")
}

func TestPrepareToolContinuationCompactsOversizedUserAsLastResort(t *testing.T) {
	m := newTestModel(t)
	m.cfg.Context.Strategy = "summarize"
	m.cfg.Context.MaxContextTokens = 600
	m.cfg.Context.ReserveResponseTokens = 100
	m.cfg.Context.SummarizeAfterMessages = 1
	m.cfg.Context.SummaryMaxTokens = 80
	m.cfg.Prompt.IncludeFormattingHints = false
	m.cfg.Prompt.IncludeModelHints = false
	m.session.Messages = benchmarkToolRounds(1, 16)
	m.session.Messages[0].Content = "Run the benchmark. " + strings.Repeat("preserve this detail ", 300)
	original := m.session.Messages[0].Content

	prepared, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.summary == "" || !strings.Contains(prepared.summary, "Run the benchmark.") {
		t.Fatalf("summary = %q, want compacted original request", prepared.summary)
	}
	users := 0
	for _, message := range prepared.composed.Messages {
		if message.Role != provider.RoleUser {
			continue
		}
		users++
		if message.Content != compactedContinuationAnchor {
			t.Fatalf("user content = %q, want bounded continuation anchor", message.Content)
		}
	}
	if users != 1 {
		t.Fatalf("user messages = %d, want one compacted anchor", users)
	}
	if m.session.Messages[0].Content != original {
		t.Fatal("request preparation mutated the stored user message")
	}
	last := prepared.composed.Messages[len(prepared.composed.Messages)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "result_1") {
		t.Fatalf("last message = %+v, want newest tool result", last)
	}
}

func TestDropOldestGroupPreservesActiveUserAndNewestToolPair(t *testing.T) {
	messages := benchmarkToolRounds(2, 16)
	dropped, recent, ok := dropOldestGroup(messages, true)
	if !ok {
		t.Fatal("first completed tool group should be droppable")
	}
	if len(dropped) != 2 || !strings.Contains(dropped[1].Content, "result_1") {
		t.Fatalf("dropped = %+v, want oldest complete tool group", dropped)
	}
	if len(recent) != 3 || recent[0].Role != provider.RoleUser || !strings.Contains(recent[2].Content, "result_2") {
		t.Fatalf("recent = %+v, want user anchor and newest complete tool group", recent)
	}
	if _, next, ok := dropOldestGroup(recent, true); ok || len(next) != len(recent) {
		t.Fatalf("newest tool group was droppable: ok=%t next=%+v", ok, next)
	}
}

func hasMessageRole(messages []provider.Message, role provider.Role) bool {
	for _, message := range messages {
		if message.Role == role {
			return true
		}
	}
	return false
}

func TestToolCallDiagnosticsDoNotStoreArguments(t *testing.T) {
	arguments := `{"issue_key":"SECRET-123"}`
	diagnostics := diagnoseToolCalls([]provider.ToolCall{{ID: "call_1", Name: "mcp__jira__lookup", Arguments: arguments}})
	if len(diagnostics) != 1 || !diagnostics[0].ArgumentsJSON || diagnostics[0].ArgumentBytes != len(arguments) {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if strings.Contains(diagnostics[0].ArgumentsHash, "SECRET") {
		t.Fatal("diagnostics leaked argument content")
	}
}
