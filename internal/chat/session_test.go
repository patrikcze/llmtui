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

// TestRevBumpsOnEveryMessageMutation locks in the contract callers that
// cache rendering keyed on Rev() depend on: every method that changes
// Messages — including in-place field edits that don't change its length —
// must bump Rev, and nothing else should.
func TestRevBumpsOnEveryMessageMutation(t *testing.T) {
	s := NewSession("")
	if s.Rev() != 0 {
		t.Fatalf("Rev() = %d, want 0 for a fresh session with no system prompt", s.Rev())
	}

	s.AddUser("hi")
	if s.Rev() != 1 {
		t.Errorf("Rev() after AddUser = %d, want 1", s.Rev())
	}
	s.AddAssistant("hello")
	if s.Rev() != 2 {
		t.Errorf("Rev() after AddAssistant = %d, want 2", s.Rev())
	}
	s.AddMessage(provider.Message{Role: provider.RoleTool, Content: "ok"})
	if s.Rev() != 3 {
		t.Errorf("Rev() after AddMessage = %d, want 3", s.Rev())
	}

	s.SetLastDisplay("diff text")
	if s.Rev() != 4 {
		t.Errorf("Rev() after SetLastDisplay = %d, want 4", s.Rev())
	}
	if got := s.Messages[len(s.Messages)-1].Display; got != "diff text" {
		t.Errorf("SetLastDisplay did not set Display, got %q", got)
	}

	s.DropLast()
	if s.Rev() != 5 {
		t.Errorf("Rev() after DropLast = %d, want 5", s.Rev())
	}
	if n := len(s.Messages); n != 2 {
		t.Errorf("DropLast left %d messages, want 2", n)
	}

	s.SetMessages([]provider.Message{{Role: provider.RoleUser, Content: "restored"}})
	if s.Rev() != 6 {
		t.Errorf("Rev() after SetMessages = %d, want 6", s.Rev())
	}

	s.Clear()
	if s.Rev() != 7 {
		t.Errorf("Rev() after Clear = %d, want 7", s.Rev())
	}

	// Read-only operations must not bump Rev.
	before := s.Rev()
	s.RecordUsage(provider.Usage{TotalTokens: 10}, time.Second)
	_ = s.TotalTokens()
	_ = s.TokenHistory()
	if s.Rev() != before {
		t.Errorf("Rev() changed from a read-only operation: %d -> %d", before, s.Rev())
	}
}

func TestDropLastOnEmptySessionIsANoOp(t *testing.T) {
	s := NewSession("")
	s.DropLast()
	if s.Rev() != 0 || len(s.Messages) != 0 {
		t.Errorf("DropLast on an empty session should be a no-op, got rev=%d messages=%v", s.Rev(), s.Messages)
	}
}

func TestSetLastDisplayOnEmptySessionIsANoOp(t *testing.T) {
	s := NewSession("")
	s.SetLastDisplay("diff")
	if s.Rev() != 0 || len(s.Messages) != 0 {
		t.Errorf("SetLastDisplay on an empty session should be a no-op, got rev=%d messages=%v", s.Rev(), s.Messages)
	}
}
