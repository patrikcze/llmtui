package cache

import (
	"strings"
	"testing"
	"time"
)

func testKey() Key {
	return Key{
		Provider:     "lmstudio",
		BaseURL:      "http://localhost:1234/v1",
		Model:        "test-model",
		UserMessage:  "what is a goroutine?",
		SystemPrompt: "be helpful",
		PromptMode:   "balanced",
		Template:     "",
		Temperature:  0.7,
		TopP:         0.9,
		MaxTokens:    4096,
	}
}

func TestKeyHashStability(t *testing.T) {
	a, b := testKey(), testKey()
	if a.Hash() != b.Hash() {
		t.Error("identical keys must hash identically")
	}

	// Every field participates in the hash.
	variants := []func(*Key){
		func(k *Key) { k.Provider = "ollama" },
		func(k *Key) { k.BaseURL = "http://other:1234" },
		func(k *Key) { k.Model = "other" },
		func(k *Key) { k.UserMessage = "different" },
		func(k *Key) { k.SystemPrompt = "different" },
		func(k *Key) { k.PromptMode = "strict" },
		func(k *Key) { k.Template = "golang" },
		func(k *Key) { k.Temperature = 0.1 },
		func(k *Key) { k.MaxTokens = 100 },
	}
	for i, mutate := range variants {
		k := testKey()
		mutate(&k)
		if k.Hash() == a.Hash() {
			t.Errorf("variant %d did not change the hash", i)
		}
	}
}

func TestKeyNeverContainsSecrets(t *testing.T) {
	// The key must not embed raw base URL or message text (they could hold
	// tokens); both are hashed. The hash itself is hex.
	k := testKey()
	k.BaseURL = "http://user:supersecret@host/v1"
	h := k.Hash()
	if strings.Contains(h, "supersecret") {
		t.Error("hash leaks base URL contents")
	}
	for _, c := range h {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("hash contains non-hex character %q", c)
		}
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	c := New(t.TempDir(), time.Hour, 16, true)
	k := testKey()

	if _, ok := c.Get(k); ok {
		t.Fatal("empty cache should miss")
	}
	if err := c.Put(k, Entry{Response: "a goroutine is…", CompletionTokens: 10}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	e, ok := c.Get(k)
	if !ok || e.Response != "a goroutine is…" {
		t.Fatalf("Get = (%+v, %v), want stored entry", e, ok)
	}

	s := c.Stats()
	if s.Entries != 1 || s.Hits != 1 || s.Misses != 1 {
		t.Errorf("Stats = %+v, want 1 entry, 1 hit, 1 miss", s)
	}
}

func TestTTLExpiration(t *testing.T) {
	c := New(t.TempDir(), 50*time.Millisecond, 16, true)
	k := testKey()
	if err := c.Put(k, Entry{Response: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(k); !ok {
		t.Fatal("fresh entry should hit")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.Get(k); ok {
		t.Error("expired entry should miss")
	}
	if s := c.Stats(); s.Entries != 0 {
		t.Errorf("expired entry should be deleted, stats = %+v", s)
	}
}

func TestDisabledCache(t *testing.T) {
	c := New(t.TempDir(), time.Hour, 16, false)
	k := testKey()
	if err := c.Put(k, Entry{Response: "r"}); err != nil {
		t.Fatalf("Put on disabled cache: %v", err)
	}
	if _, ok := c.Get(k); ok {
		t.Error("disabled cache must never hit")
	}
	if s := c.Stats(); s.Entries != 0 {
		t.Error("disabled cache must not write entries")
	}

	c.SetEnabled(true)
	if !c.Enabled() {
		t.Error("SetEnabled(true) should enable")
	}
}

func TestClear(t *testing.T) {
	c := New(t.TempDir(), time.Hour, 16, true)
	for i := 0; i < 3; i++ {
		k := testKey()
		k.UserMessage = strings.Repeat("x", i+1)
		if err := c.Put(k, Entry{Response: "r"}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := c.Clear()
	if err != nil || removed != 3 {
		t.Errorf("Clear = (%d, %v), want 3 removed", removed, err)
	}
	if s := c.Stats(); s.Entries != 0 {
		t.Error("cache should be empty after Clear")
	}
}
