package embedded

import "testing"

func TestParseToolFormat(t *testing.T) {
	for _, test := range []struct {
		input string
		want  ToolFormat
	}{
		{input: "", want: ToolFormatAuto},
		{input: " AUTO ", want: ToolFormatAuto},
		{input: "standard", want: ToolFormatStandard},
		{input: "qwen", want: ToolFormatQwen},
		{input: "glm", want: ToolFormatGLM},
		{input: "mistral", want: ToolFormatMistral},
		{input: "gemma", want: ToolFormatGemma},
		{input: "gpt", want: ToolFormatGPT},
		{input: "phi", want: ToolFormatPhi},
	} {
		got, err := ParseToolFormat(test.input)
		if err != nil || got != test.want {
			t.Errorf("ParseToolFormat(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	if _, err := ParseToolFormat("unknown"); err == nil {
		t.Error("ParseToolFormat(unknown) error = nil")
	}
}

func TestResolveToolFormat(t *testing.T) {
	for _, test := range []struct {
		path string
		want ToolFormat
		ok   bool
	}{
		{path: "qwen3.gguf", want: ToolFormatQwen, ok: true},
		{path: "gemma-4-e4b.gguf", want: ToolFormatGemma, ok: true},
		{path: "devstral.gguf", want: ToolFormatMistral, ok: true},
		{path: "glm-4.gguf", want: ToolFormatGLM, ok: true},
		{path: "phi-4.gguf", want: ToolFormatPhi, ok: true},
		{path: "renamed.gguf", want: ToolFormatAuto, ok: false},
		{path: "gemma-3.gguf", want: ToolFormatAuto, ok: false},
	} {
		got, ok := ResolveToolFormat(ToolFormatAuto, test.path)
		if got != test.want || ok != test.ok {
			t.Errorf("ResolveToolFormat(auto, %q) = %q, %t; want %q, %t", test.path, got, ok, test.want, test.ok)
		}
	}
	if got, ok := ResolveToolFormat(ToolFormatStandard, "renamed.gguf"); got != ToolFormatStandard || !ok {
		t.Errorf("explicit override = %q, %t", got, ok)
	}
	if got, ok := ResolveToolFormat(ToolFormat("invalid"), "qwen.gguf"); got != ToolFormatAuto || ok {
		t.Errorf("invalid explicit override = %q, %t; want auto, false", got, ok)
	}
}
