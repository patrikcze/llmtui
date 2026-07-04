// Package rag provides an optional, local-first workspace index and keyword
// retrieval. It is disabled by default; nothing here runs unless the user
// enables RAG and indexes a workspace. Retrieval is pure keyword scoring
// (BM25-lite) — no embeddings, no external services, no vector database.
//
// Retrieved snippets are returned with their source path and line range so
// the caller can present them as clearly-labeled reference material that
// never replaces the user's own message.
package rag

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DocumentChunk is one indexed slice of a workspace file.
type DocumentChunk struct {
	ID        string    `json:"id"`
	Path      string    `json:"path"` // workspace-relative, slash-separated
	StartLine int       `json:"start_line"`
	EndLine   int       `json:"end_line"`
	Text      string    `json:"text"`
	Hash      string    `json:"hash"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Result is one retrieval hit: the chunk, its score, and the query terms
// that matched (for a human-readable "reason").
type Result struct {
	Chunk        DocumentChunk
	Score        float64
	MatchedTerms []string
}

// Index is a searchable collection of chunks with precomputed term
// statistics. Build it with NewIndex; it is safe for concurrent reads but
// not concurrent modification.
type Index struct {
	Chunks []DocumentChunk

	// Precomputed BM25 statistics (not serialized; rebuilt on load).
	termFreq  []map[string]int // per-chunk term -> count
	docFreq   map[string]int   // term -> number of chunks containing it
	docLen    []int            // per-chunk token count
	avgDocLen float64
}

// BM25 parameters (standard defaults).
const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

// NewIndex builds an index and precomputes its term statistics.
func NewIndex(chunks []DocumentChunk) *Index {
	idx := &Index{Chunks: chunks}
	idx.compute()
	return idx
}

// compute (re)builds the BM25 statistics from Chunks.
func (idx *Index) compute() {
	idx.termFreq = make([]map[string]int, len(idx.Chunks))
	idx.docFreq = map[string]int{}
	idx.docLen = make([]int, len(idx.Chunks))
	total := 0
	for i, c := range idx.Chunks {
		tf := map[string]int{}
		toks := Tokenize(c.Text)
		for _, t := range toks {
			tf[t]++
		}
		idx.termFreq[i] = tf
		idx.docLen[i] = len(toks)
		total += len(toks)
		for term := range tf {
			idx.docFreq[term]++
		}
	}
	if len(idx.Chunks) > 0 {
		idx.avgDocLen = float64(total) / float64(len(idx.Chunks))
	}
}

// Len reports the number of indexed chunks.
func (idx *Index) Len() int { return len(idx.Chunks) }

// Sources returns the distinct file paths in the index, in first-seen order.
func (idx *Index) Sources() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range idx.Chunks {
		if !seen[c.Path] {
			seen[c.Path] = true
			out = append(out, c.Path)
		}
	}
	return out
}

// Search returns up to topK chunks scored by BM25 against the query, best
// first. Chunks with no matching term are omitted. Ties break by path then
// start line so results are deterministic.
func (idx *Index) Search(query string, topK int) []Result {
	terms := Tokenize(query)
	if len(terms) == 0 || len(idx.Chunks) == 0 {
		return nil
	}
	// Deduplicate query terms; keep query-term set for idf.
	qset := map[string]bool{}
	for _, t := range terms {
		qset[t] = true
	}
	n := float64(len(idx.Chunks))

	var results []Result
	for i, c := range idx.Chunks {
		var score float64
		var matched []string
		for term := range qset {
			tf := idx.termFreq[i][term]
			if tf == 0 {
				continue
			}
			df := idx.docFreq[term]
			// BM25 idf with the standard +0.5 smoothing, floored at 0 so a
			// term present in most chunks never pushes a score negative.
			idf := math.Log((n-float64(df)+0.5)/(float64(df)+0.5) + 1)
			denom := float64(tf) + bm25K1*(1-bm25B+bm25B*float64(idx.docLen[i])/nonZero(idx.avgDocLen))
			score += idf * (float64(tf) * (bm25K1 + 1)) / denom
			matched = append(matched, term)
		}
		if score > 0 {
			results = append(results, Result{Chunk: c, Score: score, MatchedTerms: matched})
		}
	}
	sortResults(results)
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	return results
}

func nonZero(f float64) float64 {
	if f == 0 {
		return 1
	}
	return f
}

// sortResults orders by descending score, then path, then start line so
// output is stable across runs.
func sortResults(rs []Result) {
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Score != rs[j].Score {
			return rs[i].Score > rs[j].Score
		}
		if rs[i].Chunk.Path != rs[j].Chunk.Path {
			return rs[i].Chunk.Path < rs[j].Chunk.Path
		}
		return rs[i].Chunk.StartLine < rs[j].Chunk.StartLine
	})
	for i := range rs {
		sort.Strings(rs[i].MatchedTerms)
	}
}

// tokenPattern splits text into lowercase alphanumeric terms. Underscores
// split identifiers (read_file -> read, file) so a query for "read file"
// matches snake_case code.
var tokenPattern = regexp.MustCompile(`[A-Za-z0-9]+`)

// Tokenize lowercases text and extracts alphanumeric terms of length >= 2.
// Single characters and pure punctuation are dropped.
func Tokenize(s string) []string {
	raw := tokenPattern.FindAllString(strings.ToLower(s), -1)
	out := raw[:0]
	for _, t := range raw {
		if len(t) >= 2 {
			out = append(out, t)
		}
	}
	return out
}
