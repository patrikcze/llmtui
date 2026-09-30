package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

func writeNumberedLines(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %04d: the quick brown fox jumps over the lazy dog\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func boundedTestRead(t *testing.T, root, args string) tools.Result {
	t.Helper()
	calls := tools.CallsFromNative([]provider.ToolCall{{ID: "r", Name: tools.ToolReadFile, Arguments: args}})
	if len(calls) != 1 || calls[0].InputErr != "" {
		t.Fatalf("decode %s: %+v", args, calls)
	}
	return tools.NewRunner(root, 64).Execute(calls[0])
}

// TestOversizedReadBatchCompletesInSmallWindow is plan step 5's S4b as a
// regular test: three 120-line reads in one batch on the 8k fallback window
// used to fail the turn ("estimated request is … tokens but only 7680 are
// available"). The newest results are cut to fit, and the turn continues.
func TestOversizedReadBatchCompletesInSmallWindow(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{
			{ID: "a", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt"}`},
			{ID: "b", Name: tools.ToolReadFile, Arguments: `{"path":"b.txt"}`},
			{ID: "c", Name: tools.ToolReadFile, Arguments: `{"path":"c.txt"}`},
		}},
		agentScriptStep{text: "Summaries of a.txt, b.txt and c.txt."},
	)
	m.agentOn = false
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeNumberedLines(t, filepath.Join(root, name+".txt"), 120)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	if window, _ := m.contextWindow(); window > 8192 {
		t.Fatalf("test assumes the 8k fallback window, got %d", window)
	}

	driveAgentCommands(t, m, m.dispatch("Summarize a.txt, b.txt and c.txt.", nil))

	if m.errText != "" {
		t.Fatalf("turn failed: %s", m.errText)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (the batch, then its continuation)", len(prov.requests))
	}
	truncated := 0
	for _, msg := range m.session.Messages {
		if msg.Role == provider.RoleTool && strings.Contains(msg.Content, "truncated to fit the context window") {
			truncated++
		}
	}
	if truncated == 0 {
		t.Fatal("no result was cut, yet the batch did not fit before")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleAssistant || !strings.Contains(last.Content, "Summaries") {
		t.Fatalf("last message = %+v, want the model's answer", last)
	}
}

// TestBoundedReadRecordsOnlyDeliveredLines: a whole-file read cut to fit is
// rewritten to a line window of what was kept, so read coverage can never
// claim the lines that were cut.
func TestBoundedReadRecordsOnlyDeliveredLines(t *testing.T) {
	root := t.TempDir()
	writeNumberedLines(t, filepath.Join(root, "big.txt"), 120)
	full := boundedTestRead(t, root, `{"path":"big.txt"}`)
	if ob, ok := readObservationFromResult(full); !ok || ob.EndLine != 120 {
		t.Fatalf("full read observation = %+v, want lines 1-120", ob)
	}
	bounded, ok := boundResult(full, 20*56+30)
	if !ok {
		t.Fatal("result was not cut")
	}
	ob, ok := readObservationFromResult(bounded)
	if !ok || ob.StartLine != 1 || ob.EndLine != 20 || ob.TotalLines == nil || *ob.TotalLines != 120 {
		t.Fatalf("bounded observation = %+v, want lines 1-20 of 120", ob)
	}
	var execution agent.ExecutionResult
	agent.AppendReadObservation(&execution, ob)
	if covered, _ := agent.ReadCoverage("big.txt", execution.ReadObservations); covered >= 120 {
		t.Fatalf("coverage = %d lines, want less than the whole file", covered)
	}
	if !strings.Contains(bounded.Output, "line 0020:") || strings.Contains(bounded.Output, "line 0021:") {
		t.Fatalf("kept lines wrong:\n%s", bounded.Output)
	}
	for _, want := range []string{"lines 1-20 of 120", "next_offset=21", "reread with offset=21"} {
		if !strings.Contains(bounded.Output, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if bounded.Meta.Outcome != tools.OutcomePartial || bounded.Meta.Coverage.PreviewComplete {
		t.Fatalf("meta = %+v, want a partial, incomplete preview", bounded.Meta)
	}
}

// TestBoundedReadRewritesRangedWindow: a ranged read keeps its start line
// and its header is rewritten to the kept range, not left claiming more.
func TestBoundedReadRewritesRangedWindow(t *testing.T) {
	root := t.TempDir()
	writeNumberedLines(t, filepath.Join(root, "big.txt"), 200)
	full := boundedTestRead(t, root, `{"path":"big.txt","offset":101,"limit":50}`)
	if full.Meta.Window == nil || full.Meta.Window.StartLine != 101 || full.Meta.Window.EndLine != 150 {
		t.Fatalf("unexpected source window %+v", full.Meta.Window)
	}
	bounded, ok := boundResult(full, 120+10*56)
	if !ok {
		t.Fatal("result was not cut")
	}
	w := bounded.Meta.Window
	if w == nil || w.StartLine != 101 || w.EndLine <= 101 || w.EndLine >= 150 || w.NextOffset == nil || *w.NextOffset != w.EndLine+1 {
		t.Fatalf("bounded window = %+v", w)
	}
	if strings.Contains(bounded.Output, "lines 101-150") {
		t.Fatalf("header still claims the full range:\n%s", bounded.Output)
	}
	if ob, ok := readObservationFromResult(bounded); !ok || ob.StartLine != 101 || ob.EndLine != w.EndLine {
		t.Fatalf("observation = %+v, want the kept window", ob)
	}
}

// TestBoundedResultEdgeCases covers the remaining cut shapes.
func TestBoundedResultEdgeCases(t *testing.T) {
	root := t.TempDir()
	writeNumberedLines(t, filepath.Join(root, "big.txt"), 120)

	t.Run("no complete line fits: no coverage at all", func(t *testing.T) {
		bounded, ok := boundResult(boundedTestRead(t, root, `{"path":"big.txt"}`), 10)
		if !ok {
			t.Fatal("result was not cut")
		}
		if _, observed := readObservationFromResult(bounded); observed {
			t.Fatal("a read that delivered no line must not record coverage")
		}
	})
	t.Run("file version is no longer complete", func(t *testing.T) {
		r := boundedTestRead(t, root, `{"path":"big.txt"}`)
		r.Meta.FileVersion = &entity.FileVersion{Path: "big.txt", Digest: "d", Complete: true}
		bounded, _ := boundResult(r, 600)
		if bounded.Meta.FileVersion == nil || bounded.Meta.FileVersion.Complete || !r.Meta.FileVersion.Complete {
			t.Fatal("the cut copy must be incomplete and the original untouched")
		}
	})
	t.Run("other tools keep a head and the marker", func(t *testing.T) {
		r := tools.Result{Call: tools.Call{ID: "c", Tool: tools.ToolRunCommand}, Output: strings.Repeat("output line\n", 400)}
		bounded, ok := boundResult(r, 500)
		if !ok || len(bounded.Output) > 600 || !strings.HasSuffix(bounded.Output, toolResultTruncationMarker) {
			t.Fatalf("bounded = %q", bounded.Output)
		}
	})
	t.Run("errors and small results are never cut", func(t *testing.T) {
		results := []tools.Result{
			{Call: tools.Call{ID: "e", Tool: tools.ToolReadFile}, Err: os.ErrNotExist, Output: strings.Repeat("x", 4000)},
			{Call: tools.Call{ID: "s", Tool: tools.ToolReadFile}, Output: "tiny"},
		}
		if _, changed := cutNewestResults(results, 100000); changed {
			t.Fatal("an error result or a result under the minimum head was cut")
		}
	})
}

// TestFittingResultsAreUnchanged: bounding is a no-op when the continuation
// already fits, and for fenced-protocol results.
func TestFittingResultsAreUnchanged(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	writeNumberedLines(t, filepath.Join(root, "small.txt"), 5)
	m.toolsOn, m.toolsNative = true, true
	m.toolRunner = tools.NewRunner(root, 64)
	m.session.AddUser("read small.txt")
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "r", Name: tools.ToolReadFile, Arguments: `{"path":"small.txt"}`}}})
	r := boundedTestRead(t, root, `{"path":"small.txt"}`)
	before := len(m.session.Messages)
	if got := m.boundToolResultsToBudget([]tools.Result{r}); got[0].Output != r.Output {
		t.Fatal("a fitting result was changed")
	}
	if len(m.session.Messages) != before {
		t.Fatal("estimating left messages in the session")
	}
	huge := r
	huge.Call.ID = "" // fenced protocol
	huge.Output = strings.Repeat("x", 200000)
	if got := m.boundToolResultsToBudget([]tools.Result{huge}); got[0].Output != huge.Output {
		t.Fatal("a fenced-protocol result was cut")
	}
}
