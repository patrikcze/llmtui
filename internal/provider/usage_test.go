package provider

import "testing"

func TestUsageWithCachedPrompt(t *testing.T) {
	tests := []struct {
		name   string
		prompt int
		cached int
		want   int
	}{
		{name: "subset", prompt: 100, cached: 91, want: 91},
		{name: "zero reuse is known", prompt: 100, cached: 0, want: 0},
		{name: "over-report clamps to prompt", prompt: 100, cached: 150, want: 100},
		{name: "negative clamps to zero", prompt: 100, cached: -4, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := Usage{PromptTokens: tt.prompt, CompletionTokens: 7, TotalTokens: tt.prompt + 7}
			got := base.WithCachedPrompt(tt.cached)
			if !got.CachedPromptKnown || got.CachedPromptTokens != tt.want {
				t.Fatalf("WithCachedPrompt(%d) = %+v, want known %d", tt.cached, got, tt.want)
			}
			if got.PromptTokens != tt.prompt || got.TotalTokens != base.TotalTokens {
				t.Fatalf("WithCachedPrompt changed prompt/total accounting: %+v", got)
			}
			if base.CachedPromptKnown {
				t.Fatal("WithCachedPrompt mutated its receiver")
			}
		})
	}
}
