package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/patrikcze/llmtui/internal/provider"
)

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Reasoning models (served by LM Studio, vLLM, …) stream their
			// thinking separately from the visible answer.
			ReasoningContent string `json:"reasoning_content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usagePayload `json:"usage"`
}

// reasoningFallback formats captured reasoning when the model produced no
// visible answer (typically because max_tokens ran out mid-thinking).
func reasoningFallback(reasoning string) string {
	return "_(the model spent its whole token budget thinking — raise max_tokens for a final answer; reasoning shown below)_\n\n" +
		strings.TrimSpace(reasoning)
}

// streamResponse parses a server-sent-events stream of chat completion chunks.
// Each event is a line of the form "data: {json}" terminated by "data: [DONE]".
func (p *Provider) streamResponse(ctx context.Context, body io.ReadCloser, req provider.ChatRequest, events chan<- provider.ChatEvent) {
	defer close(events)
	defer body.Close()

	var (
		usage      *provider.Usage
		completion strings.Builder
		reasoning  strings.Builder
	)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue // SSE keep-alive or comment
		}
		data, found := strings.CutPrefix(line, "data:")
		if !found {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("decode stream chunk: %w", err)})
			return
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.toUsage()
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.ReasoningContent != "" {
				reasoning.WriteString(choice.Delta.ReasoningContent)
			}
			if choice.Delta.Content == "" {
				continue
			}
			completion.WriteString(choice.Delta.Content)
			if !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: choice.Delta.Content}) {
				provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
				return
			}
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("read stream: %w", err)})
		return
	}

	// A reasoning model that ran out of tokens produced no visible answer;
	// surface the reasoning rather than an empty reply.
	if completion.Len() == 0 && reasoning.Len() > 0 {
		fallback := reasoningFallback(reasoning.String())
		completion.WriteString(fallback)
		if !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: fallback}) {
			provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
			return
		}
	}

	if usage == nil {
		usage = estimateUsage(req, completion.String())
	}
	provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDone, Usage: usage})
}
