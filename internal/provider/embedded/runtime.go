package embedded

import (
	"context"
	"fmt"

	"github.com/patrikcze/llmtui/internal/provider"
)

// ModelMeta describes a loaded model.
type ModelMeta struct {
	Name         string
	Architecture string
	Quantization string
	NCtxTrain    int
	ContextSize  int
	SizeBytes    int64
	Parameters   uint64
	HasTemplate  bool
	Protocol     provider.ModelProtocol
}

// GenRequest carries one completion request into the native runtime.
type GenRequest struct {
	Messages    []provider.Message
	Tools       []provider.ToolSpec
	ToolFormat  ToolFormat
	Grammar     string
	GrammarRoot string
	Reasoning   string
	// ReasoningEffort is an optional template-level effort value. It remains
	// separate from Reasoning, which selects on/off mode for this request.
	ReasoningEffort ReasoningEffort
	// PreserveReasoning permits compatible templates to receive prior hidden
	// assistant reasoning supplied through Message.Continuation.
	PreserveReasoning bool
	Temperature       float64
	TopP              float64
	MaxTokens         int
	// Progress receives non-content activity such as prompt-processing
	// updates. The provider surfaces it as reasoning so the TUI's inactivity
	// watchdog is reset without mixing status text into the answer.
	Progress func(string)
}

// DeltaKind identifies whether a streamed model fragment is user-visible
// answer text or hidden reasoning.
type DeltaKind uint8

const (
	DeltaText DeltaKind = iota
	DeltaReasoning
)

// GenDelta is a typed streaming fragment from the native runtime.
type GenDelta struct {
	Kind DeltaKind
	Text string
}

// GenResult reports real (non-estimated) token accounting for a completed
// generation.
type GenResult struct {
	PromptTokens     int
	CompletionTokens int
	ToolCalls        []provider.ToolCall
	Turn             *provider.AssistantTurn
	Truncated        bool
}

// MalformedToolCallError is the error a Runtime.Generate implementation
// returns when the model's own output made a tool-call attempt recognizable
// (a call-start marker, or a JSON tool-call wrapper, was seen) but the
// runtime's own grammar/text parser could not turn it into a structured
// call. This is the embedded-runtime equivalent of what
// provider.ChatEvent.MalformedToolCall already names for remote backends
// (openai.looksLikeUnparsedToolCall, the Harmony content guard): the
// backend's own parser failing on a real attempt, not a model that chose not
// to call a tool.
//
// Provider.Chat must translate this into EventDone with MalformedToolCall
// set, never into a hard EventError: the existing one-shot-retry /
// fenced-protocol-fallback recovery already handles this outcome for remote
// backends and applies equally well here, since the fenced protocol does not
// depend on the runtime's native call-grammar parser at all.
type MalformedToolCallError struct {
	Format ToolFormat
}

func (e *MalformedToolCallError) Error() string {
	return fmt.Sprintf("model emitted a recognizable but malformed %s tool call", e.Format)
}

// NativeDiagnostics describes the native backends visible to a loaded runtime.
type NativeDiagnostics struct {
	Loaded        bool
	Registrations uint64
	Devices       uint64
}

type nativeDiagnosticsReporter interface {
	NativeDiagnostics() NativeDiagnostics
}

// Runtime is one loaded native inference engine. It is the seam between the
// embedded Provider and an actual llama.cpp binding; a llama.cpp-backed
// implementation (a later stage) and test mocks both implement it.
//
// Implementations need not be thread-safe: the Provider serializes every
// call to a given Runtime with its own mutex.
type Runtime interface {
	// Probe cheaply validates that the runtime could load: library files
	// present, path shape sane, etc. It must not load any native code or
	// the model itself, and must return quickly (called from
	// Provider.HealthCheck, which has a tight budget).
	Probe(opts Options) error

	// Load initializes the backend and loads the model described by opts.
	// progress receives short human-readable status lines (e.g. "loading
	// model foo.gguf …") so a caller can surface load progress as activity.
	Load(ctx context.Context, opts Options, progress func(string)) (ModelMeta, error)

	// Generate runs one completion, calling emit for each UTF-8-safe answer or
	// reasoning fragment as it becomes available. It returns real token counts
	// and any terminal tool calls. Generate
	// must honor ctx cancellation promptly and must leave the engine
	// reusable for a subsequent call afterwards.
	Generate(ctx context.Context, req GenRequest, emit func(GenDelta)) (GenResult, error)

	// Close releases any resources held by the runtime. It must be safe to
	// call even if Load was never called.
	Close() error
}
