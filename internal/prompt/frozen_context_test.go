package prompt

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// A continuation that repeats the turn's first runtime-context message must
// not send runtime sections that message already carries unchanged, while a
// changed section is still sent after history.
func TestComposeSkipsSectionsRepeatedInFrozenContext(t *testing.T) {
	fresh := Compose(Input{
		SystemPrompt: "SYSTEM", RawMessage: "raw", RuntimeContextAfterHistory: true,
		AgentDirective: "directive v1", RetrievedContext: "retrieved doc",
	})
	if fresh.RuntimeContext == "" || fresh.Messages[len(fresh.Messages)-2].Content != fresh.RuntimeContext {
		t.Fatalf("fresh request must place its runtime context just before the raw message: %+v", fresh.Messages)
	}
	if strings.Contains(fresh.Messages[0].Content, "directive v1") {
		t.Fatal("runtime section leaked into the system message")
	}

	history := []provider.Message{
		{Role: provider.RoleUser, Content: fresh.RuntimeContext},
		{Role: provider.RoleUser, Content: "raw"},
	}
	continuation := Compose(Input{
		SystemPrompt: "SYSTEM", OmitRaw: true, RuntimeContextAfterHistory: true,
		AgentDirective: "directive v2", RetrievedContext: "retrieved doc",
		RecentMessages: history, FrozenContext: fresh.RuntimeContext,
	})
	tail := continuation.RuntimeContext
	if !strings.Contains(tail, "directive v2") {
		t.Fatalf("changed section missing from the continuation context: %q", tail)
	}
	if strings.Contains(tail, "retrieved doc") {
		t.Fatalf("unchanged section repeated although the frozen context carries it: %q", tail)
	}
}
