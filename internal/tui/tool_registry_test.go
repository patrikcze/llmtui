package tui

import (
	"encoding/json"
	"testing"

	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/toolapi"
)

func TestToolRegistryCatalogMatchesProviderRequestTools(t *testing.T) {
	model := newTestModel(t)
	model.toolsOn = true
	model.toolsNative = true
	model.webOn = true
	model.mcpRegistry = newConnectedMCPRegistry(t, "jira", []mcp.Tool{{
		Server:      "jira",
		Name:        "issue_search",
		Description: "search issues",
		Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
	}}, nil)

	request := model.buildRequest(nil)
	catalog := model.toolRegistryCatalog()
	if len(catalog) != len(request.Tools) {
		t.Fatalf("registry tools = %d, request tools = %d", len(catalog), len(request.Tools))
	}
	for i := range request.Tools {
		if catalog[i].Name != request.Tools[i].Name || string(catalog[i].InputSchema) != string(request.Tools[i].Parameters) {
			t.Fatalf("tool %d differs: registry=%+v request=%+v", i, catalog[i], request.Tools[i])
		}
	}
	last := catalog[len(catalog)-1]
	if last.Name != "mcp__jira__issue_search" || last.Source != "mcp:jira" || last.Safety != "external_mcp" {
		t.Fatalf("MCP registry metadata = %+v", last)
	}
}

func TestToolRegistryCatalogTracksMCPConnectionLifecycle(t *testing.T) {
	model := newTestModel(t)
	model.toolsOn = true
	model.toolsNative = true
	model.mcpRegistry = newConnectedMCPRegistry(t, "jira", []mcp.Tool{{
		Server: "jira", Name: "issue_search", Schema: json.RawMessage(`{"type":"object"}`),
	}}, nil)

	if !catalogHasTool(model.toolRegistryCatalog(), "mcp__jira__issue_search") {
		t.Fatal("connected MCP tool missing from registry")
	}
	if !requestHasTool(model.buildRequest(nil), "mcp__jira__issue_search") {
		t.Fatal("connected MCP tool missing from provider request")
	}
	if err := model.mcpRegistry.Disable("jira"); err != nil {
		t.Fatal(err)
	}
	if catalogHasTool(model.toolRegistryCatalog(), "mcp__jira__issue_search") {
		t.Fatal("disabled MCP tool remained in registry")
	}
	if requestHasTool(model.buildRequest(nil), "mcp__jira__issue_search") {
		t.Fatal("disabled MCP tool remained in provider request")
	}
}

func TestToolRegistryCatalogEmptyWhenNativeToolsAreUnavailable(t *testing.T) {
	model := newTestModel(t)
	model.toolsOn = true
	model.toolsNative = false
	if catalog := model.toolRegistryCatalog(); len(catalog) != 0 {
		t.Fatalf("catalog = %+v, want no native tools", catalog)
	}
}

func catalogHasTool(catalog []toolapi.Tool, name string) bool {
	for _, tool := range catalog {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func requestHasTool(request provider.ChatRequest, name string) bool {
	for _, tool := range request.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
