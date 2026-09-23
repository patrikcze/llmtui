package contextmgr

import (
	"context"
	"fmt"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func BenchmarkHeuristicSummarizerRuntimeReferences(b *testing.B) {
	messages := make([]provider.Message, 0, 80)
	for i := 0; i < 80; i++ {
		messages = append(messages, provider.Message{
			Role:    provider.RoleUser,
			Content: fmt.Sprintf("[retained output — read it back with read_file resource_id ent_%026d] line %d", i, i),
		})
	}
	in := SummaryInput{Messages: messages, MaxTokens: 1200}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := (HeuristicSummarizer{}).Summarize(context.Background(), in); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecideTokenBudget(b *testing.B) {
	messages := make([]provider.Message, 0, 120)
	for i := 0; i < 120; i++ {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("message %d with bounded context evidence", i)})
	}
	params := Params{Strategy: StrategyAuto, ContextWindow: 4096, ReserveResponseTokens: 512, SummarizeAfterMessages: 12, FixedTokens: 300}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Decide(messages, params)
	}
}
