package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	defaultMaxRunBytes = 64 * 1024
	defaultMaxRuns     = 32
)

var (
	// ErrRunNotFound is returned when no persisted run matches an ID.
	ErrRunNotFound = errors.New("agent run not found")
	// ErrCorruptRun is returned for invalid, unsupported, or oversized state.
	ErrCorruptRun = errors.New("corrupt agent run state")
)

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)((?:token|secret|password|passwd|authorization|api[_-]?key)\s*[=:]\s*)[^\s,;}]+`)
	bearerPattern           = regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]{8,}`)
	keyPattern              = regexp.MustCompile(`\b(?:sk|ghp|github_pat)-[A-Za-z0-9_-]{8,}\b`)
	privateKeyPattern       = regexp.MustCompile(`(?s)-----BEGIN [^-\n]*PRIVATE KEY-----.*?-----END [^-\n]*PRIVATE KEY-----`)
)

// Store persists bounded run state. Implementations must be safe for use by
// asynchronous commands and must honor context cancellation.
type Store interface {
	Save(ctx context.Context, run *AgentRun) error
	Load(ctx context.Context, id string) (*AgentRun, error)
	Latest(ctx context.Context) (*AgentRun, error)
}

// FileStore atomically persists versioned run records in one private directory.
type FileStore struct {
	dir      string
	maxBytes int
	maxRuns  int
	mu       sync.Mutex
}

// NewFileStore returns a bounded filesystem store. Non-positive limits use
// conservative defaults.
func NewFileStore(dir string, maxBytes, maxRuns int) *FileStore {
	if maxBytes <= 0 {
		maxBytes = defaultMaxRunBytes
	}
	if maxRuns <= 0 {
		maxRuns = defaultMaxRuns
	}
	return &FileStore{dir: dir, maxBytes: maxBytes, maxRuns: maxRuns}
}

// Save writes one record to a temporary file, fsyncs it, and renames it into
// place so interruption cannot truncate the previous valid record.
func (s *FileStore) Save(ctx context.Context, run *AgentRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if run == nil || !validRunID(run.ID) {
		return NewError(ErrorMemoryWrite, "save run", fmt.Errorf("%w: invalid run ID", ErrCorruptRun))
	}
	data, err := encodePersistedRun(run, true)
	if err != nil {
		return NewError(ErrorMemoryWrite, "encode run", err)
	}
	if len(data) > s.maxBytes {
		return NewError(ErrorMemoryWrite, "save run", fmt.Errorf("%w: record is %d bytes, maximum is %d", ErrCorruptRun, len(data), s.maxBytes))
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	// Bubble Tea commands complete asynchronously. Never let a delayed older
	// snapshot overwrite a newer lifecycle transition for the same run.
	if existing, loadErr := readLimited(filepath.Join(s.dir, run.ID+".json"), s.maxBytes); loadErr == nil {
		if saved, decodeErr := decodeRun(existing); decodeErr == nil && saved.UpdatedAt.After(run.UpdatedAt) {
			return nil
		}
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return NewError(ErrorMemoryWrite, "create run directory", err)
	}
	path := filepath.Join(s.dir, run.ID+".json")
	tmp, err := os.CreateTemp(s.dir, ".run-*.tmp")
	if err != nil {
		return NewError(ErrorMemoryWrite, "create run temp file", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return NewError(ErrorMemoryWrite, "secure run temp file", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return NewError(ErrorMemoryWrite, "write run", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return NewError(ErrorMemoryWrite, "sync run", err)
	}
	if err := tmp.Close(); err != nil {
		return NewError(ErrorMemoryWrite, "close run", err)
	}
	if err := replaceFile(tmpPath, path); err != nil {
		return NewError(ErrorMemoryWrite, "finalize run", err)
	}
	removeTemp = false
	if err := s.pruneLocked(); err != nil {
		return NewError(ErrorMemoryWrite, "prune run history", err)
	}
	return nil
}

// Load returns one validated record.
func (s *FileStore) Load(ctx context.Context, id string) (*AgentRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !validRunID(id) {
		return nil, NewError(ErrorMemoryRead, "load run", fmt.Errorf("%w: invalid run ID", ErrCorruptRun))
	}
	data, err := readLimited(filepath.Join(s.dir, id+".json"), s.maxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, NewError(ErrorMemoryRead, "read run", err)
	}
	run, err := decodeRun(data)
	if err != nil {
		return nil, NewError(ErrorMemoryRead, "decode run", err)
	}
	return run, nil
}

// Latest returns the newest valid run, skipping corrupt/foreign records. A
// corrupt newest file therefore never prevents recovery of an earlier run.
func (s *FileStore) Latest(ctx context.Context) (*AgentRun, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, NewError(ErrorMemoryRead, "list runs", err)
	}
	type candidate struct {
		id      string
		modTime int64
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !validRunID(id) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr == nil {
			candidates = append(candidates, candidate{id: id, modTime: info.ModTime().UnixNano()})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime > candidates[j].modTime })
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		run, loadErr := s.Load(ctx, candidate.id)
		if loadErr == nil {
			return run, nil
		}
		if errors.Is(loadErr, context.Canceled) || errors.Is(loadErr, context.DeadlineExceeded) {
			return nil, loadErr
		}
	}
	return nil, ErrRunNotFound
}

func (s *FileStore) pruneLocked() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	type item struct {
		path string
		mod  int64
	}
	var items []item
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr == nil {
			items = append(items, item{path: filepath.Join(s.dir, entry.Name()), mod: info.ModTime().UnixNano()})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod > items[j].mod })
	if len(items) <= s.maxRuns {
		return nil
	}
	for _, item := range items[s.maxRuns:] {
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func readLimited(path string, maxBytes int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(maxBytes) {
		return nil, fmt.Errorf("%w: record exceeds %d bytes", ErrCorruptRun, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("%w: record exceeds %d bytes", ErrCorruptRun, maxBytes)
	}
	return data, nil
}

func decodeRun(data []byte) (*AgentRun, error) {
	var run AgentRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptRun, err)
	}
	if run.Version != SchemaVersion || !validRunID(run.ID) || run.Request == "" {
		return nil, fmt.Errorf("%w: unsupported or incomplete record", ErrCorruptRun)
	}
	if err := validateLimits(run.Limits); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptRun, err)
	}
	return &run, nil
}

func validRunID(id string) bool {
	if id == "" || len(id) > 128 || id != filepath.Base(id) || strings.HasPrefix(id, ".") {
		return false
	}
	for _, r := range id {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
		if !valid {
			return false
		}
	}
	return true
}

// MemoryStore is an in-memory Store for tests and embedded consumers.
type MemoryStore struct {
	mu   sync.RWMutex
	runs map[string][]byte
}

// NewMemoryStore creates an empty in-memory run store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{runs: make(map[string][]byte)} }

func (s *MemoryStore) Save(ctx context.Context, run *AgentRun) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if run == nil || !validRunID(run.ID) {
		return NewError(ErrorMemoryWrite, "save run", fmt.Errorf("%w: invalid run", ErrCorruptRun))
	}
	data, err := encodePersistedRun(run, false)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.runs[run.ID]; ok {
		if saved, decodeErr := decodeRun(existing); decodeErr == nil && saved.UpdatedAt.After(run.UpdatedAt) {
			return nil
		}
	}
	s.runs[run.ID] = append([]byte(nil), data...)
	return nil
}

func (s *MemoryStore) Load(ctx context.Context, id string) (*AgentRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	data, ok := s.runs[id]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrRunNotFound
	}
	return decodeRun(append([]byte(nil), data...))
}

func (s *MemoryStore) Latest(ctx context.Context) (*AgentRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var latest *AgentRun
	for _, data := range s.runs {
		run, err := decodeRun(data)
		if err == nil && (latest == nil || run.UpdatedAt.After(latest.UpdatedAt)) {
			latest = run
		}
	}
	if latest == nil {
		return nil, ErrRunNotFound
	}
	return latest, nil
}

var _ Store = (*FileStore)(nil)
var _ Store = (*MemoryStore)(nil)

func encodePersistedRun(run *AgentRun, indent bool) ([]byte, error) {
	data, err := json.Marshal(run)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	redactJSONStrings(value)
	if indent {
		return json.MarshalIndent(value, "", "  ")
	}
	return json.Marshal(value)
}

func redactJSONStrings(value any) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if text, ok := item.(string); ok {
				typed[key] = redactSecrets(text)
				continue
			}
			redactJSONStrings(item)
		}
	case []any:
		for i, item := range typed {
			if text, ok := item.(string); ok {
				typed[i] = redactSecrets(text)
				continue
			}
			redactJSONStrings(item)
		}
	}
}

func redactSecrets(value string) string {
	value = privateKeyPattern.ReplaceAllString(value, "[REDACTED PRIVATE KEY]")
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = secretAssignmentPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return keyPattern.ReplaceAllString(value, "[REDACTED KEY]")
}
