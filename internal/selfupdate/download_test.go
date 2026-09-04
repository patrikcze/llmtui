package selfupdate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func allowTestDownloadHost(t *testing.T) {
	t.Helper()
	testDownloadHosts["127.0.0.1"] = true
	testDownloadHosts["localhost"] = true
	t.Cleanup(func() {
		delete(testDownloadHosts, "127.0.0.1")
		delete(testDownloadHosts, "localhost")
	})
}

func TestDownloadAssetSuccess(t *testing.T) {
	allowTestDownloadHost(t)
	body := []byte(strings.Repeat("llmtui", 4096))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	dest := filepath.Join(t.TempDir(), "asset.bin")
	res, err := DownloadAsset(context.Background(), nil, Asset{
		Name: "asset.bin", Size: int64(len(body)), DownloadURL: srv.URL,
	}, dest)
	if err != nil {
		t.Fatalf("DownloadAsset: %v", err)
	}
	if res.SHA256 != sha256Hex(body) {
		t.Errorf("sha mismatch")
	}
	if res.Size != int64(len(body)) {
		t.Errorf("size = %d", res.Size)
	}
}

func TestDownloadAssetSizeMismatch(t *testing.T) {
	allowTestDownloadHost(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("short"))
	}))
	t.Cleanup(srv.Close)
	dest := filepath.Join(t.TempDir(), "a")
	_, err := DownloadAsset(context.Background(), nil, Asset{Name: "a", Size: 9999, DownloadURL: srv.URL}, dest)
	if err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("got %v, want size mismatch", err)
	}
}

func TestDownloadAssetRejectsBadURL(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "a")
	for _, u := range []string{
		"http://example.com/llmtui.tar.gz",
		"https://evil.example/llmtui.tar.gz",
		"ftp://github.com/x",
	} {
		if _, err := DownloadAsset(context.Background(), nil, Asset{Name: "a", DownloadURL: u}, dest); err == nil {
			t.Errorf("accepted bad URL %q", u)
		}
	}
}

func TestDownloadAssetOversizeDeclared(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "a")
	_, err := DownloadAsset(context.Background(), nil, Asset{
		Name: "a", Size: maxArchiveBytes + 1, DownloadURL: "https://github.com/x",
	}, dest)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("got %v, want over-limit rejection", err)
	}
}

func TestDownloadAssetDigestMismatch(t *testing.T) {
	allowTestDownloadHost(t)
	body := []byte("some bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	dest := filepath.Join(t.TempDir(), "a")
	_, err := DownloadAsset(context.Background(), nil, Asset{
		Name: "a", Size: int64(len(body)), DownloadURL: srv.URL,
		Digest: "sha256:" + strings.Repeat("0", 64),
	}, dest)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("got %v, want digest mismatch", err)
	}
}
