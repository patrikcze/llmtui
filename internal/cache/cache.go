// Package cache implements a local, file-based response cache so repeated
// prompts against slow local models return instantly. Entries are keyed by
// everything that influences the response — never by API keys.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
//
// SystemPrompt must be the fully composed system prompt actually sent to the
// provider (including tool/RAG/memory instructions), not just the raw
// chat.system_prompt config value — two requests that differ only in, say,
// whether tool instructions were appended produce different responses and
// must not share a cache entry. HistoryHash must fingerprint the prior
// conversation turns: without it, two different conversations that happen
// to send the same short next message (e.g. "yes") under identical settings
// would collide and one would get served the other's out-of-context answer.
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
	HistoryHash  string
	// ToolsHash fingerprints the tool specs actually offered to the model
	// (native, web, and MCP) — connecting or disconnecting an MCP server
	// changes what's sent to the provider even when nothing else about the
	// request changes, and a cache hit must not straddle that difference.
	ToolsHash string
}

// Hash returns a stable content hash for the key. Free-text fields are
// hashed individually so the canonical string cannot be confused by
// separator characters inside them.
func (k Key) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "v4|%s|%s|%s|%s|%s|%s|%s|%.4f|%.4f|%d|%s|%s",
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
		k.HistoryHash,
		k.ToolsHash,
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
	LastError string
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
	lastErr  error
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
// removed. Missing entries are ordinary misses; malformed or inaccessible
// entries return an error so callers can make degraded cache health visible.
func (c *Cache) Get(k Key) (Entry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return Entry{}, false, nil
	}
	path := c.path(k)
	data, err := os.ReadFile(path)
	if err != nil {
		c.misses++
		if os.IsNotExist(err) {
			return Entry{}, false, nil
		}
		return Entry{}, false, c.remember(fmt.Errorf("read cache entry: %w", err))
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		c.misses++
		decodeErr := fmt.Errorf("decode cache entry: %w", err)
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			decodeErr = errors.Join(decodeErr, fmt.Errorf("remove corrupt cache entry: %w", removeErr))
		}
		return Entry{}, false, c.remember(decodeErr)
	}
	if c.ttl > 0 && time.Since(e.CreatedAt) > c.ttl {
		c.misses++
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return Entry{}, false, c.remember(fmt.Errorf("remove expired cache entry: %w", err))
		}
		return Entry{}, false, nil
	}
	c.hits++
	return e, true, nil
}

// Put stores an entry, then prunes oldest entries when over the size limit.
func (c *Cache) Put(k Key, e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return c.remember(fmt.Errorf("create cache directory: %w", err))
	}
	e.CreatedAt = time.Now()
	data, err := json.Marshal(e)
	if err != nil {
		return c.remember(fmt.Errorf("encode cache entry: %w", err))
	}
	if err := writeAtomic(c.path(k), data); err != nil {
		return c.remember(fmt.Errorf("write cache entry: %w", err))
	}
	if err := c.prune(); err != nil {
		return c.remember(err)
	}
	return nil
}

func writeAtomic(path string, data []byte) (retErr error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".llmtui-cache-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			retErr = errors.Join(retErr, tmp.Close())
		}
		if err := os.Remove(tmpPath); err != nil && !os.IsNotExist(err) {
			retErr = errors.Join(retErr, fmt.Errorf("remove temporary file: %w", err))
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	closed = true
	if err := os.Rename(tmpPath, path); err != nil {
		// Another process may have won an immutable-key race. Preserve its
		// complete entry rather than replacing it non-atomically on platforms
		// where Rename cannot overwrite an existing file.
		if info, statErr := os.Stat(path); statErr == nil && info.Mode().IsRegular() {
			return nil
		}
		return fmt.Errorf("rename temporary file: %w", err)
	}
	return nil
}

// prune removes oldest entries until total size fits maxBytes.
func (c *Cache) prune() error {
	if c.maxBytes <= 0 {
		return nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return fmt.Errorf("read cache directory for pruning: %w", err)
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
			return fmt.Errorf("inspect cache entry for pruning: %w", err)
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
		if err := os.Remove(filepath.Join(c.dir, f.name)); err != nil {
			return fmt.Errorf("remove cache entry during pruning: %w", err)
		}
		total -= f.size
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
		return 0, c.remember(fmt.Errorf("read cache directory: %w", err))
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if err := os.Remove(filepath.Join(c.dir, e.Name())); err != nil {
			return removed, c.remember(fmt.Errorf("remove cache entry: %w", err))
		}
		removed++
	}
	return removed, nil
}

// Stats returns entry count, disk usage, and hit/miss counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{Hits: c.hits, Misses: c.misses, Enabled: c.enabled}
	if c.lastErr != nil {
		s.LastError = c.lastErr.Error()
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if !os.IsNotExist(err) {
			s.LastError = c.remember(fmt.Errorf("read cache statistics: %w", err)).Error()
		}
		return s
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			s.LastError = c.remember(fmt.Errorf("inspect cache statistics: %w", err)).Error()
			continue
		}
		s.Entries++
		s.SizeBytes += info.Size()
	}
	return s
}

// remember retains the most recent operational failure for /cache status.
// Callers must hold c.mu.
func (c *Cache) remember(err error) error {
	c.lastErr = err
	return err
}
