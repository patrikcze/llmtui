package tools

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestToolSearchDecodingAndBounds(t *testing.T) {
	call := CallsFromNative([]provider.ToolCall{{Name: ToolSearch, Arguments: `{"query":" create Jira issue ","max_results":3}`}})[0]
	if call.InputErr != "" || call.SearchQuery != "create Jira issue" || call.Max != 3 {
		t.Fatalf("call = %+v", call)
	}
	invalid := []string{
		`{}`, `{"query":" "}`, `{"query":"x","max_results":9}`,
		`{"query":"` + strings.Repeat("x", MaxToolSearchQueryRunes+1) + `"}`,
		`{"query":"x","unexpected":true}`,
	}
	for _, arguments := range invalid {
		got := CallsFromNative([]provider.ToolCall{{Name: ToolSearch, Arguments: arguments}})[0]
		if got.InputErr == "" {
			t.Fatalf("accepted invalid arguments %q", arguments)
		}
	}
}

func TestToolSearchRankingAndDeterministicLimit(t *testing.T) {
	candidates := []ToolSearchCandidate{
		{Name: "mcp__github__create_issue", Description: "Create a GitHub issue", Source: "mcp:github"},
		{Name: "mcp__jira__get_project", Description: "Get Jira project information", Source: "mcp:jira"},
		{Name: "mcp__jira__create_issue", Description: "Create a Jira issue", Source: "mcp:jira"},
		{Name: "mcp__azure__list_issues", Description: "List work items", Source: "mcp:azure"},
	}
	matches := SearchTools("jira create issue", 2, candidates)
	if len(matches) != 2 || matches[0].Name != "mcp__jira__create_issue" || matches[1].Name != "mcp__github__create_issue" {
		t.Fatalf("matches = %+v", matches)
	}
	again := SearchTools("jira create issue", 2, candidates)
	if matches[0].Name != again[0].Name || matches[1].Name != again[1].Name {
		t.Fatalf("non-deterministic matches: %+v vs %+v", matches, again)
	}

	exact := SearchTools("mcp__jira__get_project", 1, candidates)
	if len(exact) != 1 || exact[0].Name != "mcp__jira__get_project" {
		t.Fatalf("exact match = %+v", exact)
	}
	source := SearchTools("azure", 8, candidates)
	if len(source) == 0 || source[0].Name != "mcp__azure__list_issues" {
		t.Fatalf("source match = %+v", source)
	}
}
