// Package embedded implements an in-process llama.cpp-backed provider. This
// file and the package as a whole contain no native code or cgo: the actual
// inference engine is isolated behind the Runtime interface (runtime.go) so
// this stage can build and test everywhere with plain `go build ./...`.
package embedded

import (
	"fmt"
	"math"
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

// KV cache element types supported by the pinned llama.cpp/Yzma runtime.
const (
	KVCacheTypeF16  = "f16"
	KVCacheTypeQ8_0 = "q8_0"
	KVCacheTypeQ4_0 = "q4_0"
)

// ParseKVCacheType validates a configured KV cache type. Empty selects f16.
func ParseKVCacheType(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "":
		return KVCacheTypeF16, nil
	case KVCacheTypeF16, KVCacheTypeQ8_0, KVCacheTypeQ4_0:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported embedded kv cache type %q (supported: f16, q8_0, q4_0)", value)
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
func ValidateKVFlashCombination(typeV, flashAttention string) error {
	if (typeV == KVCacheTypeQ8_0 || typeV == KVCacheTypeQ4_0) && flashAttention == FlashAttentionOff {
		return fmt.Errorf(
			"KV cache V type %q requires flash attention: set flash_attention: auto or on, or set kv_cache.type_v: f16",
			typeV,
		)
	}
	return nil
}

// ReasoningEffort is an optional model-template reasoning level. Auto leaves
// the template's own default untouched.
type ReasoningEffort string

const (
	ReasoningEffortAuto   ReasoningEffort = "auto"
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
	ReasoningEffortXHigh  ReasoningEffort = "xhigh"
)

func ParseReasoningEffort(value string) (ReasoningEffort, error) {
	normalized := ReasoningEffort(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "", ReasoningEffortAuto:
		return ReasoningEffortAuto, nil
	case ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh, ReasoningEffortXHigh:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported embedded reasoning.effort %q (supported: auto, low, medium, high, xhigh)", value)
	}
}

// SpeculativeType identifies an optional embedded decoding path.
type SpeculativeType string

const (
	SpeculativeOff      SpeculativeType = "off"
	SpeculativeDraftMTP SpeculativeType = "draft-mtp"
)

func ParseSpeculativeType(value string) (SpeculativeType, error) {
	normalized := SpeculativeType(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case "", SpeculativeOff:
		return SpeculativeOff, nil
	case SpeculativeDraftMTP:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported embedded speculative.type %q (supported: off, draft-mtp)", value)
	}
}

// KVCache configures target or draft K/V types. Offload nil preserves the
// native default instead of accidentally changing it through a Go zero value.
type KVCache struct {
	TypeK   string
	TypeV   string
	Offload *bool
}

// Speculative configures the optional speculative decoder.
type Speculative struct {
	Type      SpeculativeType
	DraftNMax int
	KVCache   KVCache
}

// Rope scaling modes supported by the linked llama.cpp runtime. Unspecified
// preserves the scaling mode stored in model metadata.
type RopeScalingType string

const (
	RopeScalingUnspecified RopeScalingType = ""
	RopeScalingNone        RopeScalingType = "none"
	RopeScalingLinear      RopeScalingType = "linear"
	RopeScalingYARN        RopeScalingType = "yarn"
	RopeScalingLongRope    RopeScalingType = "longrope"
)

// ParseRopeScalingType validates a configured RoPE scaling override.
func ParseRopeScalingType(value string) (RopeScalingType, error) {
	normalized := RopeScalingType(strings.ToLower(strings.TrimSpace(value)))
	switch normalized {
	case RopeScalingUnspecified, RopeScalingNone, RopeScalingLinear, RopeScalingYARN, RopeScalingLongRope:
		return normalized, nil
	default:
		return "", fmt.Errorf("unsupported embedded rope_scaling_type %q (supported: none, linear, yarn, longrope)", value)
	}
}

// RopeScaling contains optional context-level RoPE/YaRN overrides. Pointer
// values preserve llama.cpp's model-metadata defaults when a field is omitted.
type RopeScaling struct {
	Type       RopeScalingType
	FreqBase   *float64
	FreqScale  *float64
	ExtFactor  *float64
	AttnFactor *float64
	BetaFast   *float64
	BetaSlow   *float64
	OrigCtx    *int
}

// AllowsContextExtension reports whether the explicit scaling mode is meant
// to extrapolate beyond the model's trained context window.
func (r RopeScaling) AllowsContextExtension() bool {
	return r.Type == RopeScalingLinear || r.Type == RopeScalingYARN || r.Type == RopeScalingLongRope
}

// Validate rejects values that cannot be represented safely by llama.cpp.
func (r RopeScaling) Validate() error {
	switch r.Type {
	case RopeScalingUnspecified, RopeScalingNone, RopeScalingLinear, RopeScalingYARN, RopeScalingLongRope:
	default:
		return fmt.Errorf("unsupported embedded rope_scaling_type %q", r.Type)
	}
	for name, value := range map[string]*float64{
		"rope_freq_base": r.FreqBase, "rope_freq_scale": r.FreqScale,
		"yarn_ext_factor": r.ExtFactor, "yarn_attn_factor": r.AttnFactor,
		"yarn_beta_fast": r.BetaFast, "yarn_beta_slow": r.BetaSlow,
	} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || math.Abs(*value) > math.MaxFloat32) {
			return fmt.Errorf("%s must be finite and fit in float32", name)
		}
	}
	if r.FreqBase != nil && *r.FreqBase <= 0 {
		return fmt.Errorf("rope_freq_base must be greater than zero: %g", *r.FreqBase)
	}
	if r.FreqScale != nil && *r.FreqScale <= 0 {
		return fmt.Errorf("rope_freq_scale must be greater than zero: %g", *r.FreqScale)
	}
	if r.OrigCtx != nil && (*r.OrigCtx <= 0 || uint64(*r.OrigCtx) > math.MaxUint32) {
		return fmt.Errorf("yarn_orig_ctx %d is outside the supported range 1..%d", *r.OrigCtx, uint64(math.MaxUint32))
	}
	return nil
}

// Sampling configures the native token sampler chain. Zero values are valid
// Go zero values, not automatically "use the default" — callers that want
// ADR defaults applied must do so explicitly (see internal/app/factory.go).
type Sampling struct {
	TopK                int
	MinP                float64
	RepeatPenalty       float64
	RepeatLastN         int
	PresencePenalty     float64
	DRYMultiplier       float64
	DRYBase             float64
	DRYAllowedLength    int
	DRYPenaltyLastN     int
	DRYSequenceBreakers []string
	Seed                uint32 // 0 = random
	Stop                []string
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
	// ThreadsBatch is the prompt/batch thread count. 0 preserves Threads.
	ThreadsBatch int
	// BatchSize is the native decode batch size. 0 means "runtime default".
	BatchSize int
	// UBatchSize is the native physical micro-batch size. 0 preserves the
	// llama.cpp default used by older llmtui configurations.
	UBatchSize int
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
	// "q8_0", or "q4_0". Structured KVCache overrides each side separately.
	KVCacheType string
	// KVCache supplies separate K/V cache types and an optional K/Q/V offload
	// override. TypeK and TypeV are already normalized by the factory.
	KVCache KVCache
	// FlashAttention selects the flash-attention mode: "" or "auto"
	// (default, llama.cpp decides), "on", or "off".
	FlashAttention string
	Reasoning      struct {
		Effort   ReasoningEffort
		Preserve bool
	}
	Speculative Speculative
	RopeScaling RopeScaling
	Sampling    Sampling
}
