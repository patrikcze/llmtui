// Package mock implements an offline demo provider so the TUI can be
// exercised without any local LLM server running.
package mock

import (
	"context"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

// Provider is a fully offline provider that streams canned Markdown replies.
type Provider struct {
	// Delay between streamed chunks; zero disables pacing (used in tests).
	Delay time.Duration
}

// New returns a mock provider with a natural typing cadence.
func New() *Provider {
	return &Provider{Delay: 18 * time.Millisecond}
}

func (p *Provider) Name() string { return "mock" }

// HealthCheck always succeeds: the mock provider is always available.
func (p *Provider) HealthCheck(ctx context.Context) error { return nil }

func (p *Provider) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{
		{ID: "demo-model", Name: "Demo Model", Description: "Offline demo model built into llmtui", ContextLen: 8192},
		{ID: "demo-model-mini", Name: "Demo Model Mini", Description: "Smaller offline demo model", ContextLen: 4096},
	}, nil
}

const demoReply = "Hello! I'm the **built-in demo model**. No local LLM server was reachable, " +
	"so llmtui switched to offline demo mode.\n\n" +
	"Here is what you can do next:\n\n" +
	"1. Start **Ollama** (`ollama serve`) and run `llmtui chat --provider ollama`\n" +
	"2. Start **LM Studio**'s local server and run `llmtui chat --provider lmstudio`\n" +
	"3. Point at any OpenAI-compatible endpoint with `--base-url`\n\n" +
	"```go\n// llmtui streams real tokens once a provider is online\nfor event := range stream {\n\tfmt.Print(event.Delta)\n}\n```\n\n" +
	"Everything you see here — streaming, Markdown rendering, usage stats — works " +
	"exactly the same against a real backend."

// Chat streams a canned Markdown response word by word.
func (p *Provider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	events := make(chan provider.ChatEvent)

	promptTokens := 0
	for _, m := range req.Messages {
		promptTokens += provider.EstimateTokens(m.Content)
	}

	go func() {
		defer close(events)
		words := strings.SplitAfter(demoReply, " ")
		completion := 0
		for _, w := range words {
			if p.Delay > 0 {
				select {
				case <-ctx.Done():
					events <- provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()}
					return
				case <-time.After(p.Delay):
				}
			} else if ctx.Err() != nil {
				events <- provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()}
				return
			}
			completion += provider.EstimateTokens(w)
			select {
			case events <- provider.ChatEvent{Type: provider.EventDelta, Delta: w}:
			case <-ctx.Done():
				events <- provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()}
				return
			}
		}
		events <- provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completion,
			TotalTokens:      promptTokens + completion,
			Estimated:        true,
		}}
	}()

	return events, nil
}
