package web

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalizeResearchQueries(t *testing.T) {
	tests := []struct {
		name     string
		queries  []string
		fallback string
		want     []string
	}{
		{name: "none falls back to question", fallback: " go release ", want: []string{"go release"}},
		{name: "blank entries fall back", queries: []string{"", "  "}, fallback: "q", want: []string{"q"}},
		{name: "case-insensitive dedupe", queries: []string{"Go 1.27", "go 1.27", "go release notes"}, fallback: "q", want: []string{"Go 1.27", "go release notes"}},
		{name: "capped at three", queries: []string{"a1", "b2", "c3", "d4"}, fallback: "q", want: []string{"a1", "b2", "c3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeResearchQueries(tt.queries, tt.fallback); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResultKey(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
		ok   bool
	}{
		{name: "www and trailing slash", url: "https://WWW.Example.com/docs/", want: "example.com/docs", ok: true},
		{name: "tracking parameters dropped", url: "https://example.com/a?utm_source=x&id=7&fbclid=y&ref=z&gclid=w", want: "example.com/a?id=7", ok: true},
		{name: "http kept", url: "http://example.com", want: "example.com", ok: true},
		{name: "duckduckgo dropped", url: "https://duckduckgo.com/y.js?ad=1", ok: false},
		{name: "duckduckgo subdomain dropped", url: "https://links.duckduckgo.com/l/?uddg=x", ok: false},
		{name: "non-http dropped", url: "ftp://example.com/file", ok: false},
		{name: "no host dropped", url: "/relative/path", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ResultKey(tt.url)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ResultKey(%q) = %q, %v; want %q, %v", tt.url, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestFuseResultsRanksAgreementFirstAndDedupes(t *testing.T) {
	lists := [][]SearchResult{
		{
			{Title: "A", URL: "https://a.example/x"},
			{Title: "B", URL: "https://b.example/y"},
			{Title: "Ad", URL: "https://duckduckgo.com/y.js?ad=1"},
		},
		{
			{Title: "B again", URL: "https://www.b.example/y/?utm_source=feed"},
			{Title: "C", URL: "https://c.example/z"},
		},
	}
	got := FuseResults(lists, 2)
	var titles []string
	for _, result := range got {
		titles = append(titles, result.Title)
	}
	// B was found by both queries, so it outranks A (first in one list);
	// the duplicate keeps its first-seen entry and the ad is dropped.
	if want := []string{"B", "A", "C"}; !reflect.DeepEqual(titles, want) {
		t.Fatalf("fused titles = %q, want %q", titles, want)
	}
}

func TestFuseResultsCapsPagesPerSite(t *testing.T) {
	lists := [][]SearchResult{{
		{Title: "1", URL: "https://docs.example/1"},
		{Title: "2", URL: "https://docs.example/2"},
		{Title: "3", URL: "https://docs.example/3"},
		{Title: "other", URL: "https://other.example/"},
	}}
	got := FuseResults(lists, 2)
	var titles []string
	for _, result := range got {
		titles = append(titles, result.Title)
	}
	if want := []string{"1", "2", "other"}; !reflect.DeepEqual(titles, want) {
		t.Fatalf("fused titles = %q, want %q", titles, want)
	}
}

func TestKeyTerms(t *testing.T) {
	got := KeyTerms("What is the latest Go release in 2026, and when was Go 1.27 released?")
	// Stop words and the two-letter "go" are dropped; numbers are kept.
	want := []string{"latest", "release", "2026", "1", "27", "released"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KeyTerms = %q, want %q", got, want)
	}
}

func TestSplitPassagesJoinsShortLinesAndDropsChrome(t *testing.T) {
	text := "Home\nMenu\n" +
		"Go 1.27 was released in August 2026 with a new garbage collector.\n" +
		"It also improves the toolchain.\n\n" +
		strings.Repeat("A long sentence about generics and iterators. ", 20)
	got := SplitPassages(text, 80, 600)
	if len(got) < 2 {
		t.Fatalf("passages = %q", got)
	}
	for _, passage := range got {
		if n := utf8.RuneCountInString(passage); n < 40 || n > 600 {
			t.Errorf("passage length %d out of bounds: %q", n, passage)
		}
		if strings.Contains(passage, "Menu") && !strings.Contains(passage, "Go 1.27") {
			t.Errorf("navigation chrome survived as its own passage: %q", passage)
		}
	}
}

func TestSplitPassagesHardSplitsOnRuneBoundaries(t *testing.T) {
	line := strings.Repeat("ž", 1500) // one "sentence" with no breaks
	for _, passage := range SplitPassages(line, 80, 600) {
		if !utf8.ValidString(passage) {
			t.Fatalf("split produced invalid UTF-8")
		}
		if n := utf8.RuneCountInString(passage); n > 600 {
			t.Fatalf("passage has %d runes, want <= 600", n)
		}
	}
}

func TestRelevantPassagesPicksMatchesInPageOrder(t *testing.T) {
	filler := "This paragraph talks about something unrelated to the question at all, purely filler text."
	text := strings.Join([]string{
		filler,
		"The Kubernetes 1.34 release adds sidecar improvements and was published in 2026.",
		filler,
		"Upgrading Kubernetes clusters requires draining nodes before the release rollout.",
		filler,
	}, "\n\n")
	got := RelevantPassages(text, KeyTerms("kubernetes release 2026"), 5, 2000)
	if len(got) != 2 {
		t.Fatalf("passages = %q", got)
	}
	if !strings.Contains(got[0], "1.34") || !strings.Contains(got[1], "draining") {
		t.Fatalf("passages not in page order: %q", got)
	}
}

func TestRelevantPassagesRespectsBudgetAndLimit(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "Azure VMware Solution pricing depends on the host SKU and the region chosen.")
	}
	text := strings.Join(lines, "\n\n")
	got := RelevantPassages(text, KeyTerms("azure vmware pricing"), 3, 2000)
	if len(got) != 3 {
		t.Fatalf("limit not applied: %d passages", len(got))
	}
	got = RelevantPassages(text, KeyTerms("azure vmware pricing"), 10, 200)
	total := 0
	for _, passage := range got {
		total += utf8.RuneCountInString(passage)
	}
	if total > 200 || len(got) == 0 {
		t.Fatalf("budget not applied: %d chars in %d passages", total, len(got))
	}
}

func TestRelevantPassagesFallsBackToOpening(t *testing.T) {
	text := strings.Join([]string{
		"First paragraph of the page that says nothing about the search terms at all, by design.",
		"Second paragraph of the page that also says nothing about the search terms, by design.",
		"Third paragraph of the page, still with nothing relevant to the question here, by design.",
	}, "\n\n")
	got := RelevantPassages(text, []string{"quantum"}, 5, 2000)
	if len(got) != 2 || !strings.HasPrefix(got[0], "First") || !strings.HasPrefix(got[1], "Second") {
		t.Fatalf("fallback passages = %q", got)
	}
	if got := RelevantPassages("", []string{"x"}, 5, 2000); got != nil {
		t.Fatalf("empty text = %q", got)
	}
}
