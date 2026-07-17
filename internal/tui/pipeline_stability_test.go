package tui

import (
	"encoding/json"
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
