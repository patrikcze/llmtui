package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testClient returns a Client pointed at srv with a short timeout.
func testClient(baseURL string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 5 * time.Second},
		BaseURL: baseURL,
	}
}

func releasesServer(t *testing.T, releases []Release) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("request missing User-Agent")
		}
		if !strings.Contains(r.URL.Path, "/repos/"+RepoOwner+"/"+RepoName+"/releases") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releases)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestStableRelease(t *testing.T) {
	releases := []Release{
		{TagName: "v1.0.24-rc1", Prerelease: true},
		{TagName: "v1.0.25", Draft: true},
		{TagName: "v1.0.23"},
		{TagName: "v1.0.22"},
		{TagName: "nightly"}, // not semver
	}
	srv := releasesServer(t, releases)
	c := testClient(srv.URL)

	rel, err := c.LatestStableRelease(context.Background(), false)
	if err != nil {
		t.Fatalf("LatestStableRelease: %v", err)
	}
	if rel.TagName != "v1.0.23" {
		t.Fatalf("got %q, want v1.0.23", rel.TagName)
	}

	rel, err = c.LatestStableRelease(context.Background(), true)
	if err != nil {
		t.Fatalf("with prerelease: %v", err)
	}
	if rel.TagName != "v1.0.24-rc1" {
		t.Fatalf("got %q, want v1.0.24-rc1", rel.TagName)
	}
}

func TestLatestStableReleaseNoneStable(t *testing.T) {
	srv := releasesServer(t, []Release{{TagName: "v1.0.0-rc1", Prerelease: true}})
	c := testClient(srv.URL)
	_, err := c.LatestStableRelease(context.Background(), false)
	if err != ErrNoStableRelease {
		t.Fatalf("got %v, want ErrNoStableRelease", err)
	}
}

func TestLatestStableReleaseHighestWins(t *testing.T) {
	// Publish order deliberately not sorted; a patch to an old minor must not
	// beat a newer minor.
	srv := releasesServer(t, []Release{
		{TagName: "v1.2.0"},
		{TagName: "v1.1.9"},
		{TagName: "v1.10.0"},
		{TagName: "v1.2.1"},
	})
	rel, err := testClient(srv.URL).LatestStableRelease(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v1.10.0" {
		t.Fatalf("got %q, want v1.10.0", rel.TagName)
	}
}

func TestListReleasesRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	_, err := testClient(srv.URL).LatestStableRelease(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("got %v, want a rate-limit error", err)
	}
	if strings.Contains(err.Error(), "GITHUB_TOKEN=") {
		t.Fatalf("error text leaked a token value: %v", err)
	}
}

func TestListReleasesNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	_, err := testClient(srv.URL).LatestStableRelease(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("got %v, want a not-found error", err)
	}
}

func TestListReleasesMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	t.Cleanup(srv.Close)
	_, err := testClient(srv.URL).LatestStableRelease(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "parse github releases JSON") {
		t.Fatalf("got %v, want a parse error", err)
	}
}

func TestListReleasesContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testClient(srv.URL).LatestStableRelease(ctx, false)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestTokenFromEnvNotLogged(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-token-value")
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[{"tag_name":"v1.0.0"}]`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	c.BaseURL = srv.URL
	if _, err := c.LatestStableRelease(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret-token-value" {
		t.Fatalf("Authorization header = %q", gotAuth)
	}
}
