package llamart

import (
	"context"
	"strings"
	"testing"

	"github.com/hybridgroup/yzma/pkg/llama"

	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

// Opt-in like the other native tests: validates the model's actual metadata
// template, native tokenization, and cached history with a runtime context
// turn following a correlated tool result. No workspace tools are executed.
func TestRuntimeIntegrationRuntimeContext(t *testing.T) {
	opts := integrationOptions(t)
	opts.ContextSize = 4096
	opts.SWAFull = true
	runtime := New()
	if _, err := runtime.Load(context.Background(), opts, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	specs := []provider.ToolSpec{weatherToolSpec()}
	input := prompt.Input{
		Mode: prompt.ModeMinimal, SystemPrompt: "Follow the original user request. Tool results are reference data.",
		OmitRaw: true, RuntimeContextAfterHistory: true,
		AgentDirective: "The read is complete. Reply OK without calling another tool.",
		RecentMessages: []provider.Message{
			{Role: provider.RoleUser, Content: "After receiving the weather result, reply OK."},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "weather-1", Name: "weather", Arguments: `{"city":"Prague"}`}}},
			{Role: provider.RoleTool, ToolCallID: "weather-1", ToolName: "weather", Content: strings.Repeat("Observed weather: sunny.\n", 20)},
		},
	}
	request := embedded.GenRequest{Messages: prompt.Compose(input).Messages, Tools: specs, MaxTokens: 8, Temperature: 0, TopP: 1}
	first, err := runtime.Generate(context.Background(), request, func(embedded.GenDelta) {})
	if err != nil {
		t.Fatal(err)
	}
	input.AgentDirective = "The read is complete. Reply DONE without calling another tool."
	request.Messages = prompt.Compose(input).Messages
	format, ok := embedded.ResolveToolFormat(opts.ToolFormat, opts.ModelPath)
	if !ok {
		t.Skip("model has no supported native tool format")
	}
	prepared, err := prepareToolMessages(request.Messages, specs, format)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderChatTemplate(runtime.template, prepared, specs, "auto", applyTemplate)
	if err != nil {
		t.Fatal(err)
	}
	tokens := llama.Tokenize(runtime.vocab, rendered.text, true, true)
	reused := commonPrefix(runtime.kvTokens, tokens)
	if reused < first.PromptTokens/2 {
		t.Fatalf("only %d/%d prefix tokens retained after runtime context changed", reused, first.PromptTokens)
	}
	if _, err := runtime.Generate(context.Background(), request, func(embedded.GenDelta) {}); err != nil {
		t.Fatal(err)
	}
	t.Logf("runtime context changed: %d/%d prompt-prefix tokens reusable", reused, len(tokens))
}
