package prompt

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func allIncludes() Include {
	return Include{SessionSummary: true, LocalMemory: true, ModelHints: true, FormattingHints: true}
}

func TestRawUserMessagePreserved(t *testing.T) {
	raw := "  Fix this  code!! \n```go\nfmt.Println(1)\n```  "
	for _, mode := range []string{ModeMinimal, ModeBalanced, ModeCoding, ModeStrict} {
		out := Compose(Input{
			RawMessage:     raw,
			SystemPrompt:   "be helpful",
			SessionSummary: "we discussed X",
			MemorySnippets: []string{"prefer Go"},
			ModelHints:     []string{"answer directly"},
			Mode:           mode,
			Include:        allIncludes(),
		})
		last := out.Messages[len(out.Messages)-1]
		if last.Role != provider.RoleUser || last.Content != raw {
			t.Errorf("mode %s: raw message altered: %q", mode, last.Content)
		}
		// The preview must expose the raw message as its own section.
		found := false
		for _, s := range out.Sections {
			if s.Title == "Raw User Message" && s.Content == raw {
				found = true
			}
		}
		if !found {
			t.Errorf("mode %s: preview missing raw user message section", mode)
		}
	}
}

func TestBalancedIncludesHelpers(t *testing.T) {
	out := Compose(Input{
		RawMessage:     "hello",
		SystemPrompt:   "sys",
		TemplateName:   "golang",
		TemplatePrompt: "you are a Go expert",
		SessionSummary: "earlier we did X",
		MemorySnippets: []string{"prefer cobra"},
		ModelHints:     []string{"be direct"},
		Mode:           ModeBalanced,
		Include:        allIncludes(),
	})
	system := out.Messages[0]
	if system.Role != provider.RoleSystem {
		t.Fatal("first message should be the system message")
	}
	for _, want := range []string{"sys", "Go expert", "earlier we did X", "prefer cobra", "be direct", "terminal chat application"} {
		if !strings.Contains(system.Content, want) {
			t.Errorf("system message missing %q", want)
		}
	}
	titles := map[string]bool{}
	for _, s := range out.Sections {
		titles[s.Title] = true
	}
	for _, want := range []string{"System Prompt", "Template Prompt (golang)", "Helper Instructions", "Model Helper Hints", "Session Summary", "Relevant Memory", "Raw User Message"} {
		if !titles[want] {
			t.Errorf("sections missing %q (have %v)", want, titles)
		}
	}
}

func TestMinimalAndStrictSkipHelpers(t *testing.T) {
	for _, mode := range []string{ModeMinimal, ModeStrict} {
		out := Compose(Input{
			RawMessage:     "hello",
			SystemPrompt:   "sys",
			SessionSummary: "summary",
			MemorySnippets: []string{"memory"},
			Mode:           mode,
			Include:        allIncludes(),
		})
		system := out.Messages[0].Content
		if strings.Contains(system, "summary") || strings.Contains(system, "memory") {
			t.Errorf("mode %s must not include summary/memory helpers", mode)
		}
	}
}

func TestCodingModeAddsGuidance(t *testing.T) {
	out := Compose(Input{RawMessage: "x", Mode: ModeCoding, Include: allIncludes()})
	if !strings.Contains(out.Messages[0].Content, "runnable code") {
		t.Error("coding mode should add coding guidance")
	}
}

func TestIncludeTogglesRespected(t *testing.T) {
	out := Compose(Input{
		RawMessage:     "hello",
		SessionSummary: "summary text",
		MemorySnippets: []string{"memory text"},
		Mode:           ModeBalanced,
		Include:        Include{}, // everything off
	})
	if len(out.Messages) != 1 {
		// no system content at all → only the user message
		t.Errorf("messages = %d, want 1 (no helpers enabled)", len(out.Messages))
	}
}

func TestRecentMessagesIncludedBetweenSystemAndRaw(t *testing.T) {
	recent := []provider.Message{
		{Role: provider.RoleUser, Content: "earlier question"},
		{Role: provider.RoleAssistant, Content: "earlier answer"},
	}
	out := Compose(Input{
		RawMessage:     "follow-up",
		SystemPrompt:   "sys",
		RecentMessages: recent,
		Mode:           ModeBalanced,
		Include:        allIncludes(),
	})
	if len(out.Messages) != 4 {
		t.Fatalf("messages = %d, want system + 2 recent + raw", len(out.Messages))
	}
	if out.Messages[1].Content != "earlier question" || out.Messages[2].Content != "earlier answer" {
		t.Error("recent messages out of order")
	}
}

func TestImagesRideOnRawMessage(t *testing.T) {
	img := provider.Image{Data: []byte("png"), MIME: "image/png"}
	out := Compose(Input{RawMessage: "what is this?", Images: []provider.Image{img}, Mode: ModeMinimal})
	last := out.Messages[len(out.Messages)-1]
	if len(last.Images) != 1 {
		t.Error("images must attach to the raw user message")
	}
}

func TestValidMode(t *testing.T) {
	for _, m := range []string{ModeMinimal, ModeBalanced, ModeCoding, ModeStrict} {
		if !ValidMode(m) {
			t.Errorf("ValidMode(%q) = false", m)
		}
	}
	if ValidMode("bogus") {
		t.Error("ValidMode(bogus) = true")
	}
}
