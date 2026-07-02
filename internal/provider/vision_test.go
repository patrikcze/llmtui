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
