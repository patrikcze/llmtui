package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/web"
)

// researchWeb is a concurrency-safe WebClient fake keyed by query and URL.
type researchWeb struct {
	mu        sync.Mutex
	results   map[string][]web.SearchResult
	searchErr map[string]error
	pages     map[string]web.Page
	fetchErr  map[string]error
	searched  []string
	fetched   []string
	// barrier, when set, makes every Fetch wait until that many fetches are
	// in flight at once, proving the pages are read concurrently.
	barrier *sync.WaitGroup
}

func (w *researchWeb) Search(ctx context.Context, query string, max int) ([]web.SearchResult, error) {
	w.mu.Lock()
	w.searched = append(w.searched, query)
	results, err := w.results[query], w.searchErr[query]
	w.mu.Unlock()
	return results, err
}

func (w *researchWeb) Fetch(ctx context.Context, rawURL string) (web.Page, error) {
	w.mu.Lock()
	w.fetched = append(w.fetched, rawURL)
	page, err := w.pages[rawURL], w.fetchErr[rawURL]
	w.mu.Unlock()
	if w.barrier != nil {
		w.barrier.Done()
		done := make(chan struct{})
		go func() { w.barrier.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			return web.Page{}, ctx.Err()
		}
	}
	page.URL = rawURL
	page.Status = 200
	return page, err
}

const goReleasePage = "Navigation\n\n" +
	"Go 1.27 was released on August 12, 2026. The release adds a faster garbage collector and new iterator helpers.\n\n" +
	"Unrelated paragraph about the history of the gopher mascot and conference talks, kept long enough to count.\n\n" +
	"To install the Go 1.27 release, download the archive for your platform from the official downloads page."

func TestWebResearchReadsFusedSourcesIntoCitedNotes(t *testing.T) {
	fake := &researchWeb{
		results: map[string][]web.SearchResult{
			"go 1.27 release date": {
				{Title: "Go 1.27 Release Notes", URL: "https://go.dev/doc/go1.27", Snippet: "release notes"},
				{Title: "Blog", URL: "https://blog.example/go-127", Snippet: "blog snippet"},
			},
			"golang latest version": {
				{Title: "Go 1.27 again", URL: "https://go.dev/doc/go1.27/?utm_source=x"},
				{Title: "Broken", URL: "https://broken.example/", Snippet: "the broken page said 1.27"},
			},
		},
		pages: map[string]web.Page{
			"https://go.dev/doc/go1.27":   {Content: goReleasePage},
			"https://blog.example/go-127": {Content: goReleasePage},
		},
		fetchErr: map[string]error{"https://broken.example/": errors.New("status 503")},
	}
	call := Call{Tool: ToolWebResearch, ID: "call_1", Body: "When was the latest Go release?", Max: 3}
	setWebQueries(&call, []string{"go 1.27 release date", "golang latest version"})
	res := webRunner(t, fake).Execute(call)
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	out := res.Output
	for _, want := range []string{
		untrustedWebPreamble,
		"LLMTUI_UNTRUSTED_BEGIN",
		"Research notes for: When was the latest Go release?",
		`Searched: "go 1.27 release date"; "golang latest version"`,
		"Sources read: 2 of 3",
		"[1] Go 1.27 Release Notes - https://go.dev/doc/go1.27",
		"Go 1.27 was released on August 12, 2026.",
		"[3] Broken - https://broken.example/",
		"(could not be read: status 503)",
		"Search snippet: the broken page said 1.27",
		webResearchGuidance,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "gopher mascot") {
		t.Errorf("irrelevant passage was kept:\n%s", out)
	}
	if strings.Index(out, webResearchGuidance) < strings.Index(out, "LLMTUI_UNTRUSTED_END") {
		t.Error("tool guidance must follow the untrusted frame, not sit inside it")
	}
	if len(fake.fetched) != 3 {
		t.Errorf("fetched %q, want 3 de-duplicated pages", fake.fetched)
	}
	if res.Meta.Outcome != OutcomePartial {
		t.Errorf("outcome = %q, want partial when a source failed", res.Meta.Outcome)
	}
	if len(res.Entities) != 2 {
		t.Fatalf("entities = %d, want one per read source", len(res.Entities))
	}
	for _, candidate := range res.Entities {
		if candidate.Kind != entity.KindWebResult || candidate.Trust != entity.TrustWebUntrusted ||
			candidate.Provenance.Operation != ToolWebResearch || candidate.Provenance.CallID != "call_1" {
			t.Errorf("candidate = %+v", candidate)
		}
	}
	if len(res.Captures) != 1 || res.Captures[0].Trust != entity.TrustWebUntrusted {
		t.Errorf("captures = %+v", res.Captures)
	}
}

func TestWebResearchFetchesPagesConcurrently(t *testing.T) {
	var barrier sync.WaitGroup
	barrier.Add(3)
	fake := &researchWeb{
		results: map[string][]web.SearchResult{"q": {
			{Title: "A", URL: "https://a.example/"},
			{Title: "B", URL: "https://b.example/"},
			{Title: "C", URL: "https://c.example/"},
		}},
		pages: map[string]web.Page{
			"https://a.example/": {Content: goReleasePage},
			"https://b.example/": {Content: goReleasePage},
			"https://c.example/": {Content: goReleasePage},
		},
		barrier: &barrier,
	}
	r := webRunner(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	call := Call{Tool: ToolWebResearch, Body: "go release", Max: 3}
	setWebQueries(&call, []string{"q"})
	// Sequential fetches would wait on the barrier forever and only finish
	// when the context expires, so a clean result proves concurrency.
	res := r.ExecuteContext(ctx, call)
	if res.Err != nil {
		t.Fatalf("ExecuteContext: %v", res.Err)
	}
	if !strings.Contains(res.Output, "Sources read: 3 of 3") {
		t.Fatalf("output:\n%s", res.Output)
	}
}

func TestWebResearchDefaultsAndCaps(t *testing.T) {
	var results []web.SearchResult
	pages := map[string]web.Page{}
	for _, host := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		u := "https://" + host + ".example/"
		results = append(results, web.SearchResult{Title: host, URL: u})
		pages[u] = web.Page{Content: goReleasePage}
	}
	tests := []struct {
		name      string
		max       int
		wantFetch int
	}{
		{name: "default reads four", max: 0, wantFetch: web.ResearchDefaultSources},
		{name: "explicit two", max: 2, wantFetch: 2},
		{name: "maximum six", max: 6, wantFetch: web.ResearchMaxSources},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &researchWeb{results: map[string][]web.SearchResult{"go release": results}, pages: pages}
			res := webRunner(t, fake).Execute(Call{Tool: ToolWebResearch, Body: "go release", Max: tt.max})
			if res.Err != nil {
				t.Fatalf("Execute: %v", res.Err)
			}
			if len(fake.searched) != 1 || fake.searched[0] != "go release" {
				t.Errorf("searched %q, want the question as the only query", fake.searched)
			}
			if len(fake.fetched) != tt.wantFetch {
				t.Errorf("fetched %d pages, want %d", len(fake.fetched), tt.wantFetch)
			}
		})
	}
	res := webRunner(t, &researchWeb{}).Execute(Call{Tool: ToolWebResearch, Body: "q", Max: 7})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "max_sources") {
		t.Errorf("max_sources 7: err = %v", res.Err)
	}
}

func TestWebResearchSearchFailures(t *testing.T) {
	t.Run("every search failing is a network error", func(t *testing.T) {
		fake := &researchWeb{searchErr: map[string]error{"q": web.ErrRateLimited}}
		res := webRunner(t, fake).Execute(Call{Tool: ToolWebResearch, Body: "q"})
		if res.Err == nil || !errors.Is(res.Err, web.ErrRateLimited) {
			t.Fatalf("err = %v, want rate-limit error", res.Err)
		}
	})
	t.Run("one failing query is reported, the rest still read", func(t *testing.T) {
		fake := &researchWeb{
			results:   map[string][]web.SearchResult{"good": {{Title: "A", URL: "https://a.example/"}}},
			searchErr: map[string]error{"bad": errors.New("search failed: status 500")},
			pages:     map[string]web.Page{"https://a.example/": {Content: goReleasePage}},
		}
		call := Call{Tool: ToolWebResearch, Body: "go release"}
		setWebQueries(&call, []string{"good", "bad"})
		res := webRunner(t, fake).Execute(call)
		if res.Err != nil {
			t.Fatalf("Execute: %v", res.Err)
		}
		if !strings.Contains(res.Output, `Searches that failed: "bad" (search failed: status 500)`) || !strings.Contains(res.Output, "Sources read: 1 of 1") {
			t.Fatalf("output:\n%s", res.Output)
		}
	})
	t.Run("no results is a clean negative", func(t *testing.T) {
		res := webRunner(t, &researchWeb{}).Execute(Call{Tool: ToolWebResearch, Body: "nothing"})
		if res.Err != nil || !strings.Contains(res.Output, `No search results for: "nothing"`) {
			t.Fatalf("res = %+v", res)
		}
	})
}

func TestWebResearchRequiresWebAndQuestion(t *testing.T) {
	res := NewRunner(t.TempDir(), 64).Execute(Call{Tool: ToolWebResearch, Body: "q"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "disabled") {
		t.Errorf("web off: err = %v", res.Err)
	}
	res = webRunner(t, &researchWeb{}).Execute(Call{Tool: ToolWebResearch, Body: "  "})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "question") {
		t.Errorf("empty question: err = %v", res.Err)
	}
}

func TestWebResearchNativeArguments(t *testing.T) {
	tests := []struct {
		name        string
		args        string
		wantErr     string
		wantBody    string
		wantQueries []string
		wantMax     int
	}{
		{name: "full", args: `{"question":"Q?","queries":["a","b","A"],"max_sources":5}`, wantBody: "Q?", wantQueries: []string{"a", "b"}, wantMax: 5},
		{name: "single query string", args: `{"question":"Q?","queries":"only one"}`, wantBody: "Q?", wantQueries: []string{"only one"}},
		{name: "max_sources as string", args: `{"question":"Q?","max_sources":"3"}`, wantBody: "Q?", wantMax: 3},
		{name: "queries capped at three", args: `{"question":"Q?","queries":["a","b","c","d"]}`, wantBody: "Q?", wantQueries: []string{"a", "b", "c"}},
		{name: "unknown key rejected", args: `{"question":"Q?","query":"x"}`, wantErr: "unknown field"},
		{name: "missing question", args: `{"queries":["a"]}`, wantErr: "needs a question"},
		{name: "bad queries type", args: `{"question":"Q?","queries":7}`, wantErr: "array of strings"},
		{name: "bad max_sources", args: `{"question":"Q?","max_sources":"many"}`, wantErr: "max_sources"},
		{name: "oversized", args: `{"question":"` + strings.Repeat("x", maxWebResearchPayloadBytes) + `"}`, wantErr: "byte limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: ToolWebResearch, Arguments: tt.args}})
			if len(calls) != 1 {
				t.Fatalf("calls = %+v", calls)
			}
			c := calls[0]
			if tt.wantErr != "" {
				if !strings.Contains(c.InputErr, tt.wantErr) {
					t.Fatalf("InputErr = %q, want %q", c.InputErr, tt.wantErr)
				}
				return
			}
			if c.InputErr != "" || c.Tool != ToolWebResearch || c.Body != tt.wantBody || c.Max != tt.wantMax {
				t.Fatalf("call = %+v", c)
			}
			got := c.WebQueries[:c.WebQueryCount]
			if strings.Join(got, "|") != strings.Join(tt.wantQueries, "|") {
				t.Fatalf("queries = %q, want %q", got, tt.wantQueries)
			}
		})
	}
}

func TestWebResearchFencedForms(t *testing.T) {
	calls := Parse("```tool web_research\nWhat is new in Go 1.27?\n```\n" +
		"```tool web_research\n{\"question\":\"Q?\",\"queries\":[\"x\",\"y\"],\"max_sources\":2}\n```\n")
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Body != "What is new in Go 1.27?" || calls[0].WebQueryCount != 0 {
		t.Errorf("plain form = %+v", calls[0])
	}
	if calls[1].Body != "Q?" || calls[1].Max != 2 || calls[1].WebQueryCount != 2 || calls[1].WebQueries[1] != "y" {
		t.Errorf("JSON form = %+v", calls[1])
	}
}

func TestWebResearchAlwaysNeedsApprovalAndDescribesQueries(t *testing.T) {
	r := webRunner(t, &researchWeb{})
	call := Call{Tool: ToolWebResearch, Body: "short question"}
	if !r.NeedsApproval(call) {
		t.Fatal("web_research fetches pages and must ask, like web_fetch")
	}
	setWebQueries(&call, []string{"first query", "second query"})
	got := call.Describe()
	for _, want := range []string{`"short question"`, `"first query"`, `"second query"`, "up to 4 pages"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe() = %q, missing %q", got, want)
		}
	}
}
