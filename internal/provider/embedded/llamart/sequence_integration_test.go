package llamart

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

// TestIsolatedRequestPreservesConversationPrefix runs against a real GGUF
// (skips without YZMA_LIB / LLMTUI_TEST_GGUF). An isolated control request
// must leave the conversation's cached tokens intact, the next conversation
// request must reuse all of them, and the isolated reply must match the same
// request evaluated on the conversation sequence.
func TestIsolatedRequestPreservesConversationPrefix(t *testing.T) {
	opts := integrationOptions(t)
	opts.ContextSize = 4096
	rt := New()
	if _, err := rt.Load(context.Background(), opts, func(string) {}); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	if rt.nSeqMax < contextSequences {
		t.Fatalf("context accepted %d sequences, want %d", rt.nSeqMax, contextSequences)
	}

	conversation := []provider.Message{
		{Role: provider.RoleSystem, Content: "You are a terse assistant. " + strings.Repeat("Answer briefly and precisely. ", 40)},
		{Role: provider.RoleUser, Content: "Name one primary color."},
	}
	generateIntegration(t, rt, embedded.GenRequest{Messages: conversation, Temperature: 0, TopP: 1, MaxTokens: 8})
	cached := slices.Clone(rt.kvTokens)
	if len(cached) < 100 {
		t.Fatalf("conversation cached %d tokens, want a real prefix", len(cached))
	}

	control := embedded.GenRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "Return only a JSON object."},
			{Role: provider.RoleUser, Content: `Reply with {"ok":true}.`},
		},
		Temperature: 0, TopP: 1, MaxTokens: 16, Isolated: true,
	}
	isolated := generateIntegration(t, rt, control)
	if !slices.Equal(rt.kvTokens, cached) {
		t.Fatalf("isolated request changed the conversation cache: %d tokens before, %d after", len(cached), len(rt.kvTokens))
	}

	followUp := append(slices.Clone(conversation),
		provider.Message{Role: provider.RoleAssistant, Content: "Red."},
		provider.Message{Role: provider.RoleUser, Content: "Another one?"},
	)
	before := slices.Clone(rt.kvTokens)
	next := generateIntegration(t, rt, embedded.GenRequest{Messages: followUp, Temperature: 0, TopP: 1, MaxTokens: 8})
	reused := min(commonPrefix(before, rt.kvTokens), next.result.PromptTokens)
	t.Logf("conversation cached %d tokens; follow-up prompt %d tokens, reused %d, evaluated %d", len(cached), next.result.PromptTokens, reused, next.result.PromptTokens-reused)
	// The follow-up may diverge inside the first request's generated tail, but
	// never before the prompt part of the cached conversation.
	if reused < len(cached)-8 {
		t.Fatalf("follow-up reused %d of %d cached tokens; the isolated request displaced the conversation", reused, len(cached))
	}

	control.Isolated = false
	shared := generateIntegration(t, rt, control)
	if isolated.text != shared.text {
		t.Fatalf("isolated reply %q differs from the same request on the conversation sequence %q", isolated.text, shared.text)
	}
}
