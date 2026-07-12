package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(kb int) *Client {
	c := NewClient(5*time.Second, kb)
	c.allowPrivate = true
	return c
}

func TestFetchHTMLBecomesMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>My Page</title></head><body>
			<nav>menu junk</nav>
			<article><h1>Heading</h1><p>Some <strong>bold</strong> body text that is long enough for readability to keep. `+strings.Repeat("More words here. ", 30)+`</p></article>
		</body></html>`)
	}))
	defer srv.Close()
	page, err := testClient(64).Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if page.Status != 200 || page.Truncated {
		t.Errorf("status=%d truncated=%v", page.Status, page.Truncated)
	}
	if !strings.Contains(page.Content, "**bold**") {
		t.Errorf("expected markdown bold, got: %.200s", page.Content)
	}
	if strings.Contains(page.Content, "<p>") {
		t.Errorf("raw HTML leaked into content")
	}
}

func TestFetchPlainTextAndJSONPassThrough(t *testing.T) {
	for _, ct := range []string{"text/plain", "application/json"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ct)
			fmt.Fprint(w, `{"ok":true}`)
		}))
		page, err := testClient(64).Fetch(context.Background(), srv.URL)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", ct, err)
		}
		if page.Content != `{"ok":true}` {
			t.Errorf("%s: content %q", ct, page.Content)
		}
	}
}

func TestFetchRejectsBinaryContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		if _, err := w.Write([]byte{0x89, 'P', 'N', 'G'}); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()
	if _, err := testClient(64).Fetch(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "image/png") {
		t.Fatalf("want unsupported-content-type error, got %v", err)
	}
}

func TestFetchTruncatesToCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, strings.Repeat("x", 3*1024))
	}))
	defer srv.Close()
	page, err := testClient(1).Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !page.Truncated || len(page.Content) > 1200 || !strings.Contains(page.Content, "truncated") {
		t.Errorf("truncated=%v len=%d", page.Truncated, len(page.Content))
	}
}

func TestFetchNon2xxReturnsErrorWithBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone fishing", http.StatusNotFound)
	}))
	defer srv.Close()
	page, err := testClient(64).Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("want status error, got %v", err)
	}
	if page.Status != 404 || !strings.Contains(page.Content, "gone fishing") {
		t.Errorf("status=%d content=%q", page.Status, page.Content)
	}
}
