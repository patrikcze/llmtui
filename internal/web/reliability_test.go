package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/testutil"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchRetriesResetWithSameForm(t *testing.T) {
	c := NewClient(time.Second, 64)
	calls := 0
	var deadline time.Time
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Method != http.MethodPost || r.PostForm.Get("q") != "funny true facts" {
			t.Fatalf("bad request: %v", r.PostForm)
		}
		d, ok := r.Context().Deadline()
		if !ok {
			t.Error("missing operation deadline")
		}
		if calls == 1 {
			deadline = d
			return nil, syscall.ECONNRESET
		}
		if !deadline.Equal(d) {
			t.Error("retry reset the timeout budget")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`<a class="result__a" href="https://example.com">Fact</a>`)), Header: make(http.Header), Request: r}, nil
	})
	c.http.Transport = transport
	c.httpH1.Transport = transport
	results, err := c.Search(t.Context(), "funny true facts", 5)
	if err != nil || len(results) != 1 || calls != 2 {
		t.Fatalf("results=%v err=%v calls=%d", results, err, calls)
	}
}

func TestSearchRejectsChallengeAndUnexpectedPages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		rate   bool
	}{
		{"accepted challenge", 202, `<form id="challenge-form" action="/anomaly.js"></form>`, true},
		{"ok challenge", 200, `<form id="challenge-form" action="/anomaly.js"></form>`, true},
		{"rate limited", 429, "slow down", true},
		{"forbidden", 403, "forbidden", true},
		{"unexpected markup", 200, `<html>Something changed</html>`, false},
		{"oversized", 200, strings.Repeat("x", rawReadCap+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := searchClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := c.Search(t.Context(), "query", 5)
			if err == nil || errors.Is(err, ErrRateLimited) != tc.rate {
				t.Fatalf("error=%v rate=%v", err, tc.rate)
			}
			if calls != 1 {
				t.Fatalf("calls=%d, want 1", calls)
			}
		})
	}
}

func TestFetchUsesRedirectDestination(t *testing.T) {
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/articles/final", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><title>Article</title><article><p><a href="related">Related</a>`+strings.Repeat(" Long readable article text.", 50)+`</p></article></html>`)
	}))
	defer srv.Close()
	page, err := testClient(64).Fetch(t.Context(), srv.URL+"/start")
	if err != nil {
		t.Fatal(err)
	}
	if page.URL != srv.URL+"/articles/final" || !strings.Contains(page.Content, srv.URL+"/articles/related") {
		t.Fatalf("page=%+v", page)
	}
}

func TestFetchMarksRawTruncation(t *testing.T) {
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><p>Short visible text</p><script>`+strings.Repeat("x", rawReadCap)+`</script></body></html>`)
	}))
	defer srv.Close()
	page, err := testClient(64).Fetch(t.Context(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || page.Bytes != rawReadCap {
		t.Fatalf("truncated=%v bytes=%d", page.Truncated, page.Bytes)
	}
}

func TestFetchDoesNotRetryRateLimit(t *testing.T) {
	calls := 0
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	page, err := testClient(64).Fetch(context.Background(), srv.URL)
	if err == nil || page.Status != 429 || calls != 1 {
		t.Fatalf("status=%d calls=%d err=%v", page.Status, calls, err)
	}
}

func TestSearchFallsBackFromHTTP2ToHTTP1(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		testutil.SkipIfListenerUnavailable(t, err)
		t.Fatal(err)
	}
	protocols := make(chan int, 2)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protocols <- r.ProtoMajor
		if r.ProtoMajor == 2 {
			panic(http.ErrAbortHandler)
		}
		if r.FormValue("q") != "facts" {
			t.Error("query lost on retry")
		}
		fmt.Fprint(w, `<a class="result__a" href="https://example.com">Fact</a>`)
	}))
	srv.Listener = listener
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	c := testClient(64)
	c.searchURL = srv.URL
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	for _, client := range []*http.Client{c.http, c.httpH1} {
		transport := client.Transport.(*http.Transport)
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		t.Cleanup(transport.CloseIdleConnections)
	}
	results, err := c.Search(t.Context(), "facts", 5)
	if err != nil || len(results) != 1 {
		t.Fatalf("results=%v err=%v", results, err)
	}
	if len(protocols) != 2 {
		t.Fatalf("attempts=%d", len(protocols))
	}
	if first, second := <-protocols, <-protocols; first != 2 || second != 1 {
		t.Fatalf("protocols=%d,%d", first, second)
	}
}

func TestWebRetryStops(t *testing.T) {
	for _, operation := range []string{"search", "fetch"} {
		for _, tc := range []struct {
			name      string
			err       error
			cancel    bool
			wantCalls int
		}{
			{"persistent reset", syscall.ECONNRESET, false, 2},
			{"blocked address", errBlockedAddress, false, 1},
			{"redirect limit", errTooManyRedirects, false, 1},
			{"cancelled", context.Canceled, true, 1},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				c := NewClient(time.Second, 64)
				calls := 0
				var deadline time.Time
				transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					d, ok := r.Context().Deadline()
					if !ok {
						t.Error("missing operation deadline")
					}
					if calls == 1 {
						deadline = d
					} else if !deadline.Equal(d) {
						t.Error("timeout reset on retry")
					}
					if tc.cancel {
						cancel()
					}
					return nil, tc.err
				})
				c.http.Transport = transport
				c.httpH1.Transport = transport
				var err error
				if operation == "search" {
					_, err = c.Search(ctx, "facts", 5)
				} else {
					_, err = c.Fetch(ctx, "https://example.com")
				}
				if !errors.Is(err, tc.err) || calls != tc.wantCalls {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			})
		}
	}
}

func TestFetchRawCapExactSizeIsNotTruncated(t *testing.T) {
	c := testClient(rawReadCap / 1024)
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/plain"}}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", rawReadCap))), Request: r}, nil
	})
	page, err := c.Fetch(t.Context(), "https://example.com")
	if err != nil || page.Truncated || page.Bytes != rawReadCap {
		t.Fatalf("truncated=%v bytes=%d err=%v", page.Truncated, page.Bytes, err)
	}
}
