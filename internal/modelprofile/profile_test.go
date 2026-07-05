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

// TestMatchDoesNotMatchSubstringInsideAWord guards against the unanchored
// strings.Contains bug: "coder" is a literal substring of "encoder" and
// "decoder", so those model IDs must not silently get the coding-assistant
// profile (32768-token window, temp 0.25, coding prompt style) meant for
// actual coding models.
func TestMatchDoesNotMatchSubstringInsideAWord(t *testing.T) {
	for _, model := range []string{"nomic-embed-text-encoder", "whisper-decoder"} {
		p, ok := Match(BuiltIn(), model)
		if ok && p.Name == "coder" {
			t.Errorf("Match(%q) = %s, want default (not coder)", model, p.Name)
		}
	}
}

// TestMatchStillHandlesAttachedVersionNumbers guards against an overcorrection:
// model IDs routinely attach a version number directly with no separator
// ("qwen3", "llama3.1", "gemma3"), and the boundary check must keep matching
// those rather than only accepting a separator/end-of-string.
func TestMatchStillHandlesAttachedVersionNumbers(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"qwen3:8b", "qwen"},
		{"llama3.1:70b", "llama"},
		{"gemma3:12b", "gemma"},
		{"qwen2.5-coder:14b", "coder"},
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
