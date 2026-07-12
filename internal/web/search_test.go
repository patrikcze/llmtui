package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func searchClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient(5*time.Second, 64)
	c.allowPrivate = true
	c.searchURL = srv.URL
	return c
}

func TestSearchParsesResultsAndDecodesRedirects(t *testing.T) {
	fixture, err := os.ReadFile("testdata/ddg_results.html")
	if err != nil {
		t.Fatal(err)
	}
	c := searchClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("q") != "ollama truncated" {
			t.Errorf("unexpected request: %s q=%q", r.Method, r.FormValue("q"))
		}
		w.Header().Set("Content-Type", "text/html")
		if _, err := w.Write(fixture); err != nil {
			t.Errorf("write response: %v", err)
		}
	})
	results, err := c.Search(context.Background(), "ollama truncated", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	if results[0].URL != "https://github.com/ollama/ollama/issues/14570" {
		t.Errorf("redirect not decoded: %q", results[0].URL)
	}
	if results[0].Title != "qwen3 tool call parser returns 500" {
		t.Errorf("title: %q", results[0].Title)
	}
	if results[0].Snippet == "" || results[1].URL != "https://example.com/direct" {
		t.Errorf("snippet/direct URL wrong: %+v", results)
	}
}

func TestSearchHonorsMax(t *testing.T) {
	fixture, err := os.ReadFile("testdata/ddg_results.html")
	if err != nil {
		t.Fatal(err)
	}
	c := searchClient(t, func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write(fixture); err != nil {
			t.Errorf("write response: %v", err)
		}
	})
	results, err := c.Search(context.Background(), "q", 1)
	if err != nil || len(results) != 1 {
		t.Fatalf("got %d results (err %v), want 1", len(results), err)
	}
}

func TestSearchNoResults(t *testing.T) {
	c := searchClient(t, func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("<html><body><div class='no-results'>No results.</div></body></html>")); err != nil {
			t.Errorf("write response: %v", err)
		}
	})
	results, err := c.Search(context.Background(), "zxqj", 5)
	if err != nil || len(results) != 0 {
		t.Fatalf("want empty, got %v (err %v)", results, err)
	}
}

func TestSearchRateLimited(t *testing.T) {
	c := searchClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if _, err := c.Search(context.Background(), "q", 5); err == nil || !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}
