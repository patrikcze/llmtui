package openai

import (
	"encoding/json"
	"testing"
)

func TestUsagePayloadCachedTokens(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantKnown  bool
		wantCached int
	}{
		{name: "no details is unknown", body: `{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}`},
		{name: "details without cached_tokens is unknown", body: `{"prompt_tokens":100,"prompt_tokens_details":{}}`},
		{name: "null cached_tokens is unknown", body: `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":null}}`},
		{name: "reported reuse", body: `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":88}}`, wantKnown: true, wantCached: 88},
		{name: "reported zero is known", body: `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":0}}`, wantKnown: true},
		{name: "over-report is clamped", body: `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":250}}`, wantKnown: true, wantCached: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload usagePayload
			if err := json.Unmarshal([]byte(tt.body), &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := payload.toUsage()
			if got.PromptTokens != 100 {
				t.Fatalf("PromptTokens = %d, want 100", got.PromptTokens)
			}
			if got.CachedPromptKnown != tt.wantKnown || got.CachedPromptTokens != tt.wantCached {
				t.Fatalf("cached = (%d, known %v), want (%d, known %v)",
					got.CachedPromptTokens, got.CachedPromptKnown, tt.wantCached, tt.wantKnown)
			}
		})
	}
}
