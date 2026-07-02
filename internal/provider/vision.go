package provider

import "strings"

// visionPatterns are lowercase substrings of model IDs known to accept images.
// The list is a heuristic: local model IDs carry no capability metadata, so
// we match common vision model family names.
var visionPatterns = []string{
	"vision",
	"llava",
	"bakllava",
	"moondream",
	"minicpm-v",
	"pixtral",
	"gemma3",
	"gemma-3",
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
	"gpt-4o",
	"gpt-4.1",
	"gpt-5",
	"claude",
	"demo-model", // built-in mock accepts (and ignores) images
}

// SupportsVision reports whether a model ID looks like a vision-capable
// model. It is a best-effort heuristic; chat.force_vision in the config
// overrides it.
func SupportsVision(modelID string) bool {
	id := strings.ToLower(modelID)
	for _, p := range visionPatterns {
		if strings.Contains(id, p) {
			return true
		}
	}
	return false
}
