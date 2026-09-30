package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestRuntimeContextPlacement: runtime sections move after history for
// native-tool continuations on every provider (plan step 2). A turn's first
// request defers them only as prompt.fresh_runtime_context allows (plan step
// 6): auto means embedded yes, remote no. Fenced requests never defer, and
// the raw user message always stays last and verbatim.
func TestRuntimeContextPlacement(t *testing.T) {
	for _, tc := range []struct {
		name, providerType, fresh      string
		native, continuation, deferred bool
	}{
		{name: "embedded continuation", providerType: "embedded", native: true, continuation: true, deferred: true},
		{name: "fresh embedded chat, auto", providerType: "embedded", native: true, deferred: true},
		{name: "fresh embedded chat, system", providerType: "embedded", fresh: config.FreshRuntimeContextSystem, native: true},
		{name: "fenced continuation", providerType: "embedded", continuation: true},
		{name: "fenced fresh embedded chat", providerType: "embedded"},
		{name: "remote continuation", providerType: "openai", native: true, continuation: true, deferred: true},
		{name: "fresh remote chat, auto", providerType: "openai", native: true},
		{name: "fresh remote chat, message", providerType: "openai", fresh: config.FreshRuntimeContextMessage, native: true, deferred: true},
		{name: "remote continuation, system", providerType: "openai", fresh: config.FreshRuntimeContextSystem, native: true, continuation: true, deferred: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			m.cfg.Providers = map[string]config.ProviderConfig{m.cfg.Provider: {Type: tc.providerType}}
			m.cfg.Prompt.FreshRuntimeContext = tc.fresh
			m.toolsOn, m.toolsNative = true, tc.native
			m.toolRunner = tools.NewRunner(t.TempDir(), 64)
			m.toolRecoveryFeedback = "schema recovery diagnostic"
			base := m.compositionBase("original user", nil, tc.continuation)
			if base.input.RuntimeContextAfterHistory != tc.deferred {
				t.Fatalf("deferred=%v; want %v", base.input.RuntimeContextAfterHistory, tc.deferred)
			}
			out := composeFromBase(base, nil, "")
			inSystem := strings.Contains(out.Messages[0].Content, m.toolRecoveryFeedback)
			if inSystem == tc.deferred {
				t.Fatal("tool recovery feedback has incorrect system placement")
			}
			last := out.Messages[len(out.Messages)-1]
			if !tc.continuation && (last.Content != "original user" || last.Role != "user") {
				t.Fatalf("raw user message is not last and verbatim: %+v", last)
			}
			if !tc.deferred {
				return
			}
			context := last
			if !tc.continuation {
				context = out.Messages[len(out.Messages)-2]
			}
			if !strings.Contains(context.Content, m.toolRecoveryFeedback) {
				t.Fatal("recovery feedback was lost instead of deferred")
			}
		})
	}
}

func TestWithTurnContextReinsertsBeforeRawUser(t *testing.T) {
	history := []provider.Message{
		{Role: provider.RoleUser, Content: "older question"},
		{Role: provider.RoleAssistant, Content: "older answer"},
		{Role: provider.RoleUser, Content: "raw request"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "r1", Name: "read_file"}}},
		{Role: provider.RoleTool, ToolCallID: "r1", Content: "result"},
	}
	got := withTurnContext(history, "raw request", "turn context")
	if len(got) != len(history)+1 || got[2].Content != "turn context" || got[2].Role != provider.RoleUser || got[3].Content != "raw request" {
		t.Fatalf("context not reinserted before the raw user message: %+v", got)
	}
	if len(history) != 5 || history[2].Content != "raw request" {
		t.Fatal("withTurnContext mutated its input")
	}
	if same := withTurnContext(history, "compacted away", "turn context"); len(same) != len(history) {
		t.Fatal("context inserted although the raw message is gone")
	}
	if same := withTurnContext(history, "raw request", ""); len(same) != len(history) {
		t.Fatal("empty context inserted")
	}
}
