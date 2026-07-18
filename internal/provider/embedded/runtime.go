package embedded

import (
	"context"
	"errors"

	"github.com/patrikcze/llmtui/internal/provider"
)

// ErrRuntimeUnavailable is returned by a Runtime whose native backend is not
// wired into the current build (see unavailable.go). Provider callers wrap
// it with actionable, provider-name-specific context.
var ErrRuntimeUnavailable = errors.New("embedded inference runtime is not available")

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
}

// GenRequest carries one completion request into the native runtime.
type GenRequest struct {
	Messages    []provider.Message
	Tools       []provider.ToolSpec
	ToolFormat  ToolFormat
	Reasoning   string
	Temperature float64
	TopP        float64
	MaxTokens   int
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
