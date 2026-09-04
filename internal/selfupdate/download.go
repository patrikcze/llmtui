package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// maxArchiveBytes bounds a release archive download. Desktop archives with the
// bundled llama.cpp CPU runtime are well under 200 MB; this is headroom.
const maxArchiveBytes = 1 << 30

// downloadHostAllowlist is the set of hosts a release asset may be served
// from. GitHub redirects browser_download_url to the objects host; both are
// pinned so a tampered release body cannot point the downloader elsewhere.
var downloadHostAllowlist = map[string]bool{
	"github.com":                           true,
	"api.github.com":                       true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
	"codeload.github.com":                  true,
}

// downloadClient returns an HTTP client suitable for large streamed
// downloads: no overall timeout (a slow link must not abort a valid
// download), but bounded connect/handshake/idle behaviour. Redirects are
// checked against the host allowlist.
func downloadClient(base *http.Client) *http.Client {
	c := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if err := checkDownloadURL(req.URL); err != nil {
				return err
			}
			return nil
		},
	}
	if base != nil && base.Transport != nil {
		c.Transport = base.Transport
	}
	return c
}

// testDownloadHosts is populated only by tests (via allowTestDownloadHost) to
// permit an httptest.Server host. It is always empty in production builds.
var testDownloadHosts = map[string]bool{}

func checkDownloadURL(u *url.URL) error {
	host := strings.ToLower(u.Hostname())
	if testDownloadHosts[host] {
		return nil
	}
	if u.Scheme != "https" {
		return fmt.Errorf("refusing non-HTTPS download URL %q", u.Redacted())
	}
	if !downloadHostAllowlist[host] {
		return fmt.Errorf("refusing download from unexpected host %q", host)
	}
	return nil
}

// DownloadResult reports where an asset was written and its SHA-256.
type DownloadResult struct {
	Path   string
	SHA256 string
	Size   int64
}

// DownloadAsset streams a release asset to destPath, enforcing the HTTPS host
// allowlist, a maximum size, context cancellation and the asset's declared
// size. It returns the SHA-256 of the bytes written; callers must still
// verify that against checksums.txt before using the file.
func DownloadAsset(ctx context.Context, httpClient *http.Client, a Asset, destPath string) (DownloadResult, error) {
	parsed, err := url.Parse(a.DownloadURL)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("parse asset URL: %w", err)
	}
	if err := checkDownloadURL(parsed); err != nil {
		return DownloadResult{}, err
	}

	limit := int64(maxArchiveBytes)
	if a.Size > 0 {
		if a.Size > maxArchiveBytes {
			return DownloadResult{}, fmt.Errorf("asset %q declares %d bytes, over the %d limit", a.Name, a.Size, int64(maxArchiveBytes))
		}
		limit = a.Size
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.DownloadURL, nil)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", userAgent)

	client := downloadClient(httpClient)
	// A generous ceiling so a stalled connection eventually fails, without
	// killing a legitimately slow large download.
	dlCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	req = req.WithContext(dlCtx)

	resp, err := client.Do(req)
	if err != nil {
		return DownloadResult{}, classifyTransportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("download %s: HTTP %d %s", a.Name, resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("create download file: %w", err)
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(destPath)
		return DownloadResult{}, fmt.Errorf("download %s: %w", a.Name, copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(destPath)
		return DownloadResult{}, fmt.Errorf("flush download: %w", closeErr)
	}
	if n > limit {
		_ = os.Remove(destPath)
		return DownloadResult{}, fmt.Errorf("download %s exceeded expected size %d", a.Name, limit)
	}
	if a.Size > 0 && n != a.Size {
		_ = os.Remove(destPath)
		return DownloadResult{}, fmt.Errorf("download %s: got %d bytes, expected %d", a.Name, n, a.Size)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	if digest := strings.TrimSpace(a.Digest); digest != "" {
		want := strings.TrimPrefix(strings.ToLower(digest), "sha256:")
		if isHexSHA256(want) && !strings.EqualFold(want, sum) {
			_ = os.Remove(destPath)
			return DownloadResult{}, fmt.Errorf("download %s: GitHub content digest mismatch", a.Name)
		}
	}
	return DownloadResult{Path: destPath, SHA256: sum, Size: n}, nil
}
