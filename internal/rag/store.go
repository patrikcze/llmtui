package rag

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// indexFileName is the on-disk index inside the configured index directory.
const indexFileName = "index.json"

// persisted is the serialized form. BM25 statistics are not stored; they are
// recomputed on Load, so the file stays small and format-stable.
type persisted struct {
	Root    string          `json:"root"`
	BuiltAt time.Time       `json:"built_at"`
	Chunks  []DocumentChunk `json:"chunks"`
}

// Store reads and writes an index under a directory.
type Store struct {
	dir string
}

// NewStore targets dir (created on Save if missing).
func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) path() string { return filepath.Join(s.dir, indexFileName) }

// Save writes the index for root to disk with owner-only permissions (the
// index may contain workspace source excerpts).
func (s *Store) Save(idx *Index, root string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("rag: create index dir: %w", err)
	}
	data, err := json.Marshal(persisted{Root: root, BuiltAt: time.Now(), Chunks: idx.Chunks})
	if err != nil {
		return fmt.Errorf("rag: encode index: %w", err)
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("rag: write index: %w", err)
	}
	if err := os.Rename(tmp, s.path()); err != nil {
		return fmt.Errorf("rag: replace index: %w", err)
	}
	return nil
}

// Load reads the index. A missing file returns (nil, "", time.Time{}, nil):
// no index yet is not an error.
func (s *Store) Load() (idx *Index, root string, builtAt time.Time, err error) {
	data, rerr := os.ReadFile(s.path())
	if os.IsNotExist(rerr) {
		return nil, "", time.Time{}, nil
	}
	if rerr != nil {
		return nil, "", time.Time{}, fmt.Errorf("rag: read index: %w", rerr)
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, "", time.Time{}, fmt.Errorf("rag: decode index: %w", err)
	}
	return NewIndex(p.Chunks), p.Root, p.BuiltAt, nil
}

// Clear removes the on-disk index. A missing file is not an error.
func (s *Store) Clear() error {
	if err := os.Remove(s.path()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rag: clear index: %w", err)
	}
	return nil
}
