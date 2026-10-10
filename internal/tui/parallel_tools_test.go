package tui

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/web"
)

// concurrencyWeb is a WebClient fake that records how many searches were in
// flight at once and, with a barrier, waits until that many have started.
type concurrencyWeb struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	barrier  *sync.WaitGroup
}

func (w *concurrencyWeb) Search(ctx context.Context, query string, max int) ([]web.SearchResult, error) {
	w.mu.Lock()
	w.inFlight++
	if w.inFlight > w.peak {
		w.peak = w.inFlight
	}
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.inFlight--
		w.mu.Unlock()
	}()
	if w.barrier != nil {
		w.barrier.Done()
		done := make(chan struct{})
		go func() { w.barrier.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []web.SearchResult{{Title: query, URL: "https://example.com/" + query}}, nil
}

func (w *concurrencyWeb) Fetch(ctx context.Context, rawURL string) (web.Page, error) {
	return web.Page{URL: rawURL}, nil
}

func TestRunPlannedToolBatchRunsReadOnlyCallsConcurrentlyInOrder(t *testing.T) {
	var barrier sync.WaitGroup
	barrier.Add(3)
	fake := &concurrencyWeb{barrier: &barrier}
	runner := tools.NewRunner(t.TempDir(), 64)
	runner.Web = fake
	calls := []tools.Call{
		{ID: "a", Tool: tools.ToolWebSearch, Body: "alpha"},
		{ID: "b", Tool: tools.ToolWebSearch, Body: "bravo"},
		{ID: "c", Tool: tools.ToolWebSearch, Body: "charlie"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Sequential execution would wait on the barrier until the context
	// expires, so three clean results prove the calls overlapped.
	msg, ok := runMixedToolBatch(ctx, runner, nil, calls)().(mcpToolResultsMsg)
	if !ok || len(msg.results) != 3 {
		t.Fatalf("results = %+v", msg)
	}
	for i, id := range []string{"a", "b", "c"} {
		if msg.results[i].Call.ID != id || msg.results[i].Err != nil {
			t.Errorf("result %d = %+v, want call %s in model order", i, msg.results[i], id)
		}
	}
}

func TestRunPlannedToolBatchNeverOverlapsAnEffect(t *testing.T) {
	fake := &concurrencyWeb{}
	runner := tools.NewRunner(t.TempDir(), 64)
	runner.Web = fake
	calls := []tools.Call{
		{ID: "a", Tool: tools.ToolWebSearch, Body: "alpha"},
		{ID: "w", Tool: tools.ToolWriteFile, Path: "notes.txt", Body: "x"},
		{ID: "b", Tool: tools.ToolWebSearch, Body: "bravo"},
	}
	msg, ok := runMixedToolBatch(context.Background(), runner, nil, calls)().(mcpToolResultsMsg)
	if !ok || len(msg.results) != 3 {
		t.Fatalf("results = %+v", msg)
	}
	for i, id := range []string{"a", "w", "b"} {
		if msg.results[i].Call.ID != id || msg.results[i].Err != nil {
			t.Errorf("result %d = %+v, want call %s in model order", i, msg.results[i], id)
		}
	}
	if fake.peak != 1 {
		t.Errorf("peak concurrent searches = %d; reads separated by a write must not overlap", fake.peak)
	}
}

func TestRunConcurrentlyRespectsTheCap(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	ran := make([]bool, 10)
	runConcurrently(0, len(ran), func(i int) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		inFlight--
		ran[i] = true
		mu.Unlock()
	})
	if peak > maxConcurrentToolCalls {
		t.Fatalf("peak = %d, want at most %d", peak, maxConcurrentToolCalls)
	}
	for i, done := range ran {
		if !done {
			t.Fatalf("call %d did not run", i)
		}
	}
}
