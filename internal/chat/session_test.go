package chat

import (
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestSessionSeedsSystemPrompt(t *testing.T) {
	s := NewSession("be helpful")
	if len(s.Messages) != 1 || s.Messages[0].Role != provider.RoleSystem {
		t.Fatalf("messages = %+v, want one system message", s.Messages)
	}

	empty := NewSession("")
	if len(empty.Messages) != 0 {
		t.Errorf("empty system prompt should not add a message")
	}
}

func TestRecordUsageAccumulates(t *testing.T) {
	s := NewSession("")
	st := s.RecordUsage(provider.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}, 2*time.Second)

	if st.TokensPerSec != 10 {
		t.Errorf("TokensPerSec = %v, want 10", st.TokensPerSec)
	}
	s.RecordUsage(provider.Usage{PromptTokens: 5, CompletionTokens: 5, TotalTokens: 10, Estimated: true}, time.Second)

	if s.TotalTokens() != 40 {
		t.Errorf("TotalTokens = %d, want 40", s.TotalTokens())
	}
	if !s.AnyEstimated {
		t.Error("AnyEstimated should be true after an estimated usage")
	}
	if hist := s.TokenHistory(); len(hist) != 2 || hist[0] != 30 || hist[1] != 10 {
		t.Errorf("TokenHistory = %v, want [30 10]", hist)
	}
}

func TestRecordUsageZeroDuration(t *testing.T) {
	s := NewSession("")
	st := s.RecordUsage(provider.Usage{CompletionTokens: 5}, 0)
	if st.TokensPerSec != 0 {
		t.Errorf("TokensPerSec = %v, want 0 for zero duration", st.TokensPerSec)
	}
}

func TestClearKeepsSystemPromptAndStats(t *testing.T) {
	s := NewSession("sys")
	s.AddUser("hi")
	s.AddAssistant("hello")
	s.RecordUsage(provider.Usage{TotalTokens: 10}, time.Second)

	s.Clear()
	if len(s.Messages) != 1 || s.Messages[0].Role != provider.RoleSystem {
		t.Errorf("after Clear messages = %+v, want only system prompt", s.Messages)
	}
	if len(s.Stats) != 1 {
		t.Error("Clear should keep statistics")
	}
}
