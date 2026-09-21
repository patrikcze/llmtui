package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// indexFileName is the on-disk index inside the configured index directory.
const indexFileName = "index.json"

// currentIndexVersion invalidates indexes created before content-based
// secret scanning existed. Loading those raw chunks would bypass the scanner
// until the user happened to rebuild the index.
const currentIndexVersion = 1

// persisted is the serialized form. BM25 statistics are not stored; they are
// recomputed on Load, so the file stays small and format-stable.
type persisted struct {
	Version int             `json:"version"`
	Root    string          `json:"root"`
	BuiltAt time.Time       `json:"built_at"`
	Chunks  []DocumentChunk `json:"chunks"`
}

// Store reads and writes an index under a directory.
type Store struct {
	dir string
	// root, when set, is the canonical workspace this store is bound to; Load
	// and Save refuse an index recorded for any other workspace.
	root string
}

// NewStore targets dir (created on Save if missing).
func NewStore(dir string) *Store { return &Store{dir: dir} }

// ForRoot returns a store bound to one workspace. Its index lives in a
// subdirectory named after the canonical workspace path, so indexing project
// B never replaces project A's index, and an index recorded for a different
// workspace is rejected rather than served. An index written by an older
// unscoped release (dir/index.json) is deliberately not read: its excerpts
// cannot be attributed to a workspace without trusting its recorded root.
func (s *Store) ForRoot(root string) *Store {
	canon := CanonicalRoot(root)
	sum := sha256.Sum256([]byte(canon))
	return &Store{dir: filepath.Join(s.dir, hex.EncodeToString(sum[:8])), root: canon}
}

// CanonicalRoot returns the absolute, symlink-resolved form of root so two
// spellings of the same directory share one identity. If the path cannot be
// resolved (for example it does not exist) the absolute path is used.
func CanonicalRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func (s *Store) path() string { return filepath.Join(s.dir, indexFileName) }

// Save writes the index for root to disk with owner-only permissions (the
// index may contain workspace source excerpts).
func (s *Store) Save(idx *Index, root string) error {
	if idx == nil {
		return fmt.Errorf("rag: save nil index")
	}
	if s.root != "" && CanonicalRoot(root) != s.root {
		return fmt.Errorf("rag: index root %q does not match the store workspace %q", root, s.root)
	}
	if containsSecretChunk(idx.Chunks) {
		return fmt.Errorf("rag: refusing to save an index containing likely secret material")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("rag: create index dir: %w", err)
	}
	data, err := json.Marshal(persisted{
		Version: currentIndexVersion,
		Root:    root,
		BuiltAt: time.Now(),
		Chunks:  idx.Chunks,
	})
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
	if p.Version != currentIndexVersion {
		return nil, "", time.Time{}, fmt.Errorf("rag: index format changed; run /rag index to rebuild it")
	}
	if s.root != "" && CanonicalRoot(p.Root) != s.root {
		return nil, "", time.Time{}, fmt.Errorf("rag: stored index belongs to workspace %q, not %q; run /rag index to build one for this workspace", p.Root, s.root)
	}
	if containsSecretChunk(p.Chunks) {
		return nil, "", time.Time{}, fmt.Errorf("rag: persisted index contains likely secret material; run /rag index to rebuild it safely")
	}
	return NewIndex(p.Chunks), p.Root, p.BuiltAt, nil
}

func containsSecretChunk(chunks []DocumentChunk) bool {
	for _, chunk := range chunks {
		if containsLikelySecret([]byte(chunk.Text)) {
			return true
		}
	}
	return false
}

// Clear removes the on-disk index. A missing file is not an error.
func (s *Store) Clear() error {
	if err := os.Remove(s.path()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rag: clear index: %w", err)
	}
	return nil
}
