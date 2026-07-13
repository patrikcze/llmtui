package cache

import (
	"os"
	"path/filepath"
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
		func(k *Key) { k.HistoryHash = "different" },
		func(k *Key) { k.ToolsHash = "different" },
		func(k *Key) { k.Reasoning = "off" },
	}
	for i, mutate := range variants {
		k := testKey()
		mutate(&k)
		if k.Hash() == a.Hash() {
			t.Errorf("variant %d did not change the hash", i)
		}
	}
}

func TestKeyHashVariesWithReasoning(t *testing.T) {
	a := Key{Provider: "p", Model: "m", UserMessage: "hi"}
	b := a
	b.Reasoning = "off"
	if a.Hash() == b.Hash() {
		t.Fatal("Reasoning must vary the cache key")
	}
}

// TestHistoryHashPreventsCrossConversationCollision guards against two
// different conversations that happen to send the same short next message
// (e.g. "yes") under identical settings colliding on the same cache entry
// and getting served each other's out-of-context answer.
func TestHistoryHashPreventsCrossConversationCollision(t *testing.T) {
	c := New(t.TempDir(), time.Hour, 16, true)

	convoA := testKey()
	convoA.UserMessage = "yes"
	convoA.HistoryHash = "conversation-a-prefix"

	convoB := testKey()
	convoB.UserMessage = "yes"
	convoB.HistoryHash = "conversation-b-prefix"

	if err := c.Put(convoA, Entry{Response: "answer for conversation A"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, ok, err := c.Get(convoB); err != nil || ok {
		t.Fatal("a different conversation history must not hit conversation A's cache entry")
	}
	entry, ok, err := c.Get(convoA)
	if err != nil {
		t.Fatalf("get conversation A: %v", err)
	}
	if !ok || entry.Response != "answer for conversation A" {
		t.Fatalf("conversation A's own entry should still be retrievable, got %+v, ok=%v", entry, ok)
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

	if _, ok, err := c.Get(k); err != nil || ok {
		t.Fatal("empty cache should miss")
	}
	if err := c.Put(k, Entry{Response: "a goroutine is…", CompletionTokens: 10}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	e, ok, err := c.Get(k)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
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
	if _, ok, err := c.Get(k); err != nil || !ok {
		t.Fatal("fresh entry should hit")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok, err := c.Get(k); err != nil || ok {
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
	if _, ok, err := c.Get(k); err != nil || ok {
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

func TestPutUsesAtomicOwnerOnlyFile(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, time.Hour, 16, true)
	k := testKey()
	if err := c.Put(k, Entry{Response: "complete response"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != k.Hash()+".json" {
		t.Fatalf("cache files = %v, want only final entry", entries)
	}
	info, err := os.Stat(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("entry permissions = %o, want 600", got)
	}
	entry, ok, err := c.Get(k)
	if err != nil || !ok || entry.Response != "complete response" {
		t.Fatalf("Get = (%+v, %v, %v), want complete response", entry, ok, err)
	}
}

func TestGetReportsCorruptEntry(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, time.Hour, 16, true)
	k := testKey()
	if err := os.WriteFile(filepath.Join(dir, k.Hash()+".json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("write corrupt entry: %v", err)
	}

	if _, ok, err := c.Get(k); err == nil || ok {
		t.Fatalf("Get corrupt entry = ok %v, err %v; want visible error", ok, err)
	}
	if got := c.Stats().LastError; !strings.Contains(got, "decode cache entry") {
		t.Errorf("LastError = %q, want decode error", got)
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
