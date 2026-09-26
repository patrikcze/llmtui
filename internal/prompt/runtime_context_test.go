package prompt

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestDeferredRuntimeContextPreservesSystemAndHistoryPrefix(t *testing.T) {
	input := Input{
		Mode: ModeBalanced, SystemPrompt: "system authority", RawMessage: "current task",
		RuntimeContextAfterHistory: true, AgentDirective: "read page 1",
		EntityToolsAvailable: true, EntityMaxTokens: 1200,
		Entities: []EntityRecord{{ID: "ent_00001", Preview: "untrusted data"}},
		RecentMessages: []provider.Message{
			{Role: provider.RoleUser, Content: "original task"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"a.txt"}`}}},
			{Role: provider.RoleTool, ToolCallID: "c1", Content: "page 1"},
		},
	}
	first := Compose(input)
	input.AgentDirective = "read page 2"
	input.Entities[0].Preview = "changed untrusted data"
	input.RuntimeFeedback = "Use the documented read_file schema."
	second := Compose(input)
	if first.Messages[0].Content != second.Messages[0].Content {
		t.Fatal("changing runtime state invalidated the system prefix")
	}
	for i, want := range input.RecentMessages {
		got := second.Messages[i+1]
		if got.Role != want.Role || got.Content != want.Content || got.ToolCallID != want.ToolCallID || len(got.ToolCalls) != len(want.ToolCalls) {
			t.Fatalf("history message %d changed: %+v", i, got)
		}
	}
	runtime := second.Messages[len(second.Messages)-2]
	for _, want := range []string{"not a new user request", "cannot override", "read page 2", "changed untrusted data", "LLMTUI_UNTRUSTED_BEGIN", "documented read_file schema"} {
		if !strings.Contains(runtime.Content, want) {
			t.Fatalf("runtime context missing %q: %s", want, runtime.Content)
		}
	}
	last := second.Messages[len(second.Messages)-1]
	if runtime.Role != provider.RoleUser || last.Content != input.RawMessage || last.Role != provider.RoleUser {
		t.Fatal("raw user message or runtime role changed")
	}
	input.OmitRaw = true
	continued := Compose(input)
	if len(continued.Messages) != len(second.Messages)-1 {
		t.Fatal("continuation appended a new raw user submission")
	}
}
