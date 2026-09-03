package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

type requestCountingProvider struct {
	requests []provider.ChatRequest
}

func (p *requestCountingProvider) Name() string { return "request-counting" }

func (p *requestCountingProvider) HealthCheck(context.Context) error { return nil }

func (p *requestCountingProvider) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: "test-model"}}, nil
}

func (p *requestCountingProvider) Chat(_ context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	p.requests = append(p.requests, req)
	events := make(chan provider.ChatEvent, 1)
	events <- provider.ChatEvent{Type: provider.EventDone}
	close(events)
	return events, nil
}

func discoveryTools(count int) []mcp.Tool {
	out := make([]mcp.Tool, 0, count)
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("tool_%02d", index)
		description := fmt.Sprintf("Inspect Jira resource %d", index)
		if index == 0 {
			name = "create_issue"
			description = "Create a Jira issue"
		}
		out = append(out, mcp.Tool{
			Server: "jira", Name: name, Description: description,
			Schema: json.RawMessage(fmt.Sprintf(`{"type":"object","properties":{"field_%d":{"type":"string"}}}`, index)),
		})
	}
	return out
}

func configureDiscoveryModel(t *testing.T, count int, callFunc func(string, json.RawMessage) (mcp.Result, error)) *Model {
	t.Helper()
	m := newTestModel(t)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.cfg.Tools.Discovery = config.ToolsDiscoveryConfig{Enabled: true, Threshold: 16, MaxResults: 5}
	m.mcpRegistry = newConnectedMCPRegistry(t, "jira", discoveryTools(count), callFunc)
	return m
}

func specByName(specs []provider.ToolSpec, name string) (provider.ToolSpec, bool) {
	for _, spec := range specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return provider.ToolSpec{}, false
}

func TestSmallCatalogPreservesExistingToolVisibility(t *testing.T) {
	m := configureDiscoveryModel(t, 2, nil)
	eligible := m.eligibleToolSpecs()
	visible := m.activeToolSpecs()
	if len(visible) != len(eligible) {
		t.Fatalf("small catalog visible=%d eligible=%d", len(visible), len(eligible))
	}
	if _, ok := specByName(visible, "mcp__jira__create_issue"); !ok {
		t.Fatal("small catalog hid a connected MCP tool")
	}
}

func TestLargeCatalogUsesProgressiveDisclosure(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	if len(m.eligibleToolSpecs()) <= m.toolDiscoveryThreshold() {
		t.Fatal("test catalog did not cross discovery threshold")
	}
	visible := m.activeToolSpecs()
	if _, ok := specByName(visible, tools.ToolSearch); !ok {
		t.Fatal("tool_search missing from compact visible set")
	}
	for _, spec := range visible {
		if _, _, dynamic := tools.SplitMCPToolName(spec.Name); dynamic {
			t.Fatalf("large catalog exposed MCP tool before discovery: %s", spec.Name)
		}
	}
}

func TestLargeCatalogPromotesCompactMCPDirectoryWithoutSchemas(t *testing.T) {
	m := configureDiscoveryModel(t, 22, nil)
	base := m.compositionBase("list Jira tools", nil, false)
	prompt := base.input.SystemPrompt

	for _, want := range []string{
		"Compact connected MCP directory (22 tools",
		`"jira" (22 tools)`,
		"create_issue",
		"tool_21",
		"Never pass an MCP name to run_command",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("compact directory missing %q:\n%s", want, prompt)
		}
	}
	for _, unwanted := range []string{`"field_0"`, "JSON input schema", "Inspect Jira resource"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("compact directory promoted schema/description %q:\n%s", unwanted, prompt)
		}
	}
	for _, spec := range m.activeToolSpecs() {
		if _, _, dynamic := tools.SplitMCPToolName(spec.Name); dynamic {
			t.Fatalf("compact directory made hidden schema callable: %s", spec.Name)
		}
	}
}

func TestCompactCatalogIdentifierIsQuotedAndBounded(t *testing.T) {
	identifier := "safe\n" + strings.Repeat("x", maxCompactIdentifierRunes+20)
	got := compactCatalogIdentifier(identifier)
	if strings.ContainsRune(got, '\n') || !strings.Contains(got, `\n`) {
		t.Fatalf("identifier line break was not escaped: %q", got)
	}
	if len([]rune(got)) > maxCompactIdentifierRunes+5 || !strings.Contains(got, "...") {
		t.Fatalf("identifier was not bounded: %q", got)
	}
}

func TestToolSearchDisclosesFullSchemaOnNextInference(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	name := "mcp__jira__create_issue"
	eligible, ok := specByName(m.eligibleToolSpecs(), name)
	if !ok {
		t.Fatal("eligible create_issue spec missing")
	}
	call := tools.CallsFromNative([]provider.ToolCall{{
		ID: "search-1", Name: tools.ToolSearch, Arguments: `{"query":"create Jira issue","max_results":3}`,
	}})[0]
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	if cmd := m.startToolBatch([]tools.Call{call}); cmd == nil {
		t.Fatal("tool_search did not continue to the next inference")
	}
	result := m.session.Messages[len(m.session.Messages)-1]
	if result.Role != provider.RoleTool || result.ToolCallID != "search-1" || !strings.Contains(result.Content, name) {
		t.Fatalf("search result = %+v", result)
	}
	disclosed, ok := specByName(m.activeToolSpecs(), name)
	if !ok || string(disclosed.Parameters) != string(eligible.Parameters) {
		t.Fatalf("next visible schema = %+v, eligible = %+v", disclosed, eligible)
	}
	if m.toolDepth != 1 {
		t.Fatalf("tool depth = %d, want search to consume one round", m.toolDepth)
	}
}

func TestToolSearchOnlyBatchPreservesResultsAndDeduplicatesDisclosure(t *testing.T) {
	executions := 0
	m := configureDiscoveryModel(t, 10, func(string, json.RawMessage) (mcp.Result, error) {
		executions++
		return mcp.Result{Content: "unexpected"}, nil
	})
	calls := tools.CallsFromNative([]provider.ToolCall{
		{ID: "search-1", Name: tools.ToolSearch, Arguments: `{"query":"mcp__jira__create_issue","max_results":1}`},
		{ID: "search-2", Name: tools.ToolSearch, Arguments: `{"query":"mcp__jira__create_issue","max_results":1}`},
	})
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
		{ID: calls[0].ID, Name: calls[0].Tool},
		{ID: calls[1].ID, Name: calls[1].Tool},
	}})

	counting := &requestCountingProvider{}
	m.prov = counting
	cmd := m.startToolBatch(calls)
	if cmd == nil {
		t.Fatal("search-only batch did not trigger one continuation")
	}
	if executions != 0 {
		t.Fatalf("search-only batch contacted MCP server %d time(s)", executions)
	}
	if len(m.disclosedToolOrder) != 1 || m.disclosedToolOrder[0] != "mcp__jira__create_issue" {
		t.Fatalf("disclosed order = %v", m.disclosedToolOrder)
	}
	results := m.session.Messages[len(m.session.Messages)-2:]
	for index, result := range results {
		if result.Role != provider.RoleTool || result.ToolCallID != calls[index].ID {
			t.Fatalf("result[%d] = %+v", index, result)
		}
		if !strings.Contains(result.Content, "mcp__jira__create_issue") {
			t.Fatalf("result[%d] missing match: %s", index, result.Content)
		}
	}
	if m.toolDepth != 1 {
		t.Fatalf("tool depth = %d, want one round for the batch", m.toolDepth)
	}
	_ = cmd()
	if len(counting.requests) != 1 {
		t.Fatalf("provider continuations = %d, want exactly one", len(counting.requests))
	}
}

func TestToolSearchOnlyBatchRejectsInvalidCallAtomically(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	calls := tools.CallsFromNative([]provider.ToolCall{
		{ID: "valid", Name: tools.ToolSearch, Arguments: `{"query":"create_issue","max_results":1}`},
		{ID: "invalid", Name: tools.ToolSearch, Arguments: `{"query":"","max_results":1}`},
	})
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
		{ID: calls[0].ID, Name: calls[0].Tool},
		{ID: calls[1].ID, Name: calls[1].Tool},
	}})

	if cmd := m.startToolBatch(calls); cmd == nil {
		t.Fatal("invalid search-only batch did not return correlated errors")
	}
	if len(m.disclosedToolOrder) != 0 {
		t.Fatalf("invalid atomic batch disclosed tools: %v", m.disclosedToolOrder)
	}
	results := m.session.Messages[len(m.session.Messages)-2:]
	for index, result := range results {
		if result.ToolCallID != calls[index].ID || !strings.HasPrefix(result.Content, "error:") {
			t.Fatalf("result[%d] = %+v", index, result)
		}
	}
}

func TestToolSearchOnlyBatchEnforcesTaskDisclosureCapInOrder(t *testing.T) {
	m := configureDiscoveryModel(t, 20, nil)
	nativeCalls := make([]provider.ToolCall, 0, maxTaskDisclosedTools+2)
	want := make([]string, 0, maxTaskDisclosedTools+2)
	for index := 1; index <= maxTaskDisclosedTools+2; index++ {
		name := fmt.Sprintf("mcp__jira__tool_%02d", index)
		want = append(want, name)
		nativeCalls = append(nativeCalls, provider.ToolCall{
			ID:        fmt.Sprintf("search-%02d", index),
			Name:      tools.ToolSearch,
			Arguments: fmt.Sprintf(`{"query":%q,"max_results":1}`, name),
		})
	}
	calls := tools.CallsFromNative(nativeCalls)
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: nativeCalls})

	if cmd := m.startToolBatch(calls); cmd == nil {
		t.Fatal("search-only batch did not continue")
	}
	if len(m.disclosedToolOrder) != maxTaskDisclosedTools {
		t.Fatalf("disclosed %d tools, want cap %d", len(m.disclosedToolOrder), maxTaskDisclosedTools)
	}
	for index, got := range m.disclosedToolOrder {
		if expected := want[index+2]; got != expected {
			t.Fatalf("disclosed order[%d] = %q, want %q", index, got, expected)
		}
	}
}

func TestToolSearchOnlyBatchKeepsFreshSearchWhenRepeatIsBlocked(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	m.progress = newProgressLedger(1)
	calls := tools.CallsFromNative([]provider.ToolCall{
		{ID: "stuck", Name: tools.ToolSearch, Arguments: `{"query":"create_issue","max_results":1}`},
		{ID: "fresh", Name: tools.ToolSearch, Arguments: `{"query":"tool_02","max_results":1}`},
	})
	m.progress.observeResults([]tools.Result{{Call: calls[0], Output: "unchanged search result"}})
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
		{ID: calls[0].ID, Name: calls[0].Tool},
		{ID: calls[1].ID, Name: calls[1].Tool},
	}})

	if cmd := m.startToolBatch(calls); cmd == nil {
		t.Fatal("partially blocked search batch did not continue")
	}
	results := m.session.Messages[len(m.session.Messages)-2:]
	if results[0].ToolCallID != "stuck" || !strings.HasPrefix(results[0].Content, "error:") {
		t.Fatalf("blocked result = %+v", results[0])
	}
	if results[1].ToolCallID != "fresh" || strings.HasPrefix(results[1].Content, "error:") {
		t.Fatalf("fresh result = %+v", results[1])
	}
	if m.toolOK != 1 || m.toolErr != 1 || !strings.Contains(m.notice, "blocked 1 repeat") {
		t.Fatalf("search accounting ok=%d err=%d notice=%q", m.toolOK, m.toolErr, m.notice)
	}
}

func TestToolSearchMixedBatchRemainsAtomic(t *testing.T) {
	tests := []struct {
		name string
		call provider.ToolCall
	}{
		{name: "builtin", call: provider.ToolCall{ID: "action", Name: tools.ToolListDir, Arguments: `{}`}},
		{name: "mcp", call: provider.ToolCall{ID: "action", Name: "mcp__jira__create_issue", Arguments: `{}`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := configureDiscoveryModel(t, 10, nil)
			search := provider.ToolCall{ID: "search", Name: tools.ToolSearch, Arguments: `{"query":"create_issue","max_results":1}`}
			calls := tools.CallsFromNative([]provider.ToolCall{search, test.call})
			before := len(m.session.Messages)
			m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{search, test.call}})
			if cmd := m.startToolBatch(calls); cmd == nil {
				t.Fatal("mixed batch did not return atomic errors")
			}
			if len(m.disclosedToolOrder) != 0 {
				t.Fatalf("mixed batch disclosed tools: %v", m.disclosedToolOrder)
			}
			results := m.session.Messages[before+1:]
			if len(results) != 2 {
				t.Fatalf("result count = %d", len(results))
			}
			for _, result := range results {
				if !strings.Contains(result.Content, "only tool_search calls") {
					t.Fatalf("mixed result = %+v", result)
				}
			}
		})
	}
}

func TestExactHiddenNativeMCPToolRecoversWithSchema(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	counting := &requestCountingProvider{}
	m.prov = counting
	name := "mcp__jira__create_issue"
	beforeHistory := []provider.Message{
		{Role: provider.RoleUser, Content: "open Jira"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "old-call", Name: name, Arguments: `{}`}}},
		{Role: provider.RoleTool, ToolCallID: "old-call", ToolName: name, Content: "previous result"},
		{Role: provider.RoleUser, Content: "now create another issue"},
	}
	m.session.Messages = append(m.session.Messages, beforeHistory...)
	beforeCount := len(m.session.Messages)
	m.resetTurn(m.cfg.Tools.NoProgress.Threshold, m.progressRoot())
	m.thinking = true

	_, cmd := m.handleStreamEvent(streamEventMsg{gen: m.streamGen, ok: true, event: provider.ChatEvent{
		Type: provider.EventError,
		Err: &provider.ToolNotOfferedError{
			RequestedName: name,
			OfferedNames:  activeToolNames(m.activeToolSpecs()),
		},
	}})
	if cmd == nil {
		t.Fatal("eligible hidden tool did not trigger a fresh inference")
	}
	if _, ok := specByName(m.activeToolSpecs(), name); !ok {
		t.Fatal("recovery did not disclose the exact eligible schema")
	}
	if len(m.session.Messages) != beforeCount {
		t.Fatalf("recovery changed conversation history: got %d messages, want %d", len(m.session.Messages), beforeCount)
	}
	if !strings.Contains(m.notice, "retrying with its schema") {
		t.Fatalf("notice = %q", m.notice)
	}
	if m.lastDebug.ToolsHash != toolSpecsFingerprint(m.activeToolSpecs()) {
		t.Fatal("debug fingerprint does not describe the recovered visible schema set")
	}
	_ = cmd()
	if len(counting.requests) != 1 {
		t.Fatalf("recovery provider requests = %d, want one", len(counting.requests))
	}
	offered, ok := specByName(counting.requests[0].Tools, name)
	eligible, eligibleOK := specByName(m.eligibleToolSpecs(), name)
	if !ok || !eligibleOK || string(offered.Parameters) != string(eligible.Parameters) {
		t.Fatalf("recovered provider schema = %+v, eligible = %+v", offered, eligible)
	}
}

func TestHiddenNativeMCPRecoveryRequiresExactEligibleNameAndIsBounded(t *testing.T) {
	tests := []struct {
		name      string
		requested string
		disable   bool
	}{
		{name: "unknown", requested: "mcp__jira__does_not_exist"},
		{name: "similar", requested: "mcp__jira__create_issu"},
		{name: "disconnected", requested: "mcp__jira__create_issue", disable: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			m := configureDiscoveryModel(t, 10, nil)
			if test.disable {
				if err := m.mcpRegistry.Disable("jira"); err != nil {
					t.Fatal(err)
				}
			}
			m.session.AddUser("continue")
			m.resetTurn(m.cfg.Tools.NoProgress.Threshold, m.progressRoot())
			m.thinking = true
			_, cmd := m.handleStreamEvent(streamEventMsg{gen: m.streamGen, ok: true, event: provider.ChatEvent{
				Type: provider.EventError,
				Err:  &provider.ToolNotOfferedError{RequestedName: test.requested},
			}})
			if cmd != nil || m.errText == "" {
				t.Fatalf("ineligible recovery: cmd=%v err=%q", cmd, m.errText)
			}
		})
	}

	m := configureDiscoveryModel(t, 10, nil)
	m.hasHiddenToolRecovery = true
	m.session.AddUser("continue")
	m.turnRuntime.transition(turnModelStreaming, turnOutcomeNone)
	m.thinking = true
	_, cmd := m.handleStreamEvent(streamEventMsg{gen: m.streamGen, ok: true, event: provider.ChatEvent{
		Type: provider.EventError,
		Err:  &provider.ToolNotOfferedError{RequestedName: "mcp__jira__create_issue"},
	}})
	if cmd != nil || m.errText == "" {
		t.Fatalf("second recovery was not bounded: cmd=%v err=%q", cmd, m.errText)
	}
}

func TestNewHumanTurnClearsToolDisclosure(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	name := "mcp__jira__create_issue"
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{
		ID: "previous-call", Name: name, Arguments: `{}`,
	}}})
	m.session.AddMessage(provider.Message{
		Role: provider.RoleTool, ToolCallID: "previous-call", ToolName: name, Content: "previous MCP result",
	})
	before := append([]provider.Message(nil), m.session.Messages...)
	m.discloseTools([]string{name})
	if _, ok := specByName(m.activeToolSpecs(), name); !ok {
		t.Fatal("setup did not disclose tool")
	}
	m.complete(turnOutcomeFinalAnswer)
	m.input.SetValue("start an unrelated task")
	_ = m.send()
	if _, ok := specByName(m.activeToolSpecs(), name); ok {
		t.Fatal("new human turn retained prior task disclosure")
	}
	if len(m.session.Messages) != len(before)+1 {
		t.Fatalf("new turn history length = %d, want %d", len(m.session.Messages), len(before)+1)
	}
	for index, message := range before {
		got := m.session.Messages[index]
		sameEnvelope := got.Role == message.Role &&
			got.Content == message.Content &&
			got.ToolCallID == message.ToolCallID &&
			got.ToolName == message.ToolName &&
			len(got.ToolCalls) == len(message.ToolCalls)
		if !sameEnvelope {
			t.Fatalf("history message %d changed: got %+v want %+v", index, got, message)
		}
	}
	if directory := m.compactMCPToolCatalogInstructions(); !strings.Contains(directory, "create_issue") {
		t.Fatalf("compact MCP directory disappeared after reset: %s", directory)
	}
}

func TestDisclosureChangesAndResetRestoresToolFingerprint(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	base := toolSpecsFingerprint(m.activeToolSpecs())
	m.discloseTools([]string{"mcp__jira__create_issue"})
	disclosed := toolSpecsFingerprint(m.activeToolSpecs())
	if disclosed == base {
		t.Fatal("disclosure did not change the provider/cache tool fingerprint")
	}
	m.resetToolDisclosure()
	if reset := toolSpecsFingerprint(m.activeToolSpecs()); reset != base {
		t.Fatalf("reset fingerprint = %q, want original %q", reset, base)
	}
}

func TestDisconnectedToolDisappearsFromSearchCatalog(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	name := "mcp__jira__create_issue"
	m.discloseTools([]string{name})
	if err := m.mcpRegistry.Disable("jira"); err != nil {
		t.Fatal(err)
	}
	if _, ok := specByName(m.activeToolSpecs(), name); ok {
		t.Fatal("disconnected disclosed MCP tool remained visible")
	}
	for _, spec := range m.searchableMCPToolSpecs() {
		if spec.Name == name {
			t.Fatal("disconnected MCP tool remained searchable")
		}
	}
}

func TestGuessedHiddenMCPToolCannotExecute(t *testing.T) {
	executions := 0
	m := configureDiscoveryModel(t, 10, func(string, json.RawMessage) (mcp.Result, error) {
		executions++
		return mcp.Result{Content: "executed"}, nil
	})
	call := tools.CallsFromNative([]provider.ToolCall{{
		ID: "hidden-1", Name: "mcp__jira__create_issue", Arguments: `{"summary":"hidden guess"}`,
	}})[0]
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	if cmd := m.startToolBatch([]tools.Call{call}); cmd == nil {
		t.Fatal("hidden rejection was not returned to the model")
	}
	if executions != 0 {
		t.Fatalf("hidden MCP tool executed %d time(s)", executions)
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || !strings.Contains(last.Content, "not disclosed") {
		t.Fatalf("hidden rejection = %+v", last)
	}
}

func TestDisclosedMCPToolStillRequiresApproval(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	name := "mcp__jira__create_issue"
	m.discloseTools([]string{name})
	call := tools.CallsFromNative([]provider.ToolCall{{ID: "mcp-1", Name: name, Arguments: `{}`}})[0]
	if !m.callNeedsApproval(call) {
		t.Fatal("tool discovery bypassed MCP approval policy")
	}
}

func TestRegistryMirrorsProgressivelyVisibleSnapshot(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	name := "mcp__jira__create_issue"
	if catalogHasTool(m.toolRegistryCatalog(), name) {
		t.Fatal("registry exposed hidden MCP tool")
	}
	m.discloseTools([]string{name})
	request := m.buildRequest(nil)
	catalog := m.toolRegistryCatalog()
	if !requestHasTool(request, name) || !catalogHasTool(catalog, name) || len(catalog) != len(request.Tools) {
		t.Fatalf("registry/request mismatch after disclosure: catalog=%d request=%d", len(catalog), len(request.Tools))
	}
}

func TestFallbackSearchExposesSchemaAndParsesMCPCall(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	m.toolsNative = false
	searchCalls := tools.Parse("```tool tool_search\n{\"query\":\"create Jira issue\",\"max_results\":1}\n```")
	if len(searchCalls) != 1 || searchCalls[0].SearchQuery == "" {
		t.Fatalf("fallback search call = %+v", searchCalls)
	}
	if cmd := m.startToolBatch(searchCalls); cmd == nil {
		t.Fatal("fallback tool_search did not continue")
	}
	name := "mcp__jira__create_issue"
	instructions := m.fencedDynamicToolInstructions()
	if !strings.Contains(instructions, name) || !strings.Contains(instructions, `"field_0"`) {
		t.Fatalf("fallback instructions missing disclosed full schema: %s", instructions)
	}
	mcpCalls := tools.Parse("```tool " + name + "\n{\"summary\":\"bug\"}\n```")
	if len(mcpCalls) != 1 || mcpCalls[0].MCPServer != "jira" || mcpCalls[0].MCPTool != "create_issue" || !strings.Contains(mcpCalls[0].MCPArgs, "summary") {
		t.Fatalf("fallback MCP call = %+v", mcpCalls)
	}
	if len(m.activeToolSpecs()) != 0 {
		t.Fatal("fallback mode changed native HTTP registry/provider semantics")
	}
}

func TestToolSearchResultLimitUsesConfigurationCap(t *testing.T) {
	m := configureDiscoveryModel(t, 12, nil)
	m.cfg.Tools.Discovery.MaxResults = 2
	call := tools.CallsFromNative([]provider.ToolCall{{Name: tools.ToolSearch, Arguments: `{"query":"jira","max_results":8}`}})[0]
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	_ = m.startToolBatch([]tools.Call{call})
	if len(m.disclosedToolOrder) != 2 {
		t.Fatalf("disclosed tools = %v, want config cap 2", m.disclosedToolOrder)
	}
}

func TestToolSearchResultLabelsPartialCatalogAndReportsTotal(t *testing.T) {
	m := configureDiscoveryModel(t, 22, nil)
	call := tools.CallsFromNative([]provider.ToolCall{{
		ID: "search-total", Name: tools.ToolSearch, Arguments: `{"query":"jira","max_results":5}`,
	}})[0]
	m.session.AddMessage(provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{
			ID: call.ID, Name: call.Tool,
		}},
	})
	_ = m.startToolBatch([]tools.Call{call})

	last := m.session.Messages[len(m.session.Messages)-1]
	var result toolSearchResult
	if err := json.Unmarshal([]byte(last.Content), &result); err != nil {
		t.Fatalf("decode tool search result: %v\n%s", err, last.Content)
	}
	if len(result.Matches) != 5 || result.TotalMatches != 22 || !result.Truncated {
		t.Fatalf("result = %+v, want five of 22 marked truncated", result)
	}
	if !strings.Contains(result.Hint, "Partial shortlist") || !strings.Contains(result.Hint, "complete catalog") {
		t.Fatalf("partial-result hint = %q", result.Hint)
	}
}

func TestInternalMCPCallWithoutJoinedNameRemainsExecutable(t *testing.T) {
	m := newActivityTestModel(t, 0)
	call := tools.Call{ID: "internal-1", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{}`}
	cmd := m.startToolBatch([]tools.Call{call})
	if cmd == nil || m.mcpBatchCancel == nil {
		names := activeToolNames(m.modelVisibleToolSpecs())
		t.Fatalf("internal MCP call did not start; visible=%v pending=%v err=%q", names, m.pendingCalls, m.errText)
	}
}

func TestToolSearchHonorsAndRenewsToolRoundBudget(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	m.cfg.Tools.MaxIterations = 1
	m.toolDepth = 1
	call := tools.CallsFromNative([]provider.ToolCall{{
		ID: "search-budget", Name: tools.ToolSearch, Arguments: `{"query":"create Jira issue","max_results":1}`,
	}})[0]
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	if cmd := m.startToolBatch([]tools.Call{call}); cmd != nil || !m.pendingBudget {
		t.Fatalf("spent budget did not pause: cmd=%v pending=%v", cmd, m.pendingBudget)
	}
	cmd := m.resolveBudget(0)
	if cmd == nil || m.pendingBudget || len(m.disclosedToolOrder) != 1 || m.toolDepth != 1 {
		t.Fatalf("renewed search state: cmd=%v pending=%v disclosed=%v depth=%d", cmd, m.pendingBudget, m.disclosedToolOrder, m.toolDepth)
	}
}

func TestHiddenMCPRejectionCannotBypassToolRoundBudget(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	m.cfg.Tools.MaxIterations = 1
	m.toolDepth = 1
	call := tools.CallsFromNative([]provider.ToolCall{{
		ID: "hidden-budget", Name: "mcp__jira__create_issue", Arguments: `{}`,
	}})[0]
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: call.ID, Name: call.Tool}}})
	if cmd := m.startToolBatch([]tools.Call{call}); cmd != nil || !m.pendingBudget {
		t.Fatalf("hidden call bypassed spent budget: cmd=%v pending=%v", cmd, m.pendingBudget)
	}
}
