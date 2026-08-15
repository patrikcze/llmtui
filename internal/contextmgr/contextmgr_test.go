package contextmgr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func msgs(n int, contentLen int) []provider.Message {
	out := make([]provider.Message, n)
	for i := range out {
		role := provider.RoleUser
		if i%2 == 1 {
			role = provider.RoleAssistant
		}
		out[i] = provider.Message{Role: role, Content: strings.Repeat("word ", contentLen/5)}
	}
	return out
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(nil); got != 0 {
		t.Errorf("empty = %d, want 0", got)
	}
	m := []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("x", 400)}}
	got := EstimateTokens(m)
	if got < 100 || got > 110 {
		t.Errorf("400 chars ≈ %d tokens, want ~104", got)
	}
}

func TestEstimateTokensIncludesStructuredMessageFields(t *testing.T) {
	structured := []provider.Message{
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID:        "call_123",
				Name:      "mcp__jiraWorklog__session_start",
				Arguments: `{"issue_key":"` + strings.Repeat("X", 1200) + `"}`,
			}},
		},
		{
			Role:       provider.RoleTool,
			Content:    "ok",
			ToolCallID: "call_123",
			ToolName:   "mcp__jiraWorklog__session_start",
		},
		{
			Role:   provider.RoleUser,
			Images: []provider.Image{{Data: []byte("image bytes"), MIME: "image/png"}},
		},
	}

	if got := EstimateTokens(structured); got < 550 {
		t.Fatalf("structured messages = %d estimated tokens, want tool arguments and image overhead included", got)
	}
}

func TestDecideIncludesFixedRequestOverhead(t *testing.T) {
	p := Params{
		Strategy:              StrategyTruncate,
		ContextWindow:         1000,
		ReserveResponseTokens: 100,
		FixedTokens:           850,
	}
	d := Decide([]provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("x", 400)}}, p)
	if !d.Compress {
		t.Fatalf("decision = %+v, want fixed prompt/tool overhead to trigger compression", d)
	}
	if d.Used < 950 {
		t.Errorf("used = %d, want message plus fixed overhead", d.Used)
	}
}

func TestDecideNone(t *testing.T) {
	d := Decide(msgs(50, 1000), Params{Strategy: StrategyNone, ContextWindow: 1000, ReserveResponseTokens: 100})
	if d.Compress {
		t.Error("strategy none must never compress")
	}
}

func TestDecideTruncateOnlyWhenOverBudget(t *testing.T) {
	p := Params{Strategy: StrategyTruncate, ContextWindow: 100000, ReserveResponseTokens: 2048, SummarizeAfterMessages: 4}
	if d := Decide(msgs(10, 100), p); d.Compress {
		t.Error("under budget must not compress even with many messages")
	}
	p.ContextWindow = 300
	p.ReserveResponseTokens = 100
	if d := Decide(msgs(10, 400), p); !d.Compress {
		t.Error("over budget must compress")
	}
}

func TestDecideAutoPicksSummarizeForLongConversations(t *testing.T) {
	p := Params{Strategy: StrategyAuto, ContextWindow: 100000, ReserveResponseTokens: 2048, SummarizeAfterMessages: 6}
	d := Decide(msgs(8, 100), p)
	if !d.Compress || d.Strategy != StrategySummarize {
		t.Errorf("auto with %d messages = %+v, want summarize", 8, d)
	}

	// Short but over budget → truncate.
	p.ContextWindow = 150
	p.ReserveResponseTokens = 50
	d = Decide(msgs(4, 400), p)
	if !d.Compress || d.Strategy != StrategyTruncate {
		t.Errorf("auto over-budget short = %+v, want truncate", d)
	}
}

func TestSplitKeepsLastMessages(t *testing.T) {
	conversation := append([]provider.Message{{Role: provider.RoleSystem, Content: "sys"}}, msgs(10, 50)...)
	older, recent := Split(conversation, 4)
	if len(recent) != 4 || len(older) != 6 {
		t.Fatalf("Split = %d older, %d recent, want 6/4", len(older), len(recent))
	}
	for _, m := range append(older, recent...) {
		if m.Role == provider.RoleSystem {
			t.Error("Split must exclude system messages")
		}
	}
	// Fewer messages than keepLast → everything is recent.
	older, recent = Split(msgs(2, 10), 8)
	if len(older) != 0 || len(recent) != 2 {
		t.Errorf("small Split = %d/%d, want 0/2", len(older), len(recent))
	}
}

func TestSplitNeverSeversToolCallResultPairs(t *testing.T) {
	conv := []provider.Message{
		{Role: provider.RoleUser, Content: "u1"},
		{Role: provider.RoleAssistant, Content: "a1"},
		{Role: provider.RoleUser, Content: "u2"},
		{Role: provider.RoleAssistant, Content: "", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file"}, {ID: "c2", Name: "list_dir"}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Content: "file contents"},
		{Role: provider.RoleTool, ToolCallID: "c2", Content: "dir listing"},
		{Role: provider.RoleAssistant, Content: "done"},
	}

	// keepLast=3 would open the kept window on the second tool result,
	// leaving a role:"tool" message without its assistant tool-call message —
	// protocol-invalid for OpenAI-compatible backends. The window must widen
	// backwards to start at the assistant message carrying the calls.
	older, recent := Split(conv, 3)
	if len(recent) < 2 || recent[1].Role == provider.RoleTool {
		t.Fatalf("recent = %+v, must retain the user before a complete tool group", recent)
	}
	if recent[0].Role != provider.RoleUser || recent[1].Role != provider.RoleAssistant || len(recent[1].ToolCalls) != 2 {
		t.Fatalf("recent = %+v, want user anchor followed by the assistant tool calls", recent)
	}
	if len(older)+len(recent) != len(conv) {
		t.Errorf("messages lost: %d + %d != %d", len(older), len(recent), len(conv))
	}

	// Even when the count boundary lands on a non-tool message, the latest
	// user remains the anchor for the retained active turn.
	older, recent = Split(conv, 1)
	if len(recent) != 2 || len(older) != 5 {
		t.Errorf("Split(conv, 1) = %d/%d, want 5/2", len(older), len(recent))
	}
	if recent[0].Role != provider.RoleUser || recent[0].Content != "u2" {
		t.Errorf("recent[0] = %+v, want latest user anchor", recent[0])
	}
}

func TestSplitPreservesLatestUserAcrossSevenToolRounds(t *testing.T) {
	conv := []provider.Message{{Role: provider.RoleUser, Content: "run the benchmark"}}
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("call_%d", i)
		conv = append(conv,
			provider.Message{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{{
					ID: id, Name: "read_file", Arguments: `{"path":"input.txt"}`,
				}},
			},
			provider.Message{Role: provider.RoleTool, ToolCallID: id, Content: fmt.Sprintf("result_%d", i)},
		)
	}

	older, recent := Split(conv, 8)
	if len(older) != 6 || len(recent) != 9 {
		t.Fatalf("Split = %d older/%d recent, want 6/9", len(older), len(recent))
	}
	if recent[0].Role != provider.RoleUser || recent[0].Content != "run the benchmark" {
		t.Fatalf("recent[0] = %+v, want original user anchor", recent[0])
	}
	if got := recent[len(recent)-1]; got.Role != provider.RoleTool || got.Content != "result_7" {
		t.Fatalf("last recent message = %+v, want newest tool result", got)
	}
	for _, group := range [][]provider.Message{older, recent[1:]} {
		for i := 0; i < len(group); i += 2 {
			if i+1 >= len(group) || group[i].Role != provider.RoleAssistant || group[i+1].Role != provider.RoleTool {
				t.Fatalf("tool group was severed: %+v", group)
			}
			if group[i].ToolCalls[0].ID != group[i+1].ToolCallID {
				t.Fatalf("tool IDs differ: call=%q result=%q", group[i].ToolCalls[0].ID, group[i+1].ToolCallID)
			}
		}
	}
}

func TestSplitWithZeroKeepStillRetainsNewestToolPair(t *testing.T) {
	conv := []provider.Message{
		{Role: provider.RoleUser, Content: "run the benchmark"},
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID: "call_1", Name: "read_file", Arguments: `{"path":"input.txt"}`,
			}},
		},
		{Role: provider.RoleTool, ToolCallID: "call_1", Content: "result"},
	}

	older, recent := Split(conv, 0)
	if len(older) != 0 || len(recent) != 3 {
		t.Fatalf("Split = %d older/%d recent, want 0/3", len(older), len(recent))
	}
	if recent[0].Role != provider.RoleUser ||
		recent[1].Role != provider.RoleAssistant ||
		recent[2].Role != provider.RoleTool {
		t.Fatalf("recent = %+v, want user plus newest complete tool pair", recent)
	}
}

func TestHeuristicSummarizerKeepsTechnicalDetail(t *testing.T) {
	input := SummaryInput{
		MaxTokens: 500,
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "How do I configure viper? I keep getting an error: config file not found in ~/.config/llmtui/config.yaml"},
			{Role: provider.RoleAssistant, Content: "You need to run the init command.\n```go\nviper.SetConfigFile(path)\n```\nWe decided to use LLMTUI_ as the env prefix."},
			{Role: provider.RoleUser, Content: "Nice weather today by the way, anyway thanks."},
		},
	}
	out, err := HeuristicSummarizer{}.Summarize(context.Background(), input)
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	for _, want := range []string{"config.yaml", "viper.SetConfigFile", "decided"} {
		if !strings.Contains(out.Summary, want) {
			t.Errorf("summary missing technical detail %q:\n%s", want, out.Summary)
		}
	}
	if !strings.Contains(out.Summary, "user:") || !strings.Contains(out.Summary, "assistant:") {
		t.Error("summary should attribute content to roles")
	}
}

func TestHeuristicSummarizerKeepsStructuredToolGroups(t *testing.T) {
	input := SummaryInput{
		MaxTokens: 200,
		Messages: []provider.Message{
			{
				Role: provider.RoleAssistant,
				ToolCalls: []provider.ToolCall{{
					ID: "call_1", Name: "read_file", Arguments: `{"path":"benchmark_input.txt"}`,
				}},
			},
			{
				Role:       provider.RoleTool,
				ToolCallID: "call_1",
				ToolName:   "read_file",
				Content:    "alpha=17\nbeta=25",
			},
		},
	}

	out, err := (HeuristicSummarizer{}).Summarize(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read_file", "benchmark_input.txt", "alpha=17"} {
		if !strings.Contains(out.Summary, want) {
			t.Errorf("summary missing structured tool detail %q:\n%s", want, out.Summary)
		}
	}
}

func TestHeuristicSummarizerRespectsBudget(t *testing.T) {
	long := msgs(50, 2000)
	out, err := HeuristicSummarizer{}.Summarize(context.Background(), SummaryInput{Messages: long, MaxTokens: 100})
	if err != nil {
		t.Fatal(err)
	}
	if got := provider.EstimateTokens(out.Summary); got > 150 {
		t.Errorf("summary ≈ %d tokens, want ≤ ~100 budget", got)
	}
}

// TestHeuristicSummarizerCapsLongCodeLines guards against a single very long
// fenced-code line (minified code, a base64 blob, a one-line JSON dump)
// blowing the summary past MaxTokens: the budget check in Summarize only
// runs before appending each line, so an uncapped line can push the result
// arbitrarily far over budget in one step.
func TestHeuristicSummarizerCapsLongCodeLines(t *testing.T) {
	longLine := strings.Repeat("x", 5000)
	input := SummaryInput{
		MaxTokens: 100,
		Messages: []provider.Message{
			{Role: provider.RoleAssistant, Content: "Here you go:\n```\n" + longLine + "\n```"},
		},
	}
	out, err := HeuristicSummarizer{}.Summarize(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Summary, longLine) {
		t.Error("long code line was included verbatim, uncapped")
	}
	if got := provider.EstimateTokens(out.Summary); got > 150 {
		t.Errorf("summary ≈ %d tokens, want capped near the ~100 budget even with one long code line", got)
	}
}

func TestValidStrategy(t *testing.T) {
	for _, s := range []string{StrategyNone, StrategyTruncate, StrategySummarize, StrategyAuto} {
		if !ValidStrategy(s) {
			t.Errorf("ValidStrategy(%q) = false", s)
		}
	}
	if ValidStrategy("bogus") {
		t.Error("ValidStrategy(bogus) = true")
	}
}
