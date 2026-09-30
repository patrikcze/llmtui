package prompt

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// TestEntityPreviewInHistoryRendersHeaderOnly: an entity whose producing
// tool result is already in the conversation keeps its header (id, kind,
// label, digest) but not its preview.
func TestEntityPreviewInHistoryRendersHeaderOnly(t *testing.T) {
	out := formatEntityContext([]EntityRecord{
		{ID: "ent_00001", Kind: "file", Label: "a.txt", Digest: "d1", Preview: "PREVIEW-ONE", PreviewInHistory: true},
		{ID: "ent_00002", Kind: "file", Label: "b.txt", Digest: "d2", Preview: "PREVIEW-TWO"},
	}, 0)
	if !strings.Contains(out, `id="ent_00001"`) || !strings.Contains(out, `digest="d1"`) {
		t.Fatalf("header missing for the in-history entity: %s", out)
	}
	if strings.Contains(out, "PREVIEW-ONE") || !strings.Contains(out, "preview omitted") {
		t.Fatalf("in-history preview repeated: %s", out)
	}
	if !strings.Contains(out, "PREVIEW-TWO") {
		t.Fatalf("preview of an entity not in history was dropped: %s", out)
	}
}

// TestContextMessageDoesNotRepeatFrozenText: with a frozen system message,
// a runtime section already there verbatim is left out, a fixed preamble
// already there is replaced by a pointer, and changed content is kept.
func TestContextMessageDoesNotRepeatFrozenText(t *testing.T) {
	base := Input{
		Mode: ModeBalanced, SystemPrompt: "system authority", AgentDirective: "objective: read a.txt",
		EntityToolsAvailable: true, EntityMaxTokens: 4000,
		Entities:       []EntityRecord{{ID: "ent_00001", Kind: "file", Label: "a.txt", Preview: "alpha"}},
		RecentMessages: []provider.Message{{Role: provider.RoleUser, Content: "go"}},
	}
	fresh := Compose(base).Messages[0].Content

	t.Run("unchanged runtime sections are left out", func(t *testing.T) {
		in := base
		in.OmitRaw, in.FrozenSystem = true, fresh
		out := Compose(in)
		if last := out.Messages[len(out.Messages)-1]; last.Role == provider.RoleUser && strings.HasPrefix(last.Content, "Runtime context") {
			t.Fatalf("unchanged runtime sections were repeated: %s", last.Content)
		}
	})
	t.Run("changed sections keep their data but not their preamble", func(t *testing.T) {
		in := base
		in.OmitRaw, in.FrozenSystem = true, fresh
		in.AgentDirective = "objective: report a.txt"
		in.Entities = append(in.Entities, EntityRecord{ID: "ent_00002", Kind: "file", Label: "b.txt", Preview: "beta"})
		out := Compose(in)
		ctx := out.Messages[len(out.Messages)-1].Content
		for _, want := range []string{"objective: report a.txt", "beta", preambleInSystem} {
			if !strings.Contains(ctx, want) {
				t.Errorf("context message missing %q", want)
			}
		}
		for _, preamble := range []string{agentCyclePreamble, entityContextPreamble} {
			if strings.Contains(ctx, preamble) {
				t.Errorf("preamble already in the frozen system message was repeated")
			}
		}
	})
	t.Run("without a frozen system message nothing is trimmed", func(t *testing.T) {
		in := base
		in.OmitRaw, in.RuntimeContextAfterHistory = true, true
		ctx := Compose(in).Messages
		last := ctx[len(ctx)-1].Content
		if !strings.Contains(last, agentCyclePreamble) || !strings.Contains(last, entityContextPreamble) {
			t.Fatal("the unfrozen deferred layout must carry the full sections")
		}
	})
}
