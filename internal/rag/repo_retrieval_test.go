package rag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoQuery is one manifest entry against the real, production-indexed
// llmtui repository. requiredFile is independently declared ground truth —
// a real file that answers the query — named by a human reading the
// architecture docs, never inferred from what the indexer happens to
// return. This is the decision gate Phase 2 of the quality audit asks for:
// if retrieval already clears these floors, graph-assisted retrieval
// (MAPS or similar) is not yet justified by a measured failure.
type repoQuery struct {
	query        string
	requiredFile string
}

func repoQueryManifest() []repoQuery {
	return []repoQuery{
		{"where is native tool-call recovery handled", "internal/provider/tool_diagnostics.go"},
		{"what protects a write from executing before approval", "internal/tools/tools.go"},
		{"where does memory enter Active Context", "internal/prompt/compose.go"},
		{"what persists agent runs to disk", "internal/agent/store.go"},
		{"what determines embedded model capabilities", "internal/provider/capabilities.go"},
		{"what test verifies no-progress detection", "internal/tui/progress_test.go"},
		{"why can't visible pseudo tool call syntax execute", "internal/provider/tool_diagnostics.go"},
		{"where is response cache completeness protected", "internal/cache/cache.go"},
		{"how is provider stream truncation handled", "internal/provider/openai/stream.go"},
		{"what owns current agent acceptance criteria", "internal/agent/criteria.go"},
		{"how is the raw user message kept from being rewritten", "internal/prompt/compose.go"},
		{"what enforces workspace confinement on resolved file paths", "internal/tools/tools.go"},
		{"what classifies a shell command as read-only or destructive", "internal/tools/guardrails.go"},
		{"where are SSRF protections enforced for web fetch", "internal/web/ssrf.go"},
		{"what strips terminal escape sequences from untrusted text", "internal/terminaltext/sanitize.go"},
	}
}

// repoRoot resolves the repository root from this test's package directory
// (internal/rag) without depending on the working directory a caller
// happened to invoke `go test` from.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root := filepath.Clean(filepath.Join(wd, "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("resolved repo root %q does not look like the module root: %v", root, err)
	}
	return root
}

// TestRepoRetrievalCorpus builds the real workspace index through the
// production rag.Build entry point (never hand-built DocumentChunks) and
// measures retrieval quality against the independently declared manifest
// above. This is the decision gate for any future graph-assisted retrieval
// work: MAPS/graph complexity is not justified while plain BM25-lite
// keyword retrieval already clears these floors.
func TestRepoRetrievalCorpus(t *testing.T) {
	root := repoRoot(t)
	index, skipped, err := Build(BuildConfig{
		Root:    root,
		Include: []string{"**/*.go", "**/*.md", "**/*.txt", "**/*.yaml", "**/*.yml", "**/*.json"},
		// repo_retrieval_test.go itself is excluded: its manifest embeds the
		// query strings verbatim as Go string literals, so indexing it would
		// let every query match its own source file by construction — a
		// contamination bug in the harness, not a signal about real
		// retrieval quality.
		Exclude:    []string{".claude/**", "internal/rag/repo_retrieval_test.go"},
		MaxFileKB:  512,
		MaxTotalMB: 256,
	})
	if err != nil {
		t.Fatalf("build repo index: %v", err)
	}
	if index.Len() == 0 {
		t.Fatal("repo index is empty — indexing likely mis-rooted")
	}
	t.Logf("indexed %d chunks from %d source files, skipped %d entries", index.Len(), len(index.Sources()), skipped)

	manifest := repoQueryManifest()
	var hitsAt1, hitsAt3, hitsAt5, totalTokens, totalOverlaps int
	for _, q := range manifest {
		results := index.Search(q.query, 10)
		rank := -1
		for i, r := range results {
			if r.Chunk.Path == q.requiredFile {
				rank = i
				break
			}
		}
		switch {
		case rank >= 0 && rank < 1:
			hitsAt1++
		case rank >= 0 && rank < 3:
			hitsAt3++
		case rank >= 0 && rank < 5:
			hitsAt5++
		}
		top5 := results
		if len(top5) > 5 {
			top5 = top5[:5]
		}
		for _, r := range top5 {
			totalTokens += len(Tokenize(r.Chunk.Text))
		}
		totalOverlaps += overlappingResults(top5)
		if rank < 0 {
			t.Logf("query %q: %s not found in top 10 (top hit: %s)", q.query, q.requiredFile, firstPath(results))
		} else {
			t.Logf("query %q: %s at rank %d", q.query, q.requiredFile, rank+1)
		}
	}

	n := len(manifest)
	recallAt1 := float64(hitsAt1) / float64(n)
	recallAt3 := float64(hitsAt1+hitsAt3) / float64(n)
	recallAt5 := float64(hitsAt1+hitsAt3+hitsAt5) / float64(n)
	avgTokensTop5 := float64(totalTokens) / float64(n)
	t.Logf("recall@1=%.2f recall@3=%.2f recall@5=%.2f avg_context_tokens@5=%.1f overlaps_in_top5=%d",
		recallAt1, recallAt3, recallAt5, avgTokensTop5, totalOverlaps)

	// Decision-gate floor, measured on this manifest against current
	// master's plain BM25-lite retriever (recall@5 = 0.27 at the commit
	// that introduced this test — see docs/architecture find below). That
	// is meaningfully weaker than the curated fixtures in
	// retrieval_eval_test.go (file recall@1 avg 0.80), which only prove the
	// scoring algorithm works on short, keyword-dense synthetic chunks —
	// not that natural-language "why/what/where" questions find the right
	// file in a multi-thousand-chunk real corpus of mostly terse Go
	// identifiers. This floor is set with margin below the measured
	// baseline (not at it) because this index is a function of the whole
	// repository's live content: unrelated doc/comment edits shift BM25
	// term statistics slightly from commit to commit, so a tight floor
	// would flake on ordinary content changes. A drop to the floor or below
	// signals either a real retrieval regression (broken tokenization or
	// scoring) or that content drift has genuinely degraded recall enough
	// to warrant re-measuring — either way it should not be silently
	// ignored. This measurement, not the curated fixtures, is what should
	// decide whether MAPS/graph-assisted retrieval work is justified.
	const minRecallAt5 = 0.20
	if recallAt5 < minRecallAt5 {
		t.Fatalf("repo retrieval recall@5 = %.2f below required minimum %.2f — see per-query log above for which lookups failed", recallAt5, minRecallAt5)
	}
}

func firstPath(results []Result) string {
	if len(results) == 0 {
		return "(none)"
	}
	return results[0].Chunk.Path
}

// TestRepoRetrievalCorpusManifestIsGrounded is a fast sanity check,
// independent of indexing/search, that every manifest entry names a file
// that actually exists in the repository — so the gate above can never
// silently pass because a stale ground-truth path never had a chance to be
// found.
func TestRepoRetrievalCorpusManifestIsGrounded(t *testing.T) {
	root := repoRoot(t)
	for _, q := range repoQueryManifest() {
		path := filepath.Join(root, filepath.FromSlash(q.requiredFile))
		if _, err := os.Stat(path); err != nil {
			t.Errorf("manifest entry %q: required file %s does not exist: %v", q.query, q.requiredFile, err)
		}
		if strings.TrimSpace(q.query) == "" {
			t.Errorf("manifest entry for %s has an empty query", q.requiredFile)
		}
	}
}
