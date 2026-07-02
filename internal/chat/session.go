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
}

// NewSession starts a session, seeding the system prompt if provided.
func NewSession(systemPrompt string) *Session {
	s := &Session{SystemPrompt: systemPrompt}
	if systemPrompt != "" {
		s.Messages = append(s.Messages, provider.Message{Role: provider.RoleSystem, Content: systemPrompt})
	}
	return s
}

// AddUser appends a user message.
func (s *Session) AddUser(content string) {
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleUser, Content: content})
}

// AddAssistant appends an assistant message.
func (s *Session) AddAssistant(content string) {
	s.Messages = append(s.Messages, provider.Message{Role: provider.RoleAssistant, Content: content})
}

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
}
