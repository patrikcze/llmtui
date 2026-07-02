package modelprofile

import "testing"

func TestMatchBuiltIns(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"qwen3:8b", "qwen"},
		{"qwythos-9b-claude-mythos-5-1m", "qwen"},
		{"llama3.1:70b", "llama"},
		{"mistral:7b", "llama"},
		{"qwen2.5-coder:14b", "coder"}, // coder patterns win over qwen
		{"deepseek-r1:14b", "coder"},
		{"gemma3:12b", "gemma"},
	}
	for _, tt := range tests {
		p, ok := Match(BuiltIn(), tt.model)
		if !ok || p.Name != tt.want {
			t.Errorf("Match(%q) = (%s, %v), want %s", tt.model, p.Name, ok, tt.want)
		}
	}
}

func TestMatchFallsBackToDefault(t *testing.T) {
	p, ok := Match(BuiltIn(), "totally-unknown-model")
	if ok {
		t.Error("unknown model should not report a match")
	}
	if p.Name != "default" || p.ContextWindow != 8192 {
		t.Errorf("fallback = %+v, want default profile", p)
	}
}

func TestConfigProfilesTakePriority(t *testing.T) {
	custom := Profile{Name: "mine", Match: []string{"qwen"}, ContextWindow: 999}
	p, ok := Match(append([]Profile{custom}, BuiltIn()...), "qwen3:8b")
	if !ok || p.Name != "mine" {
		t.Errorf("Match = (%s, %v), want custom profile to win", p.Name, ok)
	}
}

func TestByName(t *testing.T) {
	if p, ok := ByName(BuiltIn(), "coder"); !ok || p.PromptStyle != "coding_assistant" {
		t.Errorf("ByName(coder) = (%+v, %v)", p, ok)
	}
	if p, ok := ByName(BuiltIn(), "default"); !ok || p.Name != "default" {
		t.Errorf("ByName(default) = (%+v, %v)", p, ok)
	}
	if _, ok := ByName(BuiltIn(), "nope"); ok {
		t.Error("unknown name should not match")
	}
}
