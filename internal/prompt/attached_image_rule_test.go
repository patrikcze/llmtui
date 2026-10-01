package prompt

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// TestAttachedImageIsNotTreatedAsMissingEvidence is the regression for a live
// Gemma 4 E4B /agent turn: the first request carried the user's raw image,
// but the Entity Context rule "if no visual entity exists, say the old visual
// evidence is unavailable" was the only image instruction, and no entity
// exists until the image is captured after that answer. The model answered
// that no picture was provided. The rule must say first that an image
// attached to the current message is visible directly, and limit the
// unavailable-evidence fallback to an earlier image that is no longer
// attached. internal/tools tests the same order in get_entity_details.
func TestAttachedImageIsNotTreatedAsMissingEvidence(t *testing.T) {
	out := Compose(Input{
		Mode: ModeBalanced, SystemPrompt: "system authority",
		EntityToolsAvailable: true, EntityMaxTokens: 4000,
		RawMessage: "What is on the picture?",
		Images:     []provider.Image{{Data: []byte("png"), MIME: "image/png"}},
	})
	var text strings.Builder
	for _, message := range out.Messages {
		text.WriteString(message.Content)
		text.WriteString(" ")
	}
	rules := strings.Join(strings.Fields(text.String()), " ")
	attached := strings.Index(rules, "attached to the current user message is visible")
	fallback := strings.Index(rules, "visual evidence")
	if attached < 0 || fallback < 0 {
		t.Fatalf("image rules missing (attached=%d, fallback=%d):\n%s", attached, fallback, rules)
	}
	if attached > fallback {
		t.Fatal("the unavailable-evidence fallback comes before the attached-image rule")
	}
	if !strings.Contains(rules[attached:fallback], "earlier message that is no longer attached") {
		t.Fatalf("the fallback is not limited to an earlier, no-longer-attached image:\n%s", rules[attached:])
	}
}
