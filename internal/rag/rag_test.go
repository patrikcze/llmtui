package rag

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFile is a test helper.
func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildIndexesTextFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package main\nfunc streamParser() {}\n")
	writeFile(t, root, "docs/readme.md", "# Title\nstreaming parser docs\n")

	idx, _, err := Build(BuildConfig{Root: root, Include: []string{"**/*.go", "**/*.md"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if idx.Len() == 0 {
		t.Fatal("no chunks indexed")
	}
	srcs := idx.Sources()
	if len(srcs) != 2 {
		t.Errorf("Sources = %v, want 2 files", srcs)
	}
}

func TestBuildSkipsBinary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "text.txt", "hello world readable")
	// A NUL byte marks the file as binary.
	writeFile(t, root, "blob.txt", "abc\x00def binary content")

	idx, _, err := Build(BuildConfig{Root: root, Include: []string{"**/*.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range idx.Sources() {
		if s == "blob.txt" {
			t.Error("binary file was indexed")
		}
	}
}

func TestBuildSkipsGitAndSecrets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "keep.md", "normal content here")
	writeFile(t, root, ".git/config", "[core]\n\trepositoryformatversion = 0\n")
	writeFile(t, root, ".env", "SECRET_TOKEN=abc123")
	writeFile(t, root, "server.pem", "-----BEGIN PRIVATE KEY-----")

	idx, _, err := Build(BuildConfig{Root: root}) // no include filter = all text
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range idx.Sources() {
		switch {
		case strings.HasPrefix(s, ".git/"):
			t.Errorf(".git file indexed: %s", s)
		case s == ".env":
			t.Error(".env indexed")
		case s == "server.pem":
			t.Error("secret .pem indexed")
		}
	}
	// The clean file should still be present.
	found := false
	for _, s := range idx.Sources() {
		if s == "keep.md" {
			found = true
		}
	}
	if !found {
		t.Error("clean file keep.md was not indexed")
	}
}

func TestBuildRespectsMaxFileSize(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "small.txt", "tiny")
	writeFile(t, root, "big.txt", strings.Repeat("x ", 2000)) // ~4KB

	idx, _, err := Build(BuildConfig{Root: root, Include: []string{"**/*.txt"}, MaxFileKB: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range idx.Sources() {
		if s == "big.txt" {
			t.Error("file over max_file_kb was indexed")
		}
	}
}

func TestBuildRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeFile(t, outside, "secret.txt", "outside the workspace")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("cannot symlink: %v", err)
	}
	writeFile(t, root, "inside.txt", "inside the workspace")

	idx, _, err := Build(BuildConfig{Root: root, Include: []string{"**/*.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range idx.Sources() {
		if s == "link.txt" {
			t.Error("symlink escaping the workspace was indexed")
		}
	}
}

func TestSearchRanksRelevantChunk(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "stream.go", "func streaming() {\n// parse server-sent events\n}\n")
	writeFile(t, root, "unrelated.go", "func addNumbers(a, b int) int { return a + b }\n")

	idx, _, err := Build(BuildConfig{Root: root, Include: []string{"**/*.go"}})
	if err != nil {
		t.Fatal(err)
	}
	results := idx.Search("streaming parser", 6)
	if len(results) == 0 {
		t.Fatal("no results for a query that should match")
	}
	if results[0].Chunk.Path != "stream.go" {
		t.Errorf("top result = %s, want stream.go", results[0].Chunk.Path)
	}
	if len(results[0].MatchedTerms) == 0 {
		t.Error("top result reports no matched terms")
	}
}

func TestSearchEmptyQueryOrIndex(t *testing.T) {
	idx := NewIndex(nil)
	if got := idx.Search("anything", 6); got != nil {
		t.Errorf("search on empty index = %v, want nil", got)
	}
	idx2 := NewIndex([]DocumentChunk{{Path: "a", Text: "hello"}})
	if got := idx2.Search("   ", 6); got != nil {
		t.Errorf("empty query = %v, want nil", got)
	}
}

func TestStoreSaveLoadClear(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "content for indexing")
	idx, _, err := Build(BuildConfig{Root: root, Include: []string{"**/*.md"}})
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "ragdir"))

	if err := store.Save(idx, root); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, gotRoot, _, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil || loaded.Len() != idx.Len() {
		t.Fatalf("loaded index mismatch: %v", loaded)
	}
	if gotRoot != root {
		t.Errorf("loaded root = %q, want %q", gotRoot, root)
	}
	// Loaded index must be searchable (stats recomputed).
	if len(loaded.Search("content", 6)) == 0 {
		t.Error("loaded index not searchable")
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	after, _, _, err := store.Load()
	if err != nil {
		t.Fatalf("Load after clear: %v", err)
	}
	if after != nil {
		t.Error("index still present after Clear")
	}
}

func TestFormatContextSeparatesAndLabels(t *testing.T) {
	results := []Result{
		{Chunk: DocumentChunk{Path: "x.go", StartLine: 10, EndLine: 20, Text: "line one\nline two"}, MatchedTerms: []string{"stream"}},
	}
	out := FormatContext(results, 0)
	if !strings.Contains(out, "file: x.go lines 10-20") {
		t.Errorf("missing source label:\n%s", out)
	}
	if !strings.Contains(out, `matched "stream"`) {
		t.Errorf("missing reason:\n%s", out)
	}
	if !strings.Contains(out, "content:") {
		t.Errorf("missing content marker:\n%s", out)
	}
}

func TestFormatContextRespectsCharCap(t *testing.T) {
	long := strings.Repeat("word ", 500)
	results := []Result{
		{Chunk: DocumentChunk{Path: "a", StartLine: 1, EndLine: 1, Text: long}},
		{Chunk: DocumentChunk{Path: "b", StartLine: 1, EndLine: 1, Text: long}},
	}
	out := FormatContext(results, 200)
	if strings.Contains(out, "file: b") {
		t.Error("second snippet included despite char cap")
	}
	if !strings.Contains(out, "file: a") {
		t.Error("first snippet dropped by char cap")
	}
}
