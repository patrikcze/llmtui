package contextmgr

import (
	"context"
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
