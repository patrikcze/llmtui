// Package contextmgr keeps conversations inside the model's context window
// by estimating token usage and truncating or summarizing older messages.
package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/patrikcze/llmtui/internal/provider"
)

// localContextToolName is the tool whose results are runtime observations,
// not durable facts. The summarizer keeps a short provenance marker for each
// kind but never copies the volatile payload (or clipboard text) forward.
const localContextToolName = "local_context"

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
	return provider.EstimateMessagesTokens(msgs)
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
	// FixedTokens accounts for provider-visible request data outside the
	// conversation history, such as the composed system/user prompt and native
	// tool schemas.
	FixedTokens int
}

// Decide determines whether and how to compress. Auto picks summarize when
// the conversation is long enough to be worth it, truncate otherwise.
func Decide(msgs []provider.Message, p Params) Decision {
	d := Decision{
		Used:     EstimateTokens(msgs) + p.FixedTokens,
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

// Split divides conversational messages into (older, recent), retaining the
// latest user as the active-turn anchor plus the last keepLast messages.
// System messages are excluded entirely — the prompt composer re-adds the
// system section itself. The window may be widened past keepLast so it never
// severs a tool-call/result pair.
func Split(msgs []provider.Message, keepLast int) (older, recent []provider.Message) {
	conv := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != provider.RoleSystem {
			conv = append(conv, m)
		}
	}
	if keepLast < 0 {
		keepLast = 0
	}
	if len(conv) <= keepLast {
		return []provider.Message{}, slices.Clone(conv)
	}
	start := len(conv) - keepLast
	// The kept window must never open on a tool result: a role:"tool" message
	// whose assistant tool-call message was trimmed away is protocol-invalid
	// (OpenAI-compatible backends reject the request outright). Widen the
	// window backwards until it starts at the assistant message that carries
	// the calls, keeping call/result pairs intact.
	for start > 0 {
		if start == len(conv) {
			if conv[start-1].Role != provider.RoleTool {
				break
			}
			start--
			continue
		}
		if conv[start].Role != provider.RoleTool {
			break
		}
		start--
	}

	latestUser := -1
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == provider.RoleUser {
			latestUser = i
			break
		}
	}
	if latestUser < 0 || latestUser >= start {
		return slices.Clone(conv[:start]), slices.Clone(conv[start:])
	}

	// The active user can sit far before the count window during a long tool
	// run. Move that one message into recent while the completed tool groups
	// between it and start become summarizable older context.
	older = make([]provider.Message, 0, start-1)
	older = append(older, conv[:latestUser]...)
	older = append(older, conv[latestUser+1:start]...)
	recent = make([]provider.Message, 0, len(conv)-start+1)
	recent = append(recent, conv[latestUser])
	recent = append(recent, conv[start:]...)
	return older, recent
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

// importantLine flags a line worth keeping verbatim in the summary. It is
// deliberately inclusive: the budget check in Summarize bounds total size, so
// extra matches only mean more high-value detail is retained until the budget
// is reached. Lines are copied as written — the summarizer never relabels a
// recommendation as a decision or a proposed command as an executed one.
var importantLine = regexp.MustCompile(`(?i)(` +
	`error|fail|failed|panic|exception|traceback|` + // failures
	`fix|decid|chose|agreed|will use|going with|constraint|must not|must |should |only |never |always |require|forbidden|restrict|approv|denied|rejected|` + // decisions & constraints
	`todo|next step|follow[- ]?up|unresolved|blocked|pending|open question|` + // open work
	`exit code|exit status|status \d|passed|passing|\d+ pass|\d+ fail|tests? (pass|fail)|` + // command/test outcomes
	`created|modified|updated|added|removed|deleted|renamed|wrote |` + // file changes
	`file|path|command|config|version|install|flag|port|http|env|` + // technical nouns
	`\.go|\.ya?ml|\.json|\.toml|\.md|\.txt|\.sh|func |package |type |const )`)

// thinkBlock strips a leaked leading reasoning block (a <think>…</think> pair
// or an unclosed <think>) so raw chain-of-thought is never summarized. The
// dedicated Message.Reasoning channel is already never read here.
var thinkBlock = regexp.MustCompile(`(?is)^\s*<think>.*?(</think>|\z)`)

// harmonyChannel strips leading GPT-OSS Harmony analysis/commentary channel
// markup that occasionally leaks into visible content.
var harmonyChannel = regexp.MustCompile(`(?is)^\s*(<\|channel\|>\s*(analysis|commentary).*?(<\|end\|>|<\|message\|>|\z)|<\|start\|>assistant<\|channel\|>final<\|message\|>)`)

func stripLeakedReasoning(s string) string {
	prev := ""
	for prev != s {
		prev = s
		s = thinkBlock.ReplaceAllString(s, "")
		s = harmonyChannel.ReplaceAllString(s, "")
	}
	return strings.TrimSpace(s)
}

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
// look technically important (errors, files, decisions, code). Tool results
// are kept as bounded, clearly-marked untrusted evidence; local_context
// results are reduced to a provenance marker so volatile observations (time,
// process lists, git state, clipboard text) never persist as current facts.
func condenseMessage(m provider.Message) []string {
	out := make([]string, 0, len(m.ToolCalls)+1)
	for _, call := range m.ToolCalls {
		if call.Name == localContextToolName {
			out = append(out, fmt.Sprintf("- assistant tool call local_context: kind=%s (runtime observation)", localContextKind(call.Arguments)))
			continue
		}
		arguments := capLine(strings.TrimSpace(call.Arguments), 200)
		out = append(out, fmt.Sprintf("- assistant tool call %s: %s", call.Name, arguments))
	}

	if m.Role == provider.RoleTool && m.ToolName == localContextToolName {
		return append(out, condenseLocalContextResult(m.Content))
	}

	prefix := "- " + string(m.Role)
	if m.Role == provider.RoleTool {
		name := m.ToolName
		if name == "" {
			name = "result"
		}
		prefix = "- tool " + name + " (untrusted evidence)"
	}
	prefix += ": "
	content := stripLeakedReasoning(m.Content)
	if content == "" {
		return out
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
			picked = append(picked, "  "+capLine(trimmed, 200))
			codeLines++
		case !inCode && importantLine.MatchString(trimmed) && len(picked) < 8:
			picked = append(picked, "  "+firstSentence(trimmed))
		}
	}
	if len(picked) == 0 {
		return out
	}
	out = append(out, prefix+picked[0])
	out = append(out, picked[1:]...)
	return out
}

// localContextKind extracts the "kind" selector from a local_context tool
// call's JSON arguments, tolerating malformed input.
func localContextKind(arguments string) string {
	var args struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err == nil {
		if kind := strings.ToLower(strings.TrimSpace(args.Kind)); kind != "" {
			return kind
		}
	}
	return "unknown"
}

// condenseLocalContextResult reduces one local_context result to a single
// provenance marker. The volatile payload is intentionally dropped: an old
// snapshot of the time, running processes, git state, or recent files must
// never read as the current state, and clipboard text is never persisted in
// a summary at all. Confirmed decisions or file paths derived from these
// observations are preserved through the other messages that acted on them.
func condenseLocalContextResult(content string) string {
	var result struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal([]byte(content), &result)
	kind := strings.ToLower(strings.TrimSpace(result.Kind))

	const base = "- tool local_context (untrusted evidence): "
	switch kind {
	case "clipboard":
		return base + "clipboard was read once with approval; its contents are not retained in this summary."
	case "time":
		return base + "current date/time was observed during the conversation; call local_context kind=time again for the current value, do not reuse an old timestamp."
	case "system", "workspace", "processes", "recent_files":
		return base + kind + " was observed as a point-in-time snapshot; treat it as past context, not current state, and re-read if a current value is needed."
	default:
		return base + "a runtime observation was made; treat it as past context, not current state."
	}
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

// capLine truncates s to at most n runes, marking truncation. Code-fence
// lines are kept verbatim (no sentence-boundary cut like firstSentence),
// so without this cap a single very long line — minified code, a base64
// blob, a one-line JSON dump — could push a summary well past MaxTokens in
// one line, since the budget in Summarize is only checked before appending
// each line, not while building one.
func capLine(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
