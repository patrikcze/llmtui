package provider

import "strings"

// visionPatterns are lowercase substrings of model IDs known to accept images.
// The list is a heuristic: local model IDs carry no capability metadata, so
// we match common vision model family names. It is the fallback used when a
// backend can't report real per-model capabilities (see ResolveVision) —
// keep it broad, but prefer real data over guessing wherever it's available.
var visionPatterns = []string{
	"vision",
	"llava",
	"bakllava",
	"moondream",
	"minicpm-v",
	"pixtral",
	"gemma3",
	"gemma-3",
	"gemma4",
	"gemma-4",
	"qwen-vl",
	"qwen2-vl",
	"qwen2.5vl",
	"qwen2.5-vl",
	"qwen3-vl",
	"internvl",
	"cogvlm",
	"fuyu",
	"smolvlm",
	"phi-3-vision",
	"phi-3.5-vision",
	"phi-4-multimodal",
	"mistral-small", // Mistral Small 3.1+ is multimodal
	"molmo",
	"deepseek-vl",
	"glm-4v",
	"glm-4.1v",
	"glm-4.5v",
	"ovis",
	"yi-vl",
	"llama-3.2-vision",
	"llama3.2-vision",
	"llama-4-scout",
	"llama4-scout",
	"nvlm",
	"granite-vision",
	"idefics",
	"kimi-vl",
	"gpt-4o",
	"gpt-4.1",
	"gpt-5",
	"claude",
	"demo-model", // built-in mock accepts (and ignores) images
}

// SupportsVision reports whether a model ID looks like a vision-capable
// model. It is a best-effort heuristic; chat.force_vision in the config
// overrides it. Prefer ResolveVision when a ModelInfo is available, since it
// uses real backend-reported capability data when present.
func SupportsVision(modelID string) bool {
	id := strings.ToLower(modelID)
	for _, p := range visionPatterns {
		if strings.Contains(id, p) {
			return true
		}
	}
	return false
}

// ResolveVision reports whether info's model supports image input, using the
// backend-reported Vision field when the backend supplied one and otherwise
// falling back to the model-ID heuristic.
func ResolveVision(info ModelInfo) bool {
	if info.Vision != nil {
		return *info.Vision
	}
	return SupportsVision(info.ID)
}
