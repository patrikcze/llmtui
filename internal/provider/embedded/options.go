// Package embedded implements an in-process llama.cpp-backed provider. This
// file and the package as a whole contain no native code or cgo: the actual
// inference engine is isolated behind the Runtime interface (runtime.go) so
// this stage can build and test everywhere with plain `go build ./...`.
package embedded

import (
	"fmt"
	"strings"

	"github.com/hybridgroup/yzma/pkg/message"
)

// ToolFormat selects the llama.cpp tool-call grammar used for an embedded
// model. Auto delegates format detection to the runtime.
type ToolFormat string

const (
	ToolFormatAuto     ToolFormat = "auto"
	ToolFormatStandard ToolFormat = "standard"
	ToolFormatQwen     ToolFormat = "qwen"
	ToolFormatGLM      ToolFormat = "glm"
	ToolFormatMistral  ToolFormat = "mistral"
	ToolFormatGemma    ToolFormat = "gemma"
	ToolFormatGPT      ToolFormat = "gpt"
	ToolFormatPhi      ToolFormat = "phi"
)

// ParseToolFormat validates a configured embedded tool grammar.
func ParseToolFormat(value string) (ToolFormat, error) {
	format := ToolFormat(strings.ToLower(strings.TrimSpace(value)))
	if format == "" {
		return ToolFormatAuto, nil
	}
	switch format {
	case ToolFormatAuto, ToolFormatStandard, ToolFormatQwen, ToolFormatGLM, ToolFormatMistral, ToolFormatGemma, ToolFormatGPT, ToolFormatPhi:
		return format, nil
	default:
		return "", fmt.Errorf("unsupported embedded tool_format %q (supported: auto, standard, qwen, glm, mistral, gemma, gpt, phi)", value)
	}
}

// ResolveToolFormat returns the configured grammar, or detects it from the
// selected model path when auto is configured. The boolean is false for an
// unknown or unsupported model family.
func ResolveToolFormat(configured ToolFormat, modelPath string) (ToolFormat, bool) {
	if configured != "" && configured != ToolFormatAuto {
		switch configured {
		case ToolFormatStandard, ToolFormatQwen, ToolFormatGLM, ToolFormatMistral, ToolFormatGemma, ToolFormatGPT, ToolFormatPhi:
			return configured, true
		default:
			return ToolFormatAuto, false
		}
	}
	switch message.DetectFormatFromPath(modelPath) {
	case message.FormatStandard:
		return ToolFormatStandard, true
	case message.FormatQwen:
		return ToolFormatQwen, true
	case message.FormatGLM:
		return ToolFormatGLM, true
	case message.FormatMistral:
		return ToolFormatMistral, true
	case message.FormatGemma:
		return ToolFormatGemma, true
	case message.FormatGPT:
		return ToolFormatGPT, true
	case message.FormatPhi:
		return ToolFormatPhi, true
	default:
		return ToolFormatAuto, false
	}
}

// KV cache element types supported for the native K/V cache. F16 is the
// llama.cpp default; Q8_0 halves KV memory with a small quality cost.
const (
	KVCacheTypeF16  = "f16"
	KVCacheTypeQ8_0 = "q8_0"
)

// ParseKVCacheType validates a configured kv_cache_type. Empty selects f16.
func ParseKVCacheType(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "":
		return KVCacheTypeF16, nil
	case KVCacheTypeF16, KVCacheTypeQ8_0:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported embedded kv_cache_type %q (supported: f16, q8_0)", value)
	}
}

// Flash-attention modes. Auto lets llama.cpp decide per model and backend.
const (
	FlashAttentionAuto = "auto"
	FlashAttentionOn   = "on"
	FlashAttentionOff  = "off"
)

// ParseFlashAttention validates a configured flash_attention mode. Empty
// selects auto.
func ParseFlashAttention(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "":
		return FlashAttentionAuto, nil
	case FlashAttentionAuto, FlashAttentionOn, FlashAttentionOff:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported embedded flash_attention %q (supported: auto, on, off)", value)
	}
}

// ValidateKVFlashCombination rejects combinations llama.cpp deterministically
// refuses at context init: a quantized V cache requires flash attention, so
// q8_0 with flash_attention "off" can never load (and with "auto" it fails on
// backends where flash attention resolves to disabled).
func ValidateKVFlashCombination(kvCacheType, flashAttention string) error {
	if kvCacheType == KVCacheTypeQ8_0 && flashAttention == FlashAttentionOff {
		return fmt.Errorf(
			"kv_cache_type %q requires flash attention: set flash_attention: auto or on, or use kv_cache_type: f16",
			kvCacheType,
		)
	}
	return nil
}

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
	// MMProjPath is the resolved absolute path to the vision projector GGUF.
	// Empty configures a text-only model.
	MMProjPath string
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
	// ToolFormat selects the tool-call grammar. The zero value is equivalent
	// to auto for backwards compatibility with existing configurations.
	ToolFormat ToolFormat
	// SWAFull requests full-size KV allocation for sliding-window-attention
	// layers (llama.cpp's C default). llmtui defaults to false: models with
	// interleaved SWA (Gemma) then allocate only window-sized caches for SWA
	// layers — for Gemma 4 E4B this shrinks 131072-token KV from ~7.2 GiB to
	// ~2.0 GiB. The trade-off is that a conversation prefix older than the
	// window cannot be trimmed in place; the runtime already falls back to a
	// full re-decode when the cache refuses a partial removal.
	SWAFull bool
	// KVCacheType selects the K/V cache element type: "" or "f16" (default),
	// or "q8_0" (about half the KV memory, small quality cost).
	KVCacheType string
	// FlashAttention selects the flash-attention mode: "" or "auto"
	// (default, llama.cpp decides), "on", or "off".
	FlashAttention string
	Sampling       Sampling
}
