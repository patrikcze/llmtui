package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// TestSummarizerKeepsNewestUnderBudgetPressure is the regression for audit
// P2-8: the summary used to fill its budget from the oldest message and
// silently drop the newest compacted ones — the ones closest to the work in
// progress. It must now keep the newest, in chronological order, and say
// how many earlier messages it omitted.
func TestSummarizerKeepsNewestUnderBudgetPressure(t *testing.T) {
	var messages []provider.Message
	for i := 1; i <= 40; i++ {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("Step %d changed file step%d.go.", i, i)})
	}
	out, err := HeuristicSummarizer{}.Summarize(context.Background(), SummaryInput{Messages: messages, MaxTokens: 120})
	if err != nil {
		t.Fatal(err)
	}
	summary := out.Summary
	if !strings.Contains(summary, "Step 40 changed file step40.go") {
		t.Fatalf("newest compacted message missing:\n%s", summary)
	}
	if strings.Contains(summary, "Step 1 changed") {
		t.Fatalf("oldest message kept while budget was exceeded:\n%s", summary)
	}
	if !strings.HasPrefix(summary, "- (") || !strings.Contains(summary, "earlier messages omitted from this summary)") {
		t.Fatalf("summary does not state the omission:\n%s", summary)
	}
	if strings.Index(summary, "Step 39") > strings.Index(summary, "Step 40") {
		t.Fatalf("kept messages are not in chronological order:\n%s", summary)
	}
	if got := provider.EstimateTokens(summary); got > 120 {
		t.Fatalf("summary ≈ %d tokens, over the 120 budget", got)
	}
}

func TestSummarizerWithinBudgetHasNoOmissionMarker(t *testing.T) {
	out, err := HeuristicSummarizer{}.Summarize(context.Background(), SummaryInput{MaxTokens: 500, Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "Fix the failing test in parser.go."},
		{Role: provider.RoleAssistant, Content: "Updated parser.go and the test passed."},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Summary, "omitted") || !strings.Contains(out.Summary, "parser.go") {
		t.Fatalf("summary = %q", out.Summary)
	}
}

// TestSummarizerDropsToolCallAndResultTogether keeps a call and its outcome
// in one selection unit: under pressure either both survive or neither.
func TestSummarizerDropsToolCallAndResultTogether(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "run_command", Arguments: `{"command":"go test ./old/..."}`}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "run_command", Content: "exit status 1 in old package"},
	}
	for i := 0; i < 30; i++ {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("Later note %d about config.yaml.", i)})
	}
	out, err := HeuristicSummarizer{}.Summarize(context.Background(), SummaryInput{Messages: messages, MaxTokens: 150})
	if err != nil {
		t.Fatal(err)
	}
	hasCall := strings.Contains(out.Summary, "go test ./old/...")
	hasResult := strings.Contains(out.Summary, "exit status 1 in old package")
	if hasCall != hasResult {
		t.Fatalf("tool call and result split by selection (call=%v result=%v):\n%s", hasCall, hasResult, out.Summary)
	}
}
