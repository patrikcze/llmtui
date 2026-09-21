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
		{ID: "demo-model", Name: "Demo Model", Description: "Offline demo model built into llmtui", ContextLen: demoContextTokens},
		{ID: "demo-model-mini", Name: "Demo Model Mini", Description: "Smaller offline demo model", ContextLen: demoMiniContextTokens},
	}, nil
}

// The demo advertises a generous window on purpose: it runs with workspace
// tools on, and the native tool schemas plus system prompt plus the default
// response reserve already exceed 8k. Nothing is allocated; the window is
// only used for request budgeting.
const (
	demoContextTokens     = 32768
	demoMiniContextTokens = 16384
)

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
	// Keep one terminal event slot available when the caller cancels before
	// it starts receiving. Cancellation is observable to the caller rather
	// than racing with TryEmit on an unbuffered channel; normal streaming still
	// applies backpressure after that one event.
	events := make(chan provider.ChatEvent, 1)

	promptTokens := 0
	for _, m := range req.Messages {
		promptTokens += provider.EstimateTokens(m.Content)
	}
	isTaskContract := req.ResponseConstraint != nil && req.ResponseConstraint.Name == "llmtui_task_contract"
	if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "You establish a task contract") {
		isTaskContract = true
	}
	if isTaskContract {
		go func() {
			defer close(events)
			const contract = `{"criteria":["complete the user's requested task"],"needs_user_input":false,"question":"","user_options":[]}`
			provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: contract})
			provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{
				PromptTokens: promptTokens, CompletionTokens: provider.EstimateTokens(contract), TotalTokens: promptTokens + provider.EstimateTokens(contract), Estimated: true,
			}})
		}()
		return events, nil
	}

	go func() {
		defer close(events)
		words := strings.SplitAfter(demoReply, " ")
		if len(req.Tools) > 0 {
			words = append(words, strings.SplitAfter(toolsShowcase(req.Tools), " ")...)
		}
		completion := 0
		for _, w := range words {
			if p.Delay > 0 {
				select {
				case <-ctx.Done():
					provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
					return
				case <-time.After(p.Delay):
				}
			} else if ctx.Err() != nil {
				provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
				return
			}
			completion += provider.EstimateTokens(w)
			if !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: w}) {
				provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
				return
			}
		}
		provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completion,
			TotalTokens:      promptTokens + completion,
			Estimated:        true,
		}})
	}()

	return events, nil
}

// Capabilities describes the offline demo provider.
func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		SupportsStreaming:    true,
		SupportsModelList:    true,
		SupportsTokenUsage:   true,
		SupportsSystemPrompt: true,
		ContextWindowTokens:  demoContextTokens,
	}
}

// toolsShowcase lists the native tools the request offered so the offline
// demo shows what llmtui can do once a real model is connected.
func toolsShowcase(specs []provider.ToolSpec) string {
	var b strings.Builder
	b.WriteString("\n\n**Tools offered to the model this turn** (a real model can call these, with approval):\n\n")
	for _, s := range specs {
		b.WriteString("- `" + s.Name + "`")
		if d := firstLine(s.Description); d != "" {
			b.WriteString(" — " + d)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nThe demo model only replies with canned text; it does not call them.")
	return b.String()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	return s
}
