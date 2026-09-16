package rag

import (
	"fmt"
	"testing"
	"time"
)

// retrievalQualityFixture is a deterministic repository-shaped fixture. The
// required/useful/irrelevant labels are independent ground truth; no model is
// used to judge whether a result is relevant.
type retrievalQualityFixture struct {
	name           string
	query          string
	requiredFiles  []string
	usefulFiles    []string
	irrelevant     []string
	requiredChunks []string
	chunks         []DocumentChunk
}

type retrievalQualityMetrics struct {
	recallFile    map[int]float64
	precisionFile map[int]float64
	recallChunk   map[int]float64
	irrelevant    map[int]int
	overlaps      int
	tokens        int
	elapsed       time.Duration
}

func TestRetrievalQualityMatrix(t *testing.T) {
	for _, fixture := range retrievalQualityFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			index := NewIndex(fixture.chunks)
			metrics := measureRetrievalQuality(index, fixture)
			t.Logf("recall_file@1=%.2f @3=%.2f @5=%.2f @10=%.2f precision_file@1=%.2f @3=%.2f @5=%.2f @10=%.2f recall_chunk@1=%.2f @3=%.2f @5=%.2f @10=%.2f irrelevant@10=%d overlaps@10=%d context_tokens@10=%d latency=%s",
				metrics.recallFile[1], metrics.recallFile[3], metrics.recallFile[5], metrics.recallFile[10],
				metrics.precisionFile[1], metrics.precisionFile[3], metrics.precisionFile[5], metrics.precisionFile[10],
				metrics.recallChunk[1], metrics.recallChunk[3], metrics.recallChunk[5], metrics.recallChunk[10],
				metrics.irrelevant[10], metrics.overlaps, metrics.tokens, metrics.elapsed)
			if len(fixture.requiredFiles) == 0 || len(fixture.requiredChunks) == 0 {
				t.Fatal("fixture must define independent file and chunk ground truth")
			}
			for _, k := range []int{1, 3, 5, 10} {
				if metrics.recallFile[k] < metrics.recallFile[1] {
					t.Fatalf("file recall decreased at @%d", k)
				}
				if metrics.recallChunk[k] < metrics.recallChunk[1] {
					t.Fatalf("chunk recall decreased at @%d", k)
				}
			}
		})
	}
}

func BenchmarkRetrievalQuality(b *testing.B) {
	for _, fixture := range retrievalQualityFixtures() {
		fixture := fixture
		b.Run(fixture.name, func(b *testing.B) {
			index := NewIndex(fixture.chunks)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_ = index.Search(fixture.query, 10)
			}
		})
	}
}

func measureRetrievalQuality(index *Index, fixture retrievalQualityFixture) retrievalQualityMetrics {
	started := time.Now()
	results := index.Search(fixture.query, 10)
	metrics := retrievalQualityMetrics{
		recallFile: map[int]float64{}, precisionFile: map[int]float64{},
		recallChunk: map[int]float64{}, irrelevant: map[int]int{},
		elapsed: time.Since(started),
	}
	requiredFiles := stringSet(fixture.requiredFiles)
	relevantFiles := stringSet(append(append([]string{}, fixture.requiredFiles...), fixture.usefulFiles...))
	irrelevantFiles := stringSet(fixture.irrelevant)
	requiredChunks := stringSet(fixture.requiredChunks)
	for _, k := range []int{1, 3, 5, 10} {
		selected := results
		if len(selected) > k {
			selected = selected[:k]
		}
		seenRequiredFiles := map[string]bool{}
		seenRequiredChunks := map[string]bool{}
		relevant := 0
		irrelevant := 0
		for _, result := range selected {
			path := result.Chunk.Path
			if requiredFiles[path] {
				seenRequiredFiles[path] = true
			}
			if requiredChunks[result.Chunk.ID] {
				seenRequiredChunks[result.Chunk.ID] = true
			}
			if relevantFiles[path] {
				relevant++
			}
			if irrelevantFiles[path] {
				irrelevant++
			}
		}
		metrics.recallFile[k] = ratio(len(seenRequiredFiles), len(requiredFiles))
		metrics.precisionFile[k] = ratio(relevant, len(selected))
		metrics.recallChunk[k] = ratio(len(seenRequiredChunks), len(requiredChunks))
		metrics.irrelevant[k] = irrelevant
	}
	metrics.overlaps = overlappingResults(results)
	for _, result := range results {
		metrics.tokens += len(Tokenize(result.Chunk.Text))
	}
	return metrics
}

func overlappingResults(results []Result) int {
	overlaps := 0
	for i := range results {
		for j := i + 1; j < len(results); j++ {
			left, right := results[i].Chunk, results[j].Chunk
			if left.Path == right.Path && left.StartLine <= right.EndLine && right.StartLine <= left.EndLine {
				overlaps++
			}
		}
	}
	return overlaps
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func retrievalQualityFixtures() []retrievalQualityFixture {
	return []retrievalQualityFixture{
		{
			name:          "lexically_obvious",
			query:         "cache eviction policy",
			requiredFiles: []string{"internal/cache/cache.go"}, usefulFiles: []string{"docs/cache.md"},
			irrelevant: []string{"internal/metrics/metrics.go"}, requiredChunks: []string{"internal/cache/cache.go#1-4"},
			chunks: []DocumentChunk{
				fixtureChunk("internal/cache/cache.go", 1, 4, "func EvictExpiredEntries() { cache eviction removes stale entries by policy }"),
				fixtureChunk("docs/cache.md", 1, 4, "The cache eviction policy removes expired entries and bounds retained values."),
				fixtureChunk("internal/metrics/metrics.go", 1, 4, "Request counters and latency histograms are exported for observability."),
			},
		},
		{
			name:          "structural_relationship",
			query:         "where does the program admit a filesystem change",
			requiredFiles: []string{"internal/tools/runner.go"}, usefulFiles: []string{"docs/tool-execution.md"},
			irrelevant: []string{"internal/tools/discovery.go"}, requiredChunks: []string{"internal/tools/runner.go#1-4"},
			chunks: []DocumentChunk{
				fixtureChunk("internal/tools/runner.go", 1, 4, "The executor dispatches approved operations through the workspace policy gate; mutation is admitted only after permission."),
				fixtureChunk("internal/tools/runner.go", 3, 6, "A filesystem change reaches the dispatcher only after the policy permits the operation."),
				fixtureChunk("docs/tool-execution.md", 1, 4, "File writes require confirmation before the workspace can be changed."),
				fixtureChunk("internal/tools/discovery.go", 1, 4, "The workspace scanner lists files and directories without changing them."),
			},
		},
		{
			name:          "test_relationship",
			query:         "what test proves write protection before a file mutation",
			requiredFiles: []string{"internal/tools/runner_test.go"}, usefulFiles: []string{"internal/tools/runner.go"},
			irrelevant: []string{"internal/tools/read.go"}, requiredChunks: []string{"internal/tools/runner_test.go#1-4"},
			chunks: []DocumentChunk{
				fixtureChunk("internal/tools/runner_test.go", 1, 4, "TestWriteFileRequiresApproval proves write protection before a file mutation."),
				fixtureChunk("internal/tools/runner.go", 1, 4, "Write operations pass through the approval policy before execution."),
				fixtureChunk("internal/tools/read.go", 1, 4, "Read files and return their contents without performing writes."),
			},
		},
		{
			name:          "config_relationship",
			query:         "where is the verifier retry limit configured",
			requiredFiles: []string{"config/config.go"}, usefulFiles: []string{"config/default.yaml"},
			irrelevant: []string{"docs/agent-loop.md"}, requiredChunks: []string{"config/config.go#1-4"},
			chunks: []DocumentChunk{
				fixtureChunk("config/config.go", 1, 4, "Verifier MaxAttempts and MaxTokens are loaded from agent.verifier settings."),
				fixtureChunk("config/default.yaml", 1, 4, "agent verifier max_attempts sets the retry limit for failed checks."),
				fixtureChunk("docs/agent-loop.md", 1, 4, "The verifier observes evidence and reports whether criteria are satisfied."),
			},
		},
		{
			name:          "adr_relationship",
			query:         "why are visible tool markers not executed",
			requiredFiles: []string{"docs/adr/0004-untrusted-tool-text.md"}, usefulFiles: []string{"internal/provider/tool_diagnostics.go"},
			irrelevant: []string{"docs/terminal.md"}, requiredChunks: []string{"docs/adr/0004-untrusted-tool-text.md#1-4"},
			chunks: []DocumentChunk{
				fixtureChunk("docs/adr/0004-untrusted-tool-text.md", 1, 4, "Visible tool-like markers are diagnostic only and never executable because untrusted text cannot grant permission."),
				fixtureChunk("internal/provider/tool_diagnostics.go", 1, 4, "The parser classifies pseudo tool markers for diagnostics and does not create executable calls."),
				fixtureChunk("docs/terminal.md", 1, 4, "Terminal rendering removes control sequences from displayed output."),
			},
		},
	}
}

func fixtureChunk(path string, startLine, endLine int, text string) DocumentChunk {
	return DocumentChunk{ID: fmt.Sprintf("%s#%d-%d", path, startLine, endLine), Path: path, StartLine: startLine, EndLine: endLine, Text: text}
}
