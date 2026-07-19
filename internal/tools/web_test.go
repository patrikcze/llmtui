package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/web"
)

type stubWeb struct {
	results []web.SearchResult
	page    web.Page
	err     error
	gotMax  int
	gotURL  string
}

func (s *stubWeb) Search(ctx context.Context, q string, max int) ([]web.SearchResult, error) {
	s.gotMax = max
	return s.results, s.err
}

func (s *stubWeb) Fetch(ctx context.Context, u string) (web.Page, error) {
	s.gotURL = u
	return s.page, s.err
}

func webRunner(t *testing.T, w WebClient) *Runner {
	t.Helper()
	r := NewRunner(t.TempDir(), 64)
	r.Web = w
	r.WebMaxResults = 5
	return r
}

func TestWebToolsDisabled(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	for _, c := range []Call{{Tool: ToolWebSearch, Body: "q"}, {Tool: ToolWebFetch, Path: "https://example.com"}} {
		res := r.Execute(c)
		if res.Err == nil || !strings.Contains(res.Err.Error(), "disabled") {
			t.Errorf("%s: want disabled error, got %v", c.Tool, res.Err)
		}
	}
}

func TestWebSearchFormatsResults(t *testing.T) {
	stub := &stubWeb{results: []web.SearchResult{
		{Title: "First", URL: "https://a.example", Snippet: "alpha"},
		{Title: "Second", URL: "https://b.example", Snippet: "beta"},
	}}
	res := webRunner(t, stub).Execute(Call{Tool: ToolWebSearch, Body: "some query"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	first := strings.Split(res.Output, "\n")[0]
	if first != `2 results for "some query"` {
		t.Errorf("first line %q", first)
	}
	if !strings.Contains(res.Output, "1. First — https://a.example") || !strings.Contains(res.Output, "alpha") {
		t.Errorf("output:\n%s", res.Output)
	}
}

func TestWebSearchClampsMax(t *testing.T) {
	stub := &stubWeb{}
	r := webRunner(t, stub)
	r.Execute(Call{Tool: ToolWebSearch, Body: "q", Max: 50})
	if stub.gotMax != 5 {
		t.Errorf("max=%d, want clamp to 5", stub.gotMax)
	}
	r.Execute(Call{Tool: ToolWebSearch, Body: "q", Max: 2})
	if stub.gotMax != 2 {
		t.Errorf("max=%d, want 2", stub.gotMax)
	}
}

func TestWebSearchEmptyQueryAndNoResults(t *testing.T) {
	stub := &stubWeb{}
	r := webRunner(t, stub)
	if res := r.Execute(Call{Tool: ToolWebSearch}); res.Err == nil {
		t.Error("empty query must error")
	}
	if res := r.Execute(Call{Tool: ToolWebSearch, Body: "q"}); res.Err != nil || !strings.Contains(res.Output, "no results") {
		t.Errorf("want 'no results', got %q err %v", res.Output, res.Err)
	}
}

func TestWebFetchFormatsPage(t *testing.T) {
	stub := &stubWeb{page: web.Page{URL: "https://a.example/x", Content: "# Doc\nbody", Bytes: 46284, Status: 200}}
	res := webRunner(t, stub).Execute(Call{Tool: ToolWebFetch, Path: "https://a.example/x"})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	first := strings.Split(res.Output, "\n")[0]
	if first != "fetched https://a.example/x — 45.2 KB, status 200" {
		t.Errorf("first line %q", first)
	}
	if !strings.Contains(res.Output, "# Doc") {
		t.Errorf("content missing:\n%s", res.Output)
	}
}

func TestWebResultsSanitizeTerminalControlSequences(t *testing.T) {
	stub := &stubWeb{
		results: []web.SearchResult{{Title: "safe\x1b]0;title\x07", URL: "https://example.com", Snippet: "clip\x1b]52;c;YQ==\x07"}},
		page:    web.Page{URL: "https://example.com", Content: "body\x1b[2J"},
	}
	for _, res := range []Result{
		webRunner(t, stub).Execute(Call{Tool: ToolWebSearch, Body: "q"}),
		webRunner(t, stub).Execute(Call{Tool: ToolWebFetch, Path: "https://example.com"}),
	} {
		if strings.ContainsRune(res.Output, '\x1b') || strings.ContainsRune(res.Output, '\x07') || strings.Contains(res.Output, "YQ==") {
			t.Fatalf("terminal sequence survived web tool result: %q", res.Output)
		}
	}
}

func TestWebFetchErrorKeepsBody(t *testing.T) {
	stub := &stubWeb{page: web.Page{Status: 404, Content: "gone"}, err: errors.New("fetch failed: status 404")}
	res := webRunner(t, stub).Execute(Call{Tool: ToolWebFetch, Path: "https://a.example/x"})
	if res.Err == nil || !strings.Contains(res.Output, "gone") {
		t.Errorf("want error with body, got out=%q err=%v", res.Output, res.Err)
	}
}

func TestWebNeedsApprovalAndDescribe(t *testing.T) {
	if NeedsApproval(Call{Tool: ToolWebSearch, Body: "q"}) {
		t.Error("web_search must auto-run")
	}
	if !NeedsApproval(Call{Tool: ToolWebFetch, Path: "https://a.example"}) {
		t.Error("web_fetch must need approval")
	}
	if d := (Call{Tool: ToolWebSearch, Body: "q"}).Describe(); d != `web_search("q")` {
		t.Errorf("describe search: %q", d)
	}
	if d := (Call{Tool: ToolWebFetch, Path: "https://a.example"}).Describe(); d != "fetch https://a.example" {
		t.Errorf("describe fetch: %q", d)
	}
}

func TestWebNativeMapping(t *testing.T) {
	calls := CallsFromNative([]provider.ToolCall{
		{Name: ToolWebSearch, Arguments: `{"query":"ollama bug","max_results":3}`},
		{Name: ToolWebFetch, Arguments: `{"url":"https://a.example/x"}`},
	})
	if calls[0].Body != "ollama bug" || calls[0].Max != 3 {
		t.Errorf("search call: %+v", calls[0])
	}
	if calls[1].Path != "https://a.example/x" {
		t.Errorf("fetch call: %+v", calls[1])
	}
}

func TestWebFencedParsing(t *testing.T) {
	reply := "```tool web_search\nollama bug\n```\n```tool web_fetch https://a.example/x\n```\n"
	calls := Parse(reply)
	if len(calls) != 2 || strings.TrimSpace(calls[0].Body) != "ollama bug" || calls[1].Path != "https://a.example/x" {
		t.Fatalf("calls: %+v", calls)
	}
}

func TestWebSpecs(t *testing.T) {
	specs := WebSpecs()
	if len(specs) != 2 || specs[0].Name != ToolWebSearch || specs[1].Name != ToolWebFetch {
		t.Fatalf("specs: %+v", specs)
	}
}

func TestSummarizeWebOutput(t *testing.T) {
	if s := SummarizeOutput("2 results for \"q\"\n\n1. A — https://a\n   alpha"); s != `2 results for "q"` {
		t.Errorf("search summary: %q", s)
	}
	if s := SummarizeOutput("fetched https://a — 45.2 KB, status 200\n\n# Doc"); s != "fetched https://a — 45.2 KB, status 200" {
		t.Errorf("fetch summary: %q", s)
	}
}

func TestWebInstructionsMentionTools(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
	}{
		{"fenced", Instructions("/tmp/x", true)},
		{"native", NativeInstructions("/tmp/x", true)},
	} {
		if !strings.Contains(tc.text, "web_search") || !strings.Contains(tc.text, "untrusted") {
			t.Errorf("%s instructions missing web guidance", tc.name)
		}
	}
	if strings.Contains(Instructions("/tmp/x", false), "web_search") {
		t.Error("fenced instructions mention web tools while disabled")
	}
	if strings.Contains(NativeInstructions("/tmp/x", false), "web_search") {
		t.Error("native instructions mention web tools while disabled")
	}
}
