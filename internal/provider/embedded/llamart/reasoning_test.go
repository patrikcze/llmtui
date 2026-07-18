package llamart

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func TestReasoningRouterFragmentedThinkTags(t *testing.T) {
	router := newReasoningRouter(renderedPrompt{})
	deltas := pushAndFlush(router, "before<thi", "nk>secret", " words</thi", "nk>after")
	assertRouted(t, deltas, "beforeafter", "secret words")
	assertNoReasoningMarkup(t, deltas)
}

func TestReasoningRouterOrphanCloserFromTemplatePrompt(t *testing.T) {
	router := newReasoningRouter(newRenderedPrompt("assistant<think>"))
	deltas := pushAndFlush(router, "hidden", " chain</thi", "nk>visible")
	assertRouted(t, deltas, "visible", "hidden chain")
	assertNoReasoningMarkup(t, deltas)
}

func TestReasoningRouterGemmaChannelAcrossFragments(t *testing.T) {
	router := newReasoningRouter(renderedPrompt{})
	deltas := pushAndFlush(router, "<|chan", "nel>thoughtprivate", "<chan", "nel|>public")
	assertRouted(t, deltas, "public", "private")
	assertNoReasoningMarkup(t, deltas)
}

func TestReasoningRouterReasoningOnlyOutput(t *testing.T) {
	router := newReasoningRouter(renderedPrompt{})
	deltas := pushAndFlush(router, "<think>only secret")
	assertRouted(t, deltas, "", "only secret")
	assertNoReasoningMarkup(t, deltas)
}

func TestReasoningRouterCancellationDoesNotLeakBufferedMarker(t *testing.T) {
	router := newReasoningRouter(renderedPrompt{})
	if deltas := router.Push("visible<thi"); len(deltas) != 1 || deltas[0].Text != "visible" {
		t.Fatalf("Push before cancellation = %+v, want only safe visible text", deltas)
	}
	// Generation cancellation abandons the router without Flush. The retained
	// partial delimiter therefore never becomes visible answer content.
}

func pushAndFlush(router *reasoningRouter, pieces ...string) []embedded.GenDelta {
	var deltas []embedded.GenDelta
	for _, piece := range pieces {
		deltas = append(deltas, router.Push(piece)...)
	}
	return append(deltas, router.Flush()...)
}

func assertRouted(t *testing.T, deltas []embedded.GenDelta, wantText, wantReasoning string) {
	t.Helper()
	var text, reasoning strings.Builder
	for _, delta := range deltas {
		if delta.Kind == embedded.DeltaReasoning {
			reasoning.WriteString(delta.Text)
		} else {
			text.WriteString(delta.Text)
		}
	}
	if text.String() != wantText || reasoning.String() != wantReasoning {
		t.Errorf("routed text=%q reasoning=%q, want text=%q reasoning=%q", text.String(), reasoning.String(), wantText, wantReasoning)
	}
}

func assertNoReasoningMarkup(t *testing.T, deltas []embedded.GenDelta) {
	t.Helper()
	for _, delta := range deltas {
		for _, marker := range []string{"<think>", "</think>", "<|channel>", "<channel|>"} {
			if strings.Contains(delta.Text, marker) {
				t.Errorf("delta %+v leaked marker %q", delta, marker)
			}
		}
	}
}
