package cache

import "testing"

// TestRuntimeIDVariesKey guards the cache-key completeness invariant: two
// requests identical in every shared field but backed by different runtime
// state (model file, native sampling) must not share a cache entry.
func TestRuntimeIDVariesKey(t *testing.T) {
	base := Key{Provider: "embedded", Model: "qwen3.gguf", UserMessage: "hi"}
	other := base
	other.RuntimeID = "path|size|mtime|sampling-v1"
	if base.Hash() == other.Hash() {
		t.Fatal("RuntimeID does not vary the cache key")
	}
	same := base
	if base.Hash() != same.Hash() {
		t.Fatal("identical keys must hash identically")
	}
}
