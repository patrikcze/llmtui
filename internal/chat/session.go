// Package chat holds conversation state and usage statistics.
package chat

import (
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

// RequestStats captures usage for one completed exchange.
type RequestStats struct {
	Usage        provider.Usage
	Duration     time.Duration
	TokensPerSec float64
}

// Session is one conversation plus its accumulated statistics.
type Session struct {
	SystemPrompt string
	Messages     []provider.Message
	Stats        []RequestStats

	TotalPromptTokens     int
	TotalCompletionTokens int
	AnyEstimated          bool

	// rev counts every mutation to Messages, including in-place field edits
	// that don't change its length. A caller that caches transcript
	// rendering keyed on Rev() can detect any change without re-rendering
	// to check; every method that touches Messages must bump it.
	rev int
}

// Rev reports how many times Messages has been mutated. It has no meaning
// on its own beyond "did anything change since I last looked" — callers
// compare it to a previously observed value, they don't interpret it.
func (s *Session) Rev() int { return s.rev }

// NewSession starts a session, seeding the system prompt if provided.
func NewSession(systemPrompt string) *Session {
	s := &Session{SystemPrompt: systemPrompt}
	if systemPrompt != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleSystem, Content: systemPrompt})
	}
	return s
}

// AddUser appends a user message, optionally with image attachments.
func (s *Session) AddUser(content string, images ...provider.Image) {
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: content, Images: images})
	s.rev++
}

// AddAssistant appends an assistant message.
func (s *Session) AddAssistant(content string) {
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: content})
	s.rev++
}

// AddMessage appends a prebuilt message (assistant messages carrying tool
// calls, role:"tool" results).
func (s *Session) AddMessage(msg provider.Message) {
	s.Messages = append(s.Messages, msg)
	s.rev++
}

// SetMessages replaces the conversation wholesale, e.g. loading a saved
// session (/history load, --resume/--continue).
func (s *Session) SetMessages(msgs []provider.Message) {
	s.Messages = msgs
	s.rev++
}

// DropLast removes the most recent message unconditionally. Callers decide
// whether dropping is appropriate (e.g. retry drops an unanswered user
// turn) before calling this.
func (s *Session) DropLast() {
	if n := len(s.Messages); n > 0 {
		s.Messages = s.Messages[:n-1]
		s.rev++
	}
}

// SetLastDisplay attaches display-only text (e.g. a write/edit diff) to the
// most recent message, back-filled after the message carrying the tool
// result was appended.
func (s *Session) SetLastDisplay(display string) {
	if n := len(s.Messages); n > 0 {
		s.Messages[n-1].Display = display
		s.rev++
	}
}

// Touch marks an in-place message annotation as a session mutation. It is
// used for ephemeral runtime references that do not change message count.
func (s *Session) Touch() { s.rev++ }

// RecordUsage folds one request's usage into the session totals and returns
// the derived per-request stats.
func (s *Session) RecordUsage(u provider.Usage, d time.Duration) RequestStats {
	tps := 0.0
	if d > 0 {
		tps = float64(u.CompletionTokens) / d.Seconds()
	}
	st := RequestStats{Usage: u, Duration: d, TokensPerSec: tps}
	s.Stats = append(s.Stats, st)
	s.TotalPromptTokens += u.PromptTokens
	s.TotalCompletionTokens += u.CompletionTokens
	if u.Estimated {
		s.AnyEstimated = true
	}
	return st
}

// TotalTokens returns the session-wide token total.
func (s *Session) TotalTokens() int {
	return s.TotalPromptTokens + s.TotalCompletionTokens
}

// TokenHistory returns total tokens per exchange, oldest first, for charting.
func (s *Session) TokenHistory() []int {
	out := make([]int, len(s.Stats))
	for i, st := range s.Stats {
		out[i] = st.Usage.TotalTokens
	}
	return out
}

// Clear resets the conversation but keeps the system prompt and statistics.
func (s *Session) Clear() {
	s.Messages = s.Messages[:0]
	if s.SystemPrompt != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleSystem, Content: s.SystemPrompt})
	}
	s.rev++
}
