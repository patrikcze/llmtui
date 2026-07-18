// Package embedded implements an in-process llama.cpp-backed provider. This
// file and the package as a whole contain no native code or cgo: the actual
// inference engine is isolated behind the Runtime interface (runtime.go) so
// this stage can build and test everywhere with plain `go build ./...`.
package embedded

// Sampling configures the native token sampler chain. Zero values are valid
// Go zero values, not automatically "use the default" — callers that want
// ADR defaults applied must do so explicitly (see internal/app/factory.go).
type Sampling struct {
	TopK          int
	MinP          float64
	RepeatPenalty float64
	RepeatLastN   int
	Seed          uint32 // 0 = random
	Stop          []string
}

// Options configures one embedded Provider instance.
type Options struct {
	// ModelPath is the resolved absolute path to the .gguf model file
	// (leading "~/" already expanded by the caller/factory).
	ModelPath string
	// LibraryPath is the directory containing the llama.cpp dynamic
	// libraries. Empty means "use the YZMA_LIB environment variable".
	LibraryPath string
	// ContextSize is the requested context window in tokens. 0 means the
	// runtime's bounded model default (min(n_ctx_train, 8192)).
	ContextSize int
	// GPULayers is the number of layers to offload to the GPU. -1 offloads
	// all layers (the default); 0 forces CPU-only inference.
	GPULayers int
	// Threads is the CPU thread count. 0 means "auto".
	Threads int
	// BatchSize is the native decode batch size. 0 means "runtime default".
	BatchSize int
	// ChatTemplate overrides the model's GGUF chat-template metadata, for
	// models that ship broken or missing template metadata.
	ChatTemplate string
	Sampling     Sampling
}
