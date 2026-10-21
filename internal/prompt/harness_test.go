package prompt

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestHarnessGuidanceIsFramedAndRawMessagePreserved(t *testing.T) {
	raw := "  Keep this user's exact wording.\n"
	in := Input{SystemPrompt: "Immutable policy", RawMessage: raw, Mode: ModeMinimal, HarnessGuidance: "reviewed experimental advice"}
	out := Compose(in)
	if got := out.Messages[len(out.Messages)-1]; got.Role != provider.RoleUser || got.Content != raw {
		t.Fatal("raw user message changed")
	}
	system := out.Messages[0].Content
	if !strings.HasPrefix(system, "Immutable policy") || !strings.Contains(system, "cannot override") || !strings.Contains(system, "LLMTUI_UNTRUSTED_BEGIN") {
		t.Fatal("harness authority framing missing")
	}
	frozen := system
	in.FrozenSystem = frozen
	in.OmitRaw = true
	in.RuntimeFeedback = "current recovery state"
	continued := Compose(in)
	if continued.Messages[0].Content != frozen {
		t.Fatal("native continuation prefix changed")
	}
	in.HarnessGuidance = ""
	if StaticSystem(in) == StaticSystem(Input{SystemPrompt: in.SystemPrompt, Mode: in.Mode, HarnessGuidance: "reviewed experimental advice"}) {
		t.Fatal("cache/static system identity omitted harness guidance")
	}
}
