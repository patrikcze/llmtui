package provider

import "testing"

func TestResolveModelProtocolGPTOSS(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, model, architecture string
		want                      bool
	}{
		{name: "openai id", model: "openai/gpt-oss-20b", want: true},
		{name: "ollama id", model: "gpt-oss:20b", want: true},
		{name: "gguf path", model: "/models/gpt-oss-20b-MXFP4.gguf", want: true},
		{name: "architecture wins", model: "custom.gguf", architecture: "gpt-oss", want: true},
		{name: "not substring", model: "mygpt-ossified-model", want: false},
		{name: "ordinary model", model: "qwen3:8b", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveModelProtocol(test.model, test.architecture)
			if (got.Family == ModelFamilyGPTOSS) != test.want {
				t.Fatalf("ResolveModelProtocol(%q, %q) = %+v", test.model, test.architecture, got)
			}
		})
	}
}

func TestGPTOSSReasoningEffort(t *testing.T) {
	t.Parallel()
	protocol := ResolveModelProtocol("gpt-oss:20b", "")
	for _, test := range []struct {
		configured, want string
		ok               bool
	}{
		{"", "medium", true}, {"auto", "medium", true}, {"on", "medium", true},
		{"low", "low", true}, {"medium", "medium", true}, {"high", "high", true},
		{"off", "", false}, {"max", "", false},
	} {
		got, ok := ReasoningEffort(protocol, test.configured)
		if got != test.want || ok != test.ok {
			t.Errorf("ReasoningEffort(%q) = %q, %v; want %q, %v", test.configured, got, ok, test.want, test.ok)
		}
	}
}

func TestContainsHarmonyControlToken(t *testing.T) {
	t.Parallel()
	if !ContainsHarmonyControlToken("answer<|channel|>analysis") {
		t.Fatal("expected Harmony control token to be rejected")
	}
	if ContainsHarmonyControlToken("ordinary <angle> text") {
		t.Fatal("ordinary text was rejected")
	}
}
