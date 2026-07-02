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
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usagePayload `json:"usage"`
}

// streamResponse parses a server-sent-events stream of chat completion chunks.
// Each event is a line of the form "data: {json}" terminated by "data: [DONE]".
func (p *Provider) streamResponse(ctx context.Context, body io.ReadCloser, req provider.ChatRequest, events chan<- provider.ChatEvent) {
	defer close(events)
	defer body.Close()

	var (
		usage      *provider.Usage
		completion strings.Builder
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
			events <- provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("decode stream chunk: %w", err)}
			return
		}
		if chunk.Usage != nil {
			usage = chunk.Usage.toUsage()
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content == "" {
				continue
			}
			completion.WriteString(choice.Delta.Content)
			select {
			case events <- provider.ChatEvent{Type: provider.EventDelta, Delta: choice.Delta.Content}:
			case <-ctx.Done():
				events <- provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()}
				return
			}
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		events <- provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("read stream: %w", err)}
		return
	}

	if usage == nil {
		usage = estimateUsage(req, completion.String())
	}
	events <- provider.ChatEvent{Type: provider.EventDone, Usage: usage}
}
