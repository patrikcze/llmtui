// Package cache implements a local, file-based response cache so repeated
// prompts against slow local models return instantly. Entries are keyed by
// everything that influences the response — never by API keys.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Key identifies one cacheable request. API keys and other secrets must
// never be part of this struct.
type Key struct {
	Provider     string
	BaseURL      string
	Model        string
	UserMessage  string
	SystemPrompt string
	PromptMode   string
	Template     string
	Temperature  float64
	TopP         float64
	MaxTokens    int
}

// Hash returns a stable content hash for the key. Free-text fields are
// hashed individually so the canonical string cannot be confused by
// separator characters inside them.
func (k Key) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|%s|%s|%s|%s|%s|%s|%s|%.4f|%.4f|%d",
		k.Provider,
		hashText(k.BaseURL),
		k.Model,
		hashText(k.UserMessage),
		hashText(k.SystemPrompt),
		k.PromptMode,
		k.Template,
		k.Temperature,
		k.TopP,
		k.MaxTokens,
	)
	return hex.EncodeToString(h.Sum(nil))
}

func hashText(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Entry is one cached response.
type Entry struct {
	Response         string    `json:"response"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	Estimated        bool      `json:"estimated"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	CreatedAt        time.Time `json:"created_at"`
}

// Stats reports the state of the cache.
type Stats struct {
	Entries   int
	SizeBytes int64
	Hits      int
	Misses    int
	Enabled   bool
}

// Cache is a directory of JSON entries with TTL and size pruning.
type Cache struct {
	mu       sync.Mutex
	dir      string
	ttl      time.Duration
	maxBytes int64
	enabled  bool
	hits     int
	misses   int
}

// New creates a cache rooted at dir. A zero ttl means entries never expire.
func New(dir string, ttl time.Duration, maxSizeMB int, enabled bool) *Cache {
	return &Cache{
		dir:      dir,
		ttl:      ttl,
		maxBytes: int64(maxSizeMB) * 1024 * 1024,
		enabled:  enabled,
	}
}

// Enabled reports whether lookups and writes are active.
func (c *Cache) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled
}

// SetEnabled toggles the cache at runtime.
func (c *Cache) SetEnabled(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabled = on
}

func (c *Cache) path(k Key) string {
	return filepath.Join(c.dir, k.Hash()+".json")
}

// Get returns the cached entry for k, honoring TTL. Expired entries are
// removed. Misses and disabled lookups are counted.
func (c *Cache) Get(k Key) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return Entry{}, false
	}
	data, err := os.ReadFile(c.path(k))
	if err != nil {
		c.misses++
		return Entry{}, false
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		c.misses++
		return Entry{}, false
	}
	if c.ttl > 0 && time.Since(e.CreatedAt) > c.ttl {
		os.Remove(c.path(k))
		c.misses++
		return Entry{}, false
	}
	c.hits++
	return e, true
}

// Put stores an entry, then prunes oldest entries when over the size limit.
func (c *Cache) Put(k Key, e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	e.CreatedAt = time.Now()
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode cache entry: %w", err)
	}
	if err := os.WriteFile(c.path(k), data, 0o600); err != nil {
		return fmt.Errorf("write cache entry: %w", err)
	}
	return c.prune()
}

// prune removes oldest entries until total size fits maxBytes.
func (c *Cache) prune() error {
	if c.maxBytes <= 0 {
		return nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil
	}
	type fileInfo struct {
		name string
		mod  time.Time
		size int64
	}
	var files []fileInfo
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{e.Name(), info.ModTime(), info.Size()})
		total += info.Size()
	}
	if total <= c.maxBytes {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= c.maxBytes {
			break
		}
		if err := os.Remove(filepath.Join(c.dir, f.name)); err == nil {
			total -= f.size
		}
	}
	return nil
}

// Clear removes all cached entries.
func (c *Cache) Clear() (removed int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			if os.Remove(filepath.Join(c.dir, e.Name())) == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// Stats returns entry count, disk usage, and hit/miss counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{Hits: c.hits, Misses: c.misses, Enabled: c.enabled}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return s
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if info, err := e.Info(); err == nil {
			s.Entries++
			s.SizeBytes += info.Size()
		}
	}
	return s
}
