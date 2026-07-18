// Package provider defines the common abstraction all LLM backends implement.
package provider

import (
	"context"
	"encoding/json"
	"time"
)

// Role identifies the author of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	// RoleTool carries the result of one tool call back to the model
	// (standard function-calling protocol).
	RoleTool Role = "tool"
)

// ToolCall is one function invocation requested by the model via native
// function calling. Arguments is the raw JSON object text.
type ToolCall struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolSpec declares one callable function to the model. Parameters is a JSON
// Schema object describing the arguments.
type ToolSpec struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Image is a binary image attachment for vision-capable models.
type Image struct {
	Data []byte
	MIME string // e.g. "image/png"
}

// Message is a single chat message exchanged with a model. Images are
// translated to each backend's wire format by the provider implementations.
type Message struct {
	Role    Role    `json:"role"`
	Content string  `json:"content"`
	Images  []Image `json:"-"`

	// ToolCalls is set on assistant messages that request tool execution.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID and ToolName identify which call a RoleTool message answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`

	// Display is a UI-only annotation (e.g. the rendered diff of a
	// write_file result). It is never serialized or sent to a backend.
	Display string `json:"-" yaml:"-"`
}

// ModelInfo describes a model available on a provider.
type ModelInfo struct {
	ID          string
	Name        string
	Description string
	ContextLen  int
	// Vision, when non-nil, is authoritative capability data reported by the
	// backend itself (e.g. LM Studio's native /api/v0/models endpoint, which
	// reports "type": "vlm" for vision-capable models). nil means the
	// backend exposes no such data and callers should fall back to the
	// model-ID heuristic in SupportsVision.
	Vision *bool
}

// ChatRequest carries everything a provider needs to run one completion.
type ChatRequest struct {
	Model       string
	Messages    []Message
	Temperature float64
	TopP        float64
	MaxTokens   int
	Stream      bool
	// Tools, when non-empty, enables native function calling: the specs are
	// sent to the backend and the model may answer with ToolCalls instead of
	// (or in addition to) text.
	Tools []ToolSpec
	// Reasoning, when "on" or "off", explicitly requests or suppresses a
	// reasoning model's thinking phase. Empty means backend default: the
	// provider must omit the corresponding wire field entirely.
	Reasoning string
}

// Usage reports token accounting for a completed request. Estimated is set
// when the backend did not return usage and the numbers were approximated.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Estimated        bool
}

// EventType discriminates ChatEvent variants.
type EventType int

const (
	// EventDelta carries an incremental chunk of assistant output.
	EventDelta EventType = iota
	// EventDone signals the stream finished; Usage may be attached.
	EventDone
	// EventError signals the stream failed; Err is set.
	EventError
	// EventReasoning carries a chunk of a reasoning model's "thinking"
	// (e.g. OpenAI reasoning_content, Ollama thinking). It is progress, not
	// part of the visible answer: consumers should treat it as activity
	// (resetting inactivity timers) and may show it as a thinking indicator.
	EventReasoning
)

// ChatEvent is one item on the streaming channel returned by Chat.
type ChatEvent struct {
	Type  EventType
	Delta string
	Usage *Usage
	Err   error
	// ToolCalls is set on EventDone when the model requested tool execution
	// via native function calling.
	ToolCalls []ToolCall
}

// Provider is implemented by every LLM backend.
//
// Chat returns a channel that emits EventDelta events followed by exactly one
// terminal event (EventDone or EventError), after which the channel is closed.
// Implementations must respect ctx cancellation.
type Provider interface {
	Name() string
	ListModels(ctx context.Context) ([]ModelInfo, error)
	Chat(ctx context.Context, req ChatRequest) (<-chan ChatEvent, error)
	HealthCheck(ctx context.Context) error
}

// Closer is implemented by providers that hold resources needing explicit
// release (e.g. an in-process inference runtime with a loaded model).
// Callers must invoke Close when discarding such a provider — on provider
// switch and on application exit. Close must be idempotent and safe to call
// while a stream is in flight (it cancels the stream first).
type Closer interface {
	Close() error
}

// CloseProvider releases p's resources if it implements Closer. It is safe
// to call with a nil provider.
func CloseProvider(p Provider) error {
	if c, ok := p.(Closer); ok && c != nil {
		return c.Close()
	}
	return nil
}

// RuntimeFingerprinter is implemented by providers whose response-shaping
// state is not fully captured by the request fields shared across backends —
// e.g. an embedded runtime's model file identity and native sampling
// parameters. The fingerprint participates in the response-cache key so two
// materially different runtime configurations can never share a cache entry.
type RuntimeFingerprinter interface {
	RuntimeFingerprint() string
}

// RuntimeFingerprintOf returns p's runtime fingerprint or "" for backends
// whose behavior is fully described by the shared request fields.
func RuntimeFingerprintOf(p Provider) string {
	if f, ok := p.(RuntimeFingerprinter); ok {
		return f.RuntimeFingerprint()
	}
	return ""
}

// Emit delivers ev on events, giving up when ctx is canceled so producers
// never block forever on an abandoned stream. It reports whether ev was sent.
func Emit(ctx context.Context, events chan<- ChatEvent, ev ChatEvent) bool {
	select {
	case events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// TryEmit delivers ev only if a receiver is ready right now. Producers use it
// for best-effort error notification after ctx is already canceled, where a
// blocking send could hang forever.
func TryEmit(events chan<- ChatEvent, ev ChatEvent) bool {
	select {
	case events <- ev:
		return true
	default:
		return false
	}
}

// EstimateTokens approximates token counts when a backend does not report
// usage. It uses the common ~4 characters per token heuristic.
func EstimateTokens(text string) int {
	n := len(text) / 4
	if n == 0 && len(text) > 0 {
		n = 1
	}
	return n
}

// EstimateMessageTokens approximates every provider-visible part of a chat
// message. Structured tool calls and tool-result identifiers are part of the
// prompt just like text, and images have a conservative fixed estimate.
func EstimateMessageTokens(m Message) int {
	total := 4 // role/message framing
	total += EstimateTokens(m.Content)
	for range m.Images {
		total += EstimatedTokensPerImage
	}
	for _, call := range m.ToolCalls {
		total += 4 // function-call framing
		total += EstimateTokens(call.ID)
		total += EstimateTokens(call.Name)
		total += EstimateTokens(call.Arguments)
	}
	total += EstimateTokens(m.ToolCallID)
	total += EstimateTokens(m.ToolName)
	return total
}

// EstimateMessagesTokens approximates the provider-visible cost of messages.
func EstimateMessagesTokens(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += EstimateMessageTokens(message)
	}
	return total
}

// EstimateToolSpecsTokens approximates the request overhead of native tool
// declarations, including their normalized JSON schemas.
func EstimateToolSpecsTokens(specs []ToolSpec) int {
	total := 0
	for _, spec := range specs {
		total += 12 // function/schema framing
		total += EstimateTokens(spec.Name)
		total += EstimateTokens(spec.Description)
		total += EstimateTokens(string(NormalizeToolParameters(spec.Parameters)))
	}
	return total
}

// DefaultTimeout is a sensible per-request ceiling for non-streaming calls.
const DefaultTimeout = 30 * time.Second

// EstimatedTokensPerImage is a rough prompt-token cost per attached image,
// used only when the backend does not report usage (results are marked
// Estimated). Real cost varies with resolution and model.
const EstimatedTokensPerImage = 256
