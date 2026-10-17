package embedded

import "testing"

func TestGenUsageReportsReuseOnlyWhenKnown(t *testing.T) {
	tests := []struct {
		name       string
		result     GenResult
		wantKnown  bool
		wantCached int
	}{
		{name: "untracked runtime is unknown", result: GenResult{PromptTokens: 100, CompletionTokens: 4}},
		{name: "reused KV prefix", result: GenResult{PromptTokens: 100, CompletionTokens: 4, CachedPromptTokens: 90, CachedPromptKnown: true}, wantKnown: true, wantCached: 90},
		{name: "full decode is known zero", result: GenResult{PromptTokens: 100, CompletionTokens: 4, CachedPromptKnown: true}, wantKnown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := genUsage(tt.result)
			if got.PromptTokens != 100 || got.CompletionTokens != 4 || got.TotalTokens != 104 || got.Estimated {
				t.Fatalf("genUsage accounting = %+v", got)
			}
			if got.CachedPromptKnown != tt.wantKnown || got.CachedPromptTokens != tt.wantCached {
				t.Fatalf("cached = (%d, known %v), want (%d, known %v)",
					got.CachedPromptTokens, got.CachedPromptKnown, tt.wantCached, tt.wantKnown)
			}
		})
	}
}
