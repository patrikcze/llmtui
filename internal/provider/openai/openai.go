// Package openai implements a provider for any OpenAI-compatible server:
// LM Studio, vLLM, llama.cpp, Unsloth, and similar local backends.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

// Provider speaks the OpenAI chat completions API.
type Provider struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

// Option customizes a Provider.
type Option func(*Provider)

// WithHTTPClient overrides the HTTP client (used in tests).
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.client = c }
}

// New creates an OpenAI-compatible provider. name is the configured provider
// name (e.g. "lmstudio"); baseURL should include the /v1 suffix.
func New(name, baseURL, apiKey string, opts ...Option) *Provider {
	p := &Provider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{}, // no global timeout: streams are long-lived
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return req, nil
}

// HealthCheck verifies the server answers the /models endpoint.
func (p *Provider) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := p.newRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s unreachable: %w", p.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("%s returned status %d", p.name, resp.StatusCode)
	}
	return nil
}

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func (p *Provider) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, provider.DefaultTimeout)
	defer cancel()
	req, err := p.newRequest(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list models: status %d", resp.StatusCode)
	}
	var out modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	models := make([]provider.ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		models = append(models, provider.ModelInfo{ID: m.ID, Name: m.ID})
	}
	return models, nil
}

type chatCompletionRequest struct {
	Model       string             `json:"model"`
	Messages    []provider.Message `json:"messages"`
	Temperature float64            `json:"temperature,omitempty"`
	TopP        float64            `json:"top_p,omitempty"`
	MaxTokens   int                `json:"max_tokens,omitempty"`
	Stream      bool               `json:"stream"`
	StreamOpts  *streamOptions     `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// Chat sends a chat completion request, streaming when req.Stream is set.
func (p *Provider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	body := chatCompletionRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Stream:      req.Stream,
	}
	if req.Stream {
		body.StreamOpts = &streamOptions{IncludeUsage: true}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := p.newRequest(ctx, http.MethodPost, "/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("chat request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("chat request: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	events := make(chan provider.ChatEvent)
	if req.Stream {
		go p.streamResponse(ctx, resp.Body, req, events)
	} else {
		go p.wholeResponse(resp.Body, req, events)
	}
	return events, nil
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *usagePayload `json:"usage"`
}

type usagePayload struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (u *usagePayload) toUsage() *provider.Usage {
	if u == nil {
		return nil
	}
	return &provider.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

func (p *Provider) wholeResponse(body io.ReadCloser, req provider.ChatRequest, events chan<- provider.ChatEvent) {
	defer close(events)
	defer body.Close()

	var out chatCompletionResponse
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		events <- provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("decode response: %w", err)}
		return
	}
	if len(out.Choices) == 0 {
		events <- provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("response contained no choices")}
		return
	}
	content := out.Choices[0].Message.Content
	events <- provider.ChatEvent{Type: provider.EventDelta, Delta: content}
	usage := out.Usage.toUsage()
	if usage == nil {
		usage = estimateUsage(req, content)
	}
	events <- provider.ChatEvent{Type: provider.EventDone, Usage: usage}
}

func estimateUsage(req provider.ChatRequest, completion string) *provider.Usage {
	prompt := 0
	for _, m := range req.Messages {
		prompt += provider.EstimateTokens(m.Content)
	}
	c := provider.EstimateTokens(completion)
	return &provider.Usage{
		PromptTokens:     prompt,
		CompletionTokens: c,
		TotalTokens:      prompt + c,
		Estimated:        true,
	}
}
