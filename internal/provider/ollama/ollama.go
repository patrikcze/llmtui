// Package ollama implements a provider for the native Ollama API.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

// Provider speaks the native Ollama HTTP API (/api/chat, /api/tags).
type Provider struct {
	name    string
	baseURL string
	client  *http.Client
}

// Option customizes a Provider.
type Option func(*Provider)

// WithHTTPClient overrides the HTTP client (used in tests).
func WithHTTPClient(c *http.Client) Option {
	return func(p *Provider) { p.client = c }
}

// WithName sets the configured provider name, so two ollama-typed providers
// (e.g. "ollama" and "ollama-remote") stay distinguishable in the status bar
// and cache attribution.
func WithName(name string) Option {
	return func(p *Provider) {
		if name != "" {
			p.name = name
		}
	}
}

// New creates an Ollama provider. baseURL defaults to http://localhost:11434.
func New(baseURL string, opts ...Option) *Provider {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	p := &Provider{
		name:    "ollama",
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{}, // no global timeout: streams are long-lived
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

func (p *Provider) Name() string { return p.name }

// HealthCheck pings the Ollama root endpoint.
func (p *Provider) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/", nil)
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

type tagsResponse struct {
	Models []struct {
		Name    string `json:"name"`
		Details struct {
			ParameterSize string `json:"parameter_size"`
		} `json:"details"`
	} `json:"models"`
}

func (p *Provider) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, provider.DefaultTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
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
	var out tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	models := make([]provider.ModelInfo, 0, len(out.Models))
	for _, m := range out.Models {
		models = append(models, provider.ModelInfo{
			ID:          m.Name,
			Name:        m.Name,
			Description: m.Details.ParameterSize,
		})
	}
	return models, nil
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []wireMessage `json:"messages"`
	Stream   bool          `json:"stream"`
	Options  chatOptions   `json:"options"`
	Tools    []wireTool    `json:"tools,omitempty"`
}

// wireTool declares one callable function (same shape as the OpenAI format).
type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// wireToolCall is one tool invocation. Ollama transports the arguments as a
// JSON object, not a string, so RawMessage bridges both directions.
type wireToolCall struct {
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// wireMessage is the native Ollama message format; images are base64 strings.
type wireMessage struct {
	Role      string         `json:"role"`
	Content   string         `json:"content"`
	Images    []string       `json:"images,omitempty"`
	ToolCalls []wireToolCall `json:"tool_calls,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
}

func toWireTools(specs []provider.ToolSpec) []wireTool {
	if len(specs) == 0 {
		return nil
	}
	out := make([]wireTool, 0, len(specs))
	for _, s := range specs {
		out = append(out, wireTool{Type: "function", Function: wireFunction{
			Name:        s.Name,
			Description: s.Description,
			Parameters:  s.Parameters,
		}})
	}
	return out
}

func toWireToolCalls(calls []provider.ToolCall) []wireToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]wireToolCall, 0, len(calls))
	for _, c := range calls {
		var wc wireToolCall
		wc.Function.Name = c.Name
		if json.Valid([]byte(c.Arguments)) {
			wc.Function.Arguments = json.RawMessage(c.Arguments)
		} else {
			wc.Function.Arguments = json.RawMessage("{}")
		}
		out = append(out, wc)
	}
	return out
}

func fromWireToolCalls(calls []wireToolCall) []provider.ToolCall {
	out := make([]provider.ToolCall, 0, len(calls))
	for _, c := range calls {
		out = append(out, provider.ToolCall{Name: c.Function.Name, Arguments: string(c.Function.Arguments)})
	}
	return out
}

func toWireMessages(msgs []provider.Message) []wireMessage {
	out := make([]wireMessage, 0, len(msgs))
	for _, m := range msgs {
		wm := wireMessage{
			Role:      string(m.Role),
			Content:   m.Content,
			ToolCalls: toWireToolCalls(m.ToolCalls),
			ToolName:  m.ToolName,
		}
		for _, img := range m.Images {
			wm.Images = append(wm.Images, base64.StdEncoding.EncodeToString(img.Data))
		}
		out = append(out, wm)
	}
	return out
}

type chatOptions struct {
	// Temperature and top_p are sent unconditionally: 0 is a meaningful value
	// (deterministic sampling), so omitempty would silently fall back to the
	// model default. num_predict keeps omitempty — 0 there means "unset".
	Temperature float64 `json:"temperature"`
	TopP        float64 `json:"top_p"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

// chatChunk is one NDJSON line from /api/chat.
type chatChunk struct {
	Message struct {
		Content string `json:"content"`
		// Thinking is streamed separately by reasoning models.
		Thinking  string         `json:"thinking"`
		ToolCalls []wireToolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	Error           string `json:"error"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
}

// Chat sends a chat request. Ollama streams newline-delimited JSON objects.
func (p *Provider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	body := chatRequest{
		Model:    req.Model,
		Messages: toWireMessages(req.Messages),
		Stream:   req.Stream,
		Tools:    toWireTools(req.Tools),
		Options: chatOptions{
			Temperature: req.Temperature,
			TopP:        req.TopP,
			NumPredict:  req.MaxTokens,
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

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
	go p.streamResponse(ctx, resp.Body, req, events)
	return events, nil
}

// streamResponse parses newline-delimited JSON chunks. Non-streaming
// responses are a single JSON object, which the same loop handles.
func (p *Provider) streamResponse(ctx context.Context, body io.ReadCloser, req provider.ChatRequest, events chan<- provider.ChatEvent) {
	defer close(events)
	defer body.Close()

	var (
		usage      *provider.Usage
		completion strings.Builder
		toolCalls  []provider.ToolCall
	)

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk chatChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("decode stream chunk: %w", err)})
			return
		}
		if chunk.Error != "" {
			provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: errors.New(chunk.Error)})
			return
		}
		if chunk.Message.Thinking != "" {
			if !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventReasoning, Delta: chunk.Message.Thinking}) {
				provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
				return
			}
		}
		if len(chunk.Message.ToolCalls) > 0 {
			// Ollama sends each tool call complete (never fragmented), so
			// collecting across chunks is enough; they ride the Done event.
			toolCalls = append(toolCalls, fromWireToolCalls(chunk.Message.ToolCalls)...)
		}
		if chunk.Message.Content != "" {
			completion.WriteString(chunk.Message.Content)
			if !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: chunk.Message.Content}) {
				provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
				return
			}
		}
		if chunk.Done {
			if chunk.PromptEvalCount > 0 || chunk.EvalCount > 0 {
				usage = &provider.Usage{
					PromptTokens:     chunk.PromptEvalCount,
					CompletionTokens: chunk.EvalCount,
					TotalTokens:      chunk.PromptEvalCount + chunk.EvalCount,
				}
			}
			break
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("read stream: %w", err)})
		return
	}

	if usage == nil {
		prompt := 0
		for _, m := range req.Messages {
			prompt += provider.EstimateTokens(m.Content)
			prompt += provider.EstimatedTokensPerImage * len(m.Images)
		}
		c := provider.EstimateTokens(completion.String())
		usage = &provider.Usage{
			PromptTokens:     prompt,
			CompletionTokens: c,
			TotalTokens:      prompt + c,
			Estimated:        true,
		}
	}
	provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventDone, Usage: usage, ToolCalls: toolCalls})
}

// Capabilities describes the native Ollama API.
func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		SupportsStreaming:    true,
		SupportsModelList:    true,
		SupportsTokenUsage:   true, // prompt_eval_count / eval_count
		SupportsJSONMode:     true, // format: json
		SupportsSystemPrompt: true,
	}
}
