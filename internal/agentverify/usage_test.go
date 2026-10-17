package agentverify

import (
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestMergeUsageCachedPrompt(t *testing.T) {
	known := func(prompt, cached int) *provider.Usage {
		u := provider.Usage{PromptTokens: prompt}.WithCachedPrompt(cached)
		return &u
	}
	tests := []struct {
		name          string
		first, second *provider.Usage
		wantKnown     bool
		wantCached    int
	}{
		{name: "both known", first: known(10, 4), second: known(20, 15), wantKnown: true, wantCached: 19},
		{name: "one unknown makes the merge unknown", first: known(10, 4), second: &provider.Usage{PromptTokens: 20}},
		{name: "both unknown", first: &provider.Usage{PromptTokens: 10}, second: &provider.Usage{PromptTokens: 20}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeUsage(tt.first, tt.second)
			if got.PromptTokens != 30 {
				t.Fatalf("PromptTokens = %d, want 30", got.PromptTokens)
			}
			if got.CachedPromptKnown != tt.wantKnown || got.CachedPromptTokens != tt.wantCached {
				t.Fatalf("cached = (%d, known %v), want (%d, known %v)",
					got.CachedPromptTokens, got.CachedPromptKnown, tt.wantCached, tt.wantKnown)
			}
		})
	}
}
