package provider

import "testing"

func TestSupportsVision(t *testing.T) {
	vision := []string{
		"llava:13b",
		"llama3.2-vision:11b",
		"qwen2.5vl:7b",
		"Qwen2-VL-7B-Instruct",
		"minicpm-v:8b",
		"gemma3:12b",
		"gpt-4o",
		"moondream:latest",
		"demo-model",
	}
	for _, id := range vision {
		if !SupportsVision(id) {
			t.Errorf("SupportsVision(%q) = false, want true", id)
		}
	}

	textOnly := []string{
		"qwen3:8b",
		"llama3.1:70b",
		"mistral:7b",
		"deepseek-r1:14b",
		"codellama:13b",
	}
	for _, id := range textOnly {
		if SupportsVision(id) {
			t.Errorf("SupportsVision(%q) = true, want false", id)
		}
	}
}

func TestResolveVisionPrefersBackendData(t *testing.T) {
	yes, no := true, false

	// Backend-reported data wins even when it disagrees with the heuristic.
	if !ResolveVision(ModelInfo{ID: "qwen/qwen3.6-27b", Vision: &yes}) {
		t.Error("ResolveVision with Vision=true should be true even though the ID heuristic misses this model")
	}
	if ResolveVision(ModelInfo{ID: "gpt-4o", Vision: &no}) {
		t.Error("ResolveVision with Vision=false should override a heuristic false-positive")
	}

	// No backend data: falls back to the ID heuristic.
	if !ResolveVision(ModelInfo{ID: "llava:13b"}) {
		t.Error("ResolveVision with nil Vision should fall back to SupportsVision")
	}
	if ResolveVision(ModelInfo{ID: "qwen3:8b"}) {
		t.Error("ResolveVision with nil Vision should fall back to SupportsVision")
	}
}
