// Package contextmgr keeps conversations inside the model's context window
// by estimating token usage and truncating or summarizing older messages.
package contextmgr

import (
	"context"
	"regexp"
	"strings"

	"github.com/patrikcze/llmtui/internal/provider"
)

// Strategies for fitting the conversation into the context budget.
const (
	StrategyNone      = "none"
	StrategyTruncate  = "truncate"
	StrategySummarize = "summarize"
	StrategyAuto      = "auto"
)

// ValidStrategy reports whether s is a known strategy.
func ValidStrategy(s string) bool {
	switch s {
	case StrategyNone, StrategyTruncate, StrategySummarize, StrategyAuto:
		return true
	}
	return false
}

// EstimateTokens approximates tokens for a message list, including a small
// per-message overhead for role framing.
func EstimateTokens(msgs []provider.Message) int {
	total := 0
	for _, m := range msgs {
		total += provider.EstimateTokens(m.Content) + 4
	}
	return total
}

// Decision is the outcome of a budget check.
type Decision struct {
	Compress bool
	Strategy string // resolved strategy (auto → truncate or summarize)
	Used     int    // estimated tokens of the full conversation
	Budget   int    // usable tokens (window minus response reserve)
}

// Params configures Decide.
type Params struct {
	Strategy               string
	ContextWindow          int
	ReserveResponseTokens  int
	SummarizeAfterMessages int
}

// Decide determines whether and how to compress. Auto picks summarize when
// the conversation is long enough to be worth it, truncate otherwise.
func Decide(msgs []provider.Message, p Params) Decision {
	d := Decision{
		Used:     EstimateTokens(msgs),
		Budget:   p.ContextWindow - p.ReserveResponseTokens,
		Strategy: p.Strategy,
	}
	if p.Strategy == StrategyNone || d.Budget <= 0 {
		d.Strategy = StrategyNone
		return d
	}
	overBudget := d.Used > d.Budget
	longEnough := p.SummarizeAfterMessages > 0 && countConversational(msgs) >= p.SummarizeAfterMessages

	switch p.Strategy {
	case StrategyTruncate:
		d.Compress = overBudget
	case StrategySummarize:
		d.Compress = overBudget || longEnough
	case StrategyAuto:
		d.Compress = overBudget || longEnough
		if longEnough {
			d.Strategy = StrategySummarize
		} else {
			d.Strategy = StrategyTruncate
		}
	}
	return d
}

func countConversational(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role != provider.RoleSystem {
			n++
		}
	}
	return n
}

// Split divides conversational messages into (older, recent) keeping the
// last keepLast messages intact. System messages are excluded entirely —
// the prompt composer re-adds the system section itself.
func Split(msgs []provider.Message, keepLast int) (older, recent []provider.Message) {
	var conv []provider.Message
	for _, m := range msgs {
		if m.Role != provider.RoleSystem {
			conv = append(conv, m)
		}
	}
	if keepLast < 0 {
		keepLast = 0
	}
	if len(conv) <= keepLast {
		return nil, conv
	}
	return conv[:len(conv)-keepLast], conv[len(conv)-keepLast:]
}

// SummaryInput feeds a Summarizer.
type SummaryInput struct {
	Messages  []provider.Message
	MaxTokens int
}

// SummaryOutput is the produced summary.
type SummaryOutput struct {
	Summary string
}

// Summarizer condenses older conversation. Implementations must preserve
// technical details: commands, file names, decisions, code, settings.
type Summarizer interface {
	Summarize(ctx context.Context, in SummaryInput) (SummaryOutput, error)
}

// HeuristicSummarizer summarizes without any LLM call (the default, so
// context management never triggers extra local inference).
type HeuristicSummarizer struct{}

var importantLine = regexp.MustCompile(`(?i)(error|fail|fix|decid|todo|must|should|file|path|command|config|version|install|flag|port|http|\.go|\.ya?ml|\.json|func |package )`)

// Summarize keeps first sentences plus technically important lines and
// fenced code, within the token budget.
func (HeuristicSummarizer) Summarize(_ context.Context, in SummaryInput) (SummaryOutput, error) {
	var b strings.Builder
	budget := in.MaxTokens
	if budget <= 0 {
		budget = 1200
	}
	for _, m := range in.Messages {
		lines := condenseMessage(m)
		for _, line := range lines {
			if provider.EstimateTokens(b.String()) >= budget {
				return SummaryOutput{Summary: strings.TrimSpace(b.String())}, nil
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return SummaryOutput{Summary: strings.TrimSpace(b.String())}, nil
}

// condenseMessage reduces one message to its lead sentence plus lines that
// look technically important (errors, files, decisions, code).
func condenseMessage(m provider.Message) []string {
	prefix := "- " + string(m.Role) + ": "
	content := strings.TrimSpace(m.Content)
	if content == "" {
		return nil
	}

	var picked []string
	inCode := false
	codeLines := 0
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inCode = !inCode
			continue
		}
		switch {
		case i == 0:
			lead := firstSentence(trimmed)
			picked = append(picked, lead)
			// Keep an important remainder of the lead line (e.g. an error
			// message after the opening question).
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, lead)); rest != "" && importantLine.MatchString(rest) {
				picked = append(picked, "  "+firstSentence(rest))
			}
		case inCode && codeLines < 5:
			picked = append(picked, "  "+trimmed)
			codeLines++
		case !inCode && importantLine.MatchString(trimmed) && len(picked) < 8:
			picked = append(picked, "  "+firstSentence(trimmed))
		}
	}
	if len(picked) == 0 {
		return nil
	}
	out := []string{prefix + picked[0]}
	out = append(out, picked[1:]...)
	return out
}

// firstSentence cuts at a sentence boundary (punctuation + space) so dots
// inside file paths and version numbers never truncate the line.
func firstSentence(s string) string {
	cut := len(s)
	for _, sep := range []string{". ", "! ", "? "} {
		if idx := strings.Index(s, sep); idx >= 0 && idx+1 < cut {
			cut = idx + 1
		}
	}
	s = s[:cut]
	if r := []rune(s); len(r) > 160 {
		s = string(r[:159]) + "…"
	}
	return s
}
