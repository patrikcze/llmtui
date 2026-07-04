package rag

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/tools"
)

// BuildConfig controls a workspace indexing pass.
type BuildConfig struct {
	Root       string   // workspace root; nothing outside it is indexed
	Include    []string // glob patterns (doublestar-lite, see match); empty = all text
	Exclude    []string // glob patterns pruned before include is considered
	MaxFileKB  int      // per-file size cap; larger files are skipped
	MaxTotalMB int      // cumulative content budget; indexing stops when exceeded
	ChunkLines int      // lines per chunk (default 40)
}

// DefaultExclude are always pruned even if a user's exclude list is empty:
// version control, dependency, and build directories.
var DefaultExclude = []string{
	".git/**", "node_modules/**", "vendor/**", "dist/**", "build/**", ".idea/**", ".vscode/**",
}

// Build walks Root and returns an index plus the number of files skipped.
// Safety rules (always applied, independent of config): never index outside
// Root, never follow a symlink that escapes Root, never index binary files,
// never index likely-secret files (.env, *.pem, id_rsa, ...).
func Build(cfg BuildConfig) (*Index, int, error) {
	if cfg.Root == "" {
		return nil, 0, fmt.Errorf("rag: index root is empty")
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, 0, fmt.Errorf("rag: resolve root: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, 0, fmt.Errorf("rag: resolve root: %w", err)
	}
	chunkLines := cfg.ChunkLines
	if chunkLines <= 0 {
		chunkLines = 40
	}
	maxFileBytes := int64(cfg.MaxFileKB) * 1024
	if maxFileBytes <= 0 {
		maxFileBytes = 512 * 1024
	}
	budget := int64(cfg.MaxTotalMB) * 1024 * 1024
	if budget <= 0 {
		budget = 256 * 1024 * 1024
	}
	exclude := append(append([]string{}, DefaultExclude...), cfg.Exclude...)

	var chunks []DocumentChunk
	skipped := 0
	var used int64

	walkErr := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip, don't abort the whole walk
		}
		rel, rerr := filepath.Rel(root, abs)
		if rerr != nil {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if slashRel == "." {
			return nil
		}
		if d.IsDir() {
			if matchAny(exclude, slashRel+"/") || matchAny(exclude, slashRel) {
				return fs.SkipDir
			}
			return nil
		}
		// Files.
		if matchAny(exclude, slashRel) {
			skipped++
			return nil
		}
		if tools.IsSecretPath(slashRel) {
			skipped++
			return nil
		}
		if len(cfg.Include) > 0 && !matchAny(cfg.Include, slashRel) {
			skipped++
			return nil
		}
		// Reject symlinks that resolve outside the workspace.
		if resolved, e := filepath.EvalSymlinks(abs); e == nil {
			if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
				skipped++
				return nil
			}
		}
		info, e := d.Info()
		if e != nil || info.Size() == 0 || info.Size() > maxFileBytes {
			skipped++
			return nil
		}
		data, e := os.ReadFile(abs)
		if e != nil {
			skipped++
			return nil
		}
		if isBinary(data) {
			skipped++
			return nil
		}
		if used+int64(len(data)) > budget {
			return fs.SkipAll // total budget exhausted
		}
		used += int64(len(data))
		chunks = append(chunks, chunkFile(slashRel, string(data), chunkLines, info.ModTime())...)
		return nil
	})
	if walkErr != nil {
		return nil, skipped, walkErr
	}
	return NewIndex(chunks), skipped, nil
}

// chunkFile splits content into line-windowed chunks.
func chunkFile(relPath, content string, chunkLines int, modTime time.Time) []DocumentChunk {
	lines := strings.Split(content, "\n")
	// Drop a trailing empty element from a final newline.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var chunks []DocumentChunk
	for start := 0; start < len(lines); start += chunkLines {
		end := start + chunkLines
		if end > len(lines) {
			end = len(lines)
		}
		text := strings.Join(lines[start:end], "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		sum := sha256.Sum256([]byte(text))
		hash := hex.EncodeToString(sum[:8])
		chunks = append(chunks, DocumentChunk{
			ID:        fmt.Sprintf("%s#%d-%d", relPath, start+1, end),
			Path:      relPath,
			StartLine: start + 1,
			EndLine:   end,
			Text:      text,
			Hash:      hash,
			UpdatedAt: modTime,
		})
	}
	return chunks
}

// isBinary reports whether data looks like a binary file: a NUL byte in the
// first 8 KB is the conventional heuristic (git uses the same idea).
func isBinary(data []byte) bool {
	n := len(data)
	if n > 8192 {
		n = 8192
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

// matchAny reports whether name matches any of the glob patterns. It
// supports a "**" prefix/suffix for recursive matching (e.g. "**/*.go",
// ".git/**") plus standard path.Match on the final segment.
func matchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if matchGlob(p, name) {
			return true
		}
	}
	return false
}

func matchGlob(pattern, name string) bool {
	// "prefix/**" matches prefix itself and anything beneath it.
	if suffix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return name == suffix || strings.HasPrefix(name, suffix+"/")
	}
	// "**/glob" matches glob against the basename or any trailing segment.
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
		if ok, _ := path.Match(rest, path.Base(name)); ok {
			return true
		}
		// Also try matching against each trailing path suffix.
		segs := strings.Split(name, "/")
		for i := range segs {
			if ok, _ := path.Match(rest, strings.Join(segs[i:], "/")); ok {
				return true
			}
		}
		return false
	}
	if ok, _ := path.Match(pattern, name); ok {
		return true
	}
	// A bare pattern with no slash also matches the basename.
	if !strings.Contains(pattern, "/") {
		if ok, _ := path.Match(pattern, path.Base(name)); ok {
			return true
		}
	}
	return false
}
