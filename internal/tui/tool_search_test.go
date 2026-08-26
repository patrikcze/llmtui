package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

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

func TestNewHumanTurnClearsToolDisclosure(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	name := "mcp__jira__create_issue"
	m.discloseTools([]string{name})
	if _, ok := specByName(m.activeToolSpecs(), name); !ok {
		t.Fatal("setup did not disclose tool")
	}
	m.turnRuntime.complete(turnOutcomeFinalAnswer)
	m.input.SetValue("start an unrelated task")
	_ = m.send()
	if _, ok := specByName(m.activeToolSpecs(), name); ok {
		t.Fatal("new human turn retained prior task disclosure")
	}
}

func TestDisconnectedDisclosedToolDisappears(t *testing.T) {
	m := configureDiscoveryModel(t, 10, nil)
	name := "mcp__jira__create_issue"
	m.discloseTools([]string{name})
	if err := m.mcpRegistry.Disable("jira"); err != nil {
		t.Fatal(err)
	}
	if _, ok := specByName(m.activeToolSpecs(), name); ok {
		t.Fatal("disconnected disclosed MCP tool remained visible")
	}
	for _, spec := range m.hiddenToolSpecs() {
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

func TestInternalMCPCallWithoutJoinedNameRemainsExecutable(t *testing.T) {
	m := newActivityTestModel(t, 0)
	call := tools.Call{ID: "internal-1", MCPServer: "jiraWorklog", MCPTool: "session_start", MCPArgs: `{}`}
	cmd := m.startToolBatch([]tools.Call{call})
	if cmd == nil || m.mcpBatchCancel == nil {
		names := activeToolNames(m.modelVisibleToolSpecs())
		t.Fatalf("internal MCP call did not start; visible=%v pending=%v err=%q", names, m.pendingCalls, m.errText)
	}
}
