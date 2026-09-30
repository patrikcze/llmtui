package prompt

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// TestFrozenSystemIsVerbatimAndRuntimeFollowsHistory covers plan step 2's
// composer contract: with FrozenSystem set, the system message is that text
// byte for byte, every runtime section lands in the context message after
// history, and no static section is repeated there.
func TestFrozenSystemIsVerbatimAndRuntimeFollowsHistory(t *testing.T) {
	history := []provider.Message{
		{Role: provider.RoleUser, Content: "original task"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"a.txt"}`}}},
		{Role: provider.RoleTool, ToolCallID: "c1", Content: "page 1"},
	}
	full := Input{
		Mode: ModeBalanced, SystemPrompt: "system authority", TemplatePrompt: "template rules",
		AgentDirective: "read page 2", SessionSummary: "earlier summary",
		Include:          Include{SessionSummary: true, LocalMemory: true},
		UseActiveContext: true, ActiveContext: []MemoryRecord{{ID: "m1", Kind: "note", Text: "active memo"}},
		EntityToolsAvailable: true, EntityMaxTokens: 1200,
		Entities:        []EntityRecord{{ID: "ent_00001", Preview: "entity preview"}},
		RuntimeFeedback: "Use the documented read_file schema.",
		RecentMessages:  history, OmitRaw: true,
	}
	runtimeWants := []string{"read page 2", "earlier summary", "active memo", "entity preview", "documented read_file schema"}
	staticWants := []string{"system authority", "template rules"}

	for _, tc := range []struct {
		name   string
		frozen string
	}{
		{"frozen text unrelated to the input", "FROZEN SYSTEM, byte for byte\n\twith odd  spacing"},
		{"frozen text containing stale runtime", "system authority\n\nread page 1 (stale)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := full
			in.FrozenSystem = tc.frozen
			out := Compose(in)
			if out.Messages[0].Role != provider.RoleSystem || out.Messages[0].Content != tc.frozen {
				t.Fatalf("system = %q, want the frozen text verbatim", out.Messages[0].Content)
			}
			for i, want := range history {
				if got := out.Messages[i+1]; got.Role != want.Role || got.Content != want.Content || got.ToolCallID != want.ToolCallID {
					t.Fatalf("history message %d changed: %+v", i, got)
				}
			}
			if len(out.Messages) != len(history)+2 {
				t.Fatalf("messages = %d, want system + history + one context message (no raw user)", len(out.Messages))
			}
			ctx := out.Messages[len(out.Messages)-1]
			if ctx.Role != provider.RoleUser || !strings.HasPrefix(ctx.Content, "Runtime context supplied by llmtui, not a new user request.") {
				t.Fatalf("last message = %+v, want the labeled runtime context", ctx)
			}
			for _, want := range runtimeWants {
				if !strings.Contains(ctx.Content, want) {
					t.Errorf("runtime context missing %q", want)
				}
			}
			for _, static := range staticWants {
				if strings.Contains(ctx.Content, static) {
					t.Errorf("static section %q repeated in the runtime context", static)
				}
			}
		})
	}
}

// TestStaticSystemExcludesRuntimeSections pins what a frozen system message
// is compared on: static sections only, so runtime changes keep it reusable
// and a static change does not.
func TestStaticSystemExcludesRuntimeSections(t *testing.T) {
	in := Input{
		Mode: ModeBalanced, SystemPrompt: "system authority", TemplatePrompt: "template rules",
		AgentDirective: "cycle 1", EntityToolsAvailable: true, EntityMaxTokens: 1200,
		Skills: []SkillPrompt{{ID: "s1", Body: "skill body"}},
	}
	static := StaticSystem(in)
	for _, want := range []string{"system authority", "template rules", "skill body"} {
		if !strings.Contains(static, want) {
			t.Fatalf("static system missing %q: %s", want, static)
		}
	}
	if strings.Contains(static, "cycle 1") || strings.Contains(static, "entity_context") {
		t.Fatalf("static system contains runtime sections: %s", static)
	}
	runtimeChanged := in
	runtimeChanged.AgentDirective = "cycle 2"
	runtimeChanged.Entities = []EntityRecord{{ID: "ent_00002", Preview: "new"}}
	if StaticSystem(runtimeChanged) != static {
		t.Fatal("a runtime-only change altered the static system")
	}
	skillLoaded := in
	skillLoaded.Skills = append(skillLoaded.Skills, SkillPrompt{ID: "s2", Body: "loaded mid-turn"})
	if StaticSystem(skillLoaded) == static {
		t.Fatal("loading a skill must change the static system, so a frozen one is not reused")
	}
}
