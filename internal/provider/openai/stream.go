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
			Reasoning        string           `json:"reasoning"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []streamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *usagePayload `json:"usage"`
}

// streamToolCall is one fragment of a streamed tool call: the id and name
// arrive on the first fragment for an index, the JSON arguments in pieces.
type streamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// toolCallAccumulator reassembles streamed tool-call fragments by index.
type toolCallAccumulator struct {
	order         []int
	calls         map[int]*provider.ToolCall
	argumentBytes int
}

func (a *toolCallAccumulator) add(fragments []streamToolCall) error {
	if a.calls == nil {
		a.calls = make(map[int]*provider.ToolCall)
	}
	for _, f := range fragments {
		tc, ok := a.calls[f.Index]
		if !ok {
			if len(a.calls) >= provider.MaxToolCalls {
				return fmt.Errorf("provider returned too many tool calls (maximum %d)", provider.MaxToolCalls)
			}
			tc = &provider.ToolCall{}
			a.calls[f.Index] = tc
			a.order = append(a.order, f.Index)
		}
		if f.ID != "" {
			tc.ID = f.ID
		}
		if f.Function.Name != "" {
			tc.Name = f.Function.Name
		}
		a.argumentBytes += len(f.Function.Arguments)
		if a.argumentBytes > provider.MaxToolCallArgumentBytes {
			return fmt.Errorf("provider tool-call arguments exceed the %d byte limit", provider.MaxToolCallArgumentBytes)
		}
		tc.Arguments += f.Function.Arguments
	}
	return nil
}

func (a *toolCallAccumulator) result() ([]provider.ToolCall, error) {
	if len(a.order) == 0 {
		return nil, nil
	}
	out := make([]provider.ToolCall, 0, len(a.order))
	for _, idx := range a.order {
		out = append(out, *a.calls[idx])
	}
	if err := provider.ValidateToolCalls(out); err != nil {
		return nil, err
	}
	return out, nil
}

// streamResponse parses a server-sent-events stream of chat completion chunks.
// Each event is a line of the form "data: {json}" terminated by "data: [DONE]".
func (p *Provider) streamResponse(ctx context.Context, body io.ReadCloser, req provider.ChatRequest, events chan<- provider.ChatEvent) {
	defer close(events)
	defer body.Close()

	var (
		usage       *provider.Usage
		completion  strings.Builder
		reasoning   strings.Builder
		toolCalls   toolCallAccumulator
		streamBytes int
		finishLen   bool
		protocol    = provider.ResolveModelProtocol(req.Model, "")
		guard       provider.HarmonyContentGuard
	)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		streamBytes += len(scanner.Bytes())
		if streamBytes > provider.MaxResponseBytes {
			provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: provider.ErrResponseTooLarge})
			return
		}
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
			if choice.FinishReason != nil && *choice.FinishReason == "length" {
				finishLen = true
			}
			// Tool-call fragments carry no visible text; reassemble them and
			// report them with the Done event.
			if err := toolCalls.add(choice.Delta.ToolCalls); err != nil {
				provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: err})
				return
			}
			reasoningDelta := choice.Delta.Reasoning
			if reasoningDelta == "" {
				reasoningDelta = choice.Delta.ReasoningContent
			}
			if reasoningDelta != "" {
				reasoning.WriteString(reasoningDelta)
				// Emit reasoning as activity so consumers know the model is
				// working during a long thinking phase (and don't time out).
				event := provider.ChatEvent{Type: provider.EventReasoning, Delta: reasoningDelta}
				if protocol.HarmonyRequired {
					event.Delta = ""
					event.ReasoningBytes = len(reasoningDelta)
				}
				if !provider.Emit(ctx, events, event) {
					provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
					return
				}
			}
			if choice.Delta.Content == "" {
				continue
			}
			visible := choice.Delta.Content
			if protocol.HarmonyRequired {
				var err error
				visible, err = guard.Feed(visible)
				if err != nil {
					provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: err})
					return
				}
			}
			completion.WriteString(visible)
			if visible != "" && !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: visible}) {
				provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
				return
			}
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("read stream: %w", err)})
		return
	}

	calls, err := toolCalls.result()
	if err != nil {
		provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: err})
		return
	}

	if protocol.HarmonyRequired {
		tail, err := guard.Finish()
		if err != nil || looksLikeUnparsedToolCall(completion.String()+tail) {
			provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: provider.ErrHarmonyProtocol})
			return
		}
		completion.WriteString(tail)
		if tail != "" && !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: tail}) {
			provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
			return
		}
	}

	if usage == nil {
		usage = estimateUsage(req, completion.String())
	}
	malformed := !protocol.HarmonyRequired && len(calls) == 0 && looksLikeUnparsedToolCall(completion.String())
	turn := &provider.AssistantTurn{
		FinalContent: completion.String(), Reasoning: reasoning.String(), ToolCalls: calls,
		Completed: len(calls) == 0 && !finishLen,
	}
	if len(calls) > 0 && reasoning.Len() > 0 {
		turn.Continuation = &provider.ProviderContinuation{Reasoning: reasoning.String()}
	}
	provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDone, Usage: usage, ToolCalls: calls, Truncated: finishLen, Turn: turn, MalformedToolCall: malformed})
}
