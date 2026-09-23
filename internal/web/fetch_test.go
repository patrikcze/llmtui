package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/testutil"
)

func testClient(kb int) *Client {
	c := NewClient(5*time.Second, kb)
	c.allowPrivate = true
	return c
}

func TestFetchHTMLBecomesMarkdown(t *testing.T) {
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestFetchRetainsBodyAndCacheMetadataBeforePreviewCap(t *testing.T) {
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "max-age=60")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Wed, 21 Oct 2015 07:28:00 GMT")
		w.Header().Set("Vary", "Accept-Language")
		fmt.Fprint(w, strings.Repeat("x", 3*1024))
	}))
	defer srv.Close()
	page, err := testClient(1).FetchWithOptions(context.Background(), srv.URL, FetchOptions{Mode: FetchAuto, MaxAge: time.Minute})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(page.Body) != 3*1024 || len(page.Content) >= len(page.Body) || page.BodyDigest == "" || page.SourceDigest == "" {
		t.Fatalf("body=%d content=%d bodyDigest=%q sourceDigest=%q", len(page.Body), len(page.Content), page.BodyDigest, page.SourceDigest)
	}
	if page.ETag != `"abc"` || page.LastModified == "" || page.FreshUntil.IsZero() || page.Vary == "" {
		t.Fatalf("cache metadata missing: %+v", page)
	}
}

func TestFetchConditional304(t *testing.T) {
	seen := make(chan string, 1)
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("If-None-Match")
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Cache-Control", "max-age=60")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	page, err := testClient(64).FetchWithOptions(context.Background(), srv.URL, FetchOptions{Mode: FetchAuto, ETag: `"abc"`, MaxAge: time.Minute})
	if err != nil || !page.NotModified || page.Body != "" {
		t.Fatalf("304 page=%+v err=%v", page, err)
	}
	if got := <-seen; got != `"abc"` {
		t.Errorf("If-None-Match=%q", got)
	}
}

func BenchmarkFetchSmallPage(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = w.Write([]byte("forecast: sunny\n" + strings.Repeat("detail ", 40)))
	}))
	defer srv.Close()
	c := testClient(64)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.FetchWithOptions(context.Background(), srv.URL, FetchOptions{Mode: FetchRefresh, MaxAge: time.Minute}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestFetchNon2xxReturnsErrorWithBody(t *testing.T) {
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestFetchSendsBrowserHeaders(t *testing.T) {
	seen := make(chan http.Header, 1)
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case seen <- r.Header.Clone():
		default:
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	if _, err := testClient(64).Fetch(context.Background(), srv.URL); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	h := <-seen
	if ua := h.Get("User-Agent"); !strings.HasPrefix(ua, "Mozilla/5.0") || strings.Contains(ua, "llmtui") {
		t.Errorf("User-Agent = %q, want a browser string", ua)
	}
	if h.Get("Accept-Language") == "" {
		t.Error("Accept-Language header missing")
	}
}

// TestFetchRetriesOverHTTP1AfterTransportError simulates the HTTP/2 stream
// reset that bot-protection layers trigger for non-browser clients: the first
// connection is closed before any response, the retry succeeds.
func TestFetchRetriesOverHTTP1AfterTransportError(t *testing.T) {
	var calls int32
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				t.Error("ResponseWriter is not a Hijacker")
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "recovered")
	}))
	defer srv.Close()

	page, err := testClient(64).Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if page.Content != "recovered" {
		t.Errorf("content = %q, want recovered", page.Content)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server calls = %d, want 2 (initial + retry)", got)
	}
}

func TestFetchRetriesOnceOnBlockStatusThenStops(t *testing.T) {
	var calls int32
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "bot challenge", http.StatusForbidden)
	}))
	defer srv.Close()

	page, err := testClient(64).Fetch(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want 403 error, got %v", err)
	}
	if page.Status != 403 || !strings.Contains(page.Content, "bot challenge") {
		t.Errorf("status=%d content=%q", page.Status, page.Content)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("server calls = %d, want exactly 2 (no retry loop)", got)
	}
}

func TestFetchDoesNotRetryOnNon2xxThatIsNotABlock(t *testing.T) {
	var calls int32
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := testClient(64).Fetch(context.Background(), srv.URL); err == nil {
		t.Fatal("want 404 error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server calls = %d, want 1 (404 is not retried)", got)
	}
}
