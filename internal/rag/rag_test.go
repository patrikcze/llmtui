package rag

import (
	"encoding/json"
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

func TestFormatContextSanitizesTerminalControlSequences(t *testing.T) {
	out := FormatContext([]Result{{
		Chunk: DocumentChunk{
			Path:      "docs/\x1b]0;title\x07readme.md",
			StartLine: 1,
			EndLine:   1,
			Text:      "safe \x1b]52;c;Y2xpcA==\x07 text \x1b[2J",
		},
		MatchedTerms: []string{"\x1b[31mterm"},
	}}, 4096)
	if strings.ContainsRune(out, '\x1b') || strings.ContainsRune(out, '\x07') || strings.Contains(out, "Y2xpcA==") {
		t.Fatalf("terminal sequence survived RAG formatting: %q", out)
	}
	if !strings.Contains(out, "safe  text") {
		t.Fatalf("safe content was lost: %q", out)
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

func TestBuildSkipsFilesContainingHighConfidenceSecrets(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "private key", content: "notes\n-----BEGIN PRIVATE KEY-----\nabc"},
		{name: "AWS access key", content: "aws_access_key_id = AKIAIOSFODNN7EXAMPLE"},
		{name: "authorization bearer", content: "Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345"},
		{name: "x-api-key header", content: "x-api-key: abcdefghijklmnopqrstuvwxyz012345"},
		{name: "GitHub token", content: "token=ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "ordinary-config.txt", tt.content)
			writeFile(t, root, "safe.txt", "ordinary documentation")
			idx, skipped, err := Build(BuildConfig{Root: root, Include: []string{"**/*.txt"}})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if skipped == 0 {
				t.Fatal("secret-bearing file was not counted as skipped")
			}
			for _, source := range idx.Sources() {
				if source == "ordinary-config.txt" {
					t.Fatalf("secret-bearing file was indexed for %s", tt.name)
				}
			}
		})
	}
}

func TestBuildKeepsSecretPatternNearMisses(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "examples.txt", strings.Join([]string{
		"aws_access_key_id = AKIA...",
		"Authorization: Bearer <token>",
		"x-api-key: ${API_KEY}",
		"-----BEGIN PUBLIC KEY-----",
	}, "\n"))
	idx, _, err := Build(BuildConfig{Root: root, Include: []string{"**/*.txt"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(idx.Sources()) != 1 || idx.Sources()[0] != "examples.txt" {
		t.Fatalf("near-miss documentation was unexpectedly skipped: %v", idx.Sources())
	}
}

func TestStoreRejectsSecretBearingPersistedIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	data, err := json.Marshal(persisted{
		Version: currentIndexVersion,
		Root:    "/workspace",
		Chunks: []DocumentChunk{{
			ID:   "config.txt#1-1",
			Path: "config.txt",
			Text: "Authorization: Bearer abcdefghijklmnopqrstuvwxyz012345",
		}},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, indexFileName), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if idx, _, _, err := store.Load(); err == nil || idx != nil {
		t.Fatalf("Load = (%v, %v), want secret-bearing index rejection", idx, err)
	}
}

func TestStoreRejectsUnversionedLegacyIndex(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if err := os.WriteFile(
		filepath.Join(dir, indexFileName),
		[]byte(`{"root":"/workspace","chunks":[]}`),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if idx, _, _, err := store.Load(); err == nil || idx != nil {
		t.Fatalf("Load = (%v, %v), want reindex-required error", idx, err)
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

func TestStoreForRootIsolatesWorkspaces(t *testing.T) {
	base := NewStore(filepath.Join(t.TempDir(), "ragdir"))
	rootA, rootB := t.TempDir(), t.TempDir()
	writeFile(t, rootA, "a.md", "alpha secretless notes about apples")
	writeFile(t, rootB, "b.md", "beta notes about bananas")
	idxA, _, err := Build(BuildConfig{Root: rootA, Include: []string{"**/*.md"}})
	if err != nil {
		t.Fatal(err)
	}
	idxB, _, err := Build(BuildConfig{Root: rootB, Include: []string{"**/*.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := base.ForRoot(rootA).Save(idxA, rootA); err != nil {
		t.Fatalf("Save A: %v", err)
	}

	// Another workspace must not see A's excerpts.
	got, _, _, err := base.ForRoot(rootB).Load()
	if err != nil || got != nil {
		t.Fatalf("workspace B Load = (%v, %v), want no index and no error", got, err)
	}

	// Indexing B must not overwrite A.
	if err := base.ForRoot(rootB).Save(idxB, rootB); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	gotA, _, _, err := base.ForRoot(rootA).Load()
	if err != nil || gotA == nil || len(gotA.Search("apples", 3)) == 0 {
		t.Fatalf("workspace A index lost after indexing B: %v, %v", gotA, err)
	}

	// Clearing B leaves A intact.
	if err := base.ForRoot(rootB).Clear(); err != nil {
		t.Fatal(err)
	}
	if gotA, _, _, _ := base.ForRoot(rootA).Load(); gotA == nil {
		t.Error("Clear on workspace B removed workspace A's index")
	}
}

func TestStoreForRootRejectsMismatchedRecordedRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ragdir")
	rootA, rootB := t.TempDir(), t.TempDir()
	writeFile(t, rootA, "a.md", "apples")
	idx, _, err := Build(BuildConfig{Root: rootA, Include: []string{"**/*.md"}})
	if err != nil {
		t.Fatal(err)
	}
	storeB := NewStore(dir).ForRoot(rootB)
	if err := storeB.Save(idx, rootA); err == nil {
		t.Fatal("Save accepted an index recorded for a different workspace")
	}

	// A file planted (or copied) into B's slot but recorded for A is refused.
	if err := NewStore(dir).ForRoot(rootA).Save(idx, rootA); err != nil {
		t.Fatal(err)
	}
	src := NewStore(dir).ForRoot(rootA).path()
	dst := storeB.path()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := storeB.Load(); err == nil || got != nil {
		t.Fatalf("Load = (%v, %v), want mismatch error and no index", got, err)
	}
}

func TestStoreForRootTreatsSymlinkAsSameWorkspace(t *testing.T) {
	base := NewStore(filepath.Join(t.TempDir(), "ragdir"))
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeFile(t, real, "a.md", "apples")
	idx, _, err := Build(BuildConfig{Root: real, Include: []string{"**/*.md"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := base.ForRoot(real).Save(idx, real); err != nil {
		t.Fatal(err)
	}
	if got, _, _, err := base.ForRoot(link).Load(); err != nil || got == nil {
		t.Fatalf("symlinked spelling of the same workspace did not find its index: %v, %v", got, err)
	}
}
