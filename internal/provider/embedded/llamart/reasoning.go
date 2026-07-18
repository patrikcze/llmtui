package llamart

import (
	"strings"

	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

type reasoningDelimiter struct {
	open  string
	close string
}

var reasoningDelimiters = []reasoningDelimiter{
	{open: "<think>", close: "</think>"},
	{open: "<|channel>thought", close: "<channel|>"},
	{open: "<channel>thought", close: "<channel>"},
}

// reasoningRouter incrementally removes model markup and gives reasoning its
// own stream. It retains only a possible delimiter suffix between pushes, so
// ordinary text continues streaming without waiting for generation to finish.
type reasoningRouter struct {
	buffer    string
	reasoning bool
	close     string
}

func newReasoningRouter(prompt renderedPrompt) *reasoningRouter {
	router := &reasoningRouter{}
	if !prompt.startsReasoning {
		return router
	}
	router.reasoning = true
	trimmed := strings.TrimSpace(prompt.text)
	for _, delimiter := range reasoningDelimiters {
		if strings.HasSuffix(trimmed, delimiter.open) {
			router.close = delimiter.close
			break
		}
	}
	return router
}

func (r *reasoningRouter) Push(text string) []embedded.GenDelta {
	r.buffer += text
	var deltas []embedded.GenDelta
	for r.buffer != "" {
		if r.reasoning {
			if index := strings.Index(r.buffer, r.close); index >= 0 {
				deltas = appendDelta(deltas, embedded.DeltaReasoning, r.buffer[:index])
				r.buffer = strings.TrimPrefix(r.buffer[index+len(r.close):], "\n")
				r.reasoning = false
				r.close = ""
				continue
			}
			safe := len(r.buffer) - retainedDelimiterSuffix(r.buffer, []string{r.close})
			if safe == 0 {
				break
			}
			deltas = appendDelta(deltas, embedded.DeltaReasoning, r.buffer[:safe])
			r.buffer = r.buffer[safe:]
			continue
		}

		delimiter, index := nextReasoningDelimiter(r.buffer)
		if index >= 0 {
			deltas = appendDelta(deltas, embedded.DeltaText, r.buffer[:index])
			r.buffer = strings.TrimPrefix(r.buffer[index+len(delimiter.open):], "\n")
			r.reasoning = true
			r.close = delimiter.close
			continue
		}
		opens := make([]string, 0, len(reasoningDelimiters))
		for _, candidate := range reasoningDelimiters {
			opens = append(opens, candidate.open)
		}
		safe := len(r.buffer) - retainedDelimiterSuffix(r.buffer, opens)
		if safe == 0 {
			break
		}
		deltas = appendDelta(deltas, embedded.DeltaText, r.buffer[:safe])
		r.buffer = r.buffer[safe:]
	}
	return deltas
}

func (r *reasoningRouter) Flush() []embedded.GenDelta {
	if r.buffer == "" {
		return nil
	}
	kind := embedded.DeltaText
	if r.reasoning {
		kind = embedded.DeltaReasoning
	}
	deltas := appendDelta(nil, kind, r.buffer)
	r.buffer = ""
	return deltas
}

func nextReasoningDelimiter(value string) (reasoningDelimiter, int) {
	index := -1
	var result reasoningDelimiter
	for _, delimiter := range reasoningDelimiters {
		candidate := strings.Index(value, delimiter.open)
		if candidate >= 0 && (index < 0 || candidate < index) {
			index = candidate
			result = delimiter
		}
	}
	return result, index
}

func retainedDelimiterSuffix(value string, delimiters []string) int {
	retained := 0
	for _, delimiter := range delimiters {
		for length := 1; length < len(delimiter) && length <= len(value); length++ {
			if strings.HasSuffix(value, delimiter[:length]) && length > retained {
				retained = length
			}
		}
	}
	return retained
}

func appendDelta(deltas []embedded.GenDelta, kind embedded.DeltaKind, text string) []embedded.GenDelta {
	if text == "" {
		return deltas
	}
	if len(deltas) > 0 && deltas[len(deltas)-1].Kind == kind {
		deltas[len(deltas)-1].Text += text
		return deltas
	}
	return append(deltas, embedded.GenDelta{Kind: kind, Text: text})
}
