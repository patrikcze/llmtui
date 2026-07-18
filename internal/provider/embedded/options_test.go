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
