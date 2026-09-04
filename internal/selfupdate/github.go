package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// defaultAPIBase is the GitHub REST API root.
const defaultAPIBase = "https://api.github.com"

// maxAPIResponseBytes bounds a releases JSON response. The real payload for
// this repo is a few KB; this is generous headroom, not a tuned value.
const maxAPIResponseBytes = 8 << 20

// releaseListLimit bounds how many releases are considered when picking the
// latest stable one.
const releaseListLimit = 30

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"browser_download_url"`
	// Digest is GitHub's optional "sha256:<hex>" content digest. Empty on
	// older releases; verified as an extra check when present.
	Digest string `json:"digest"`
}

// Release is a single GitHub release.
type Release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	HTMLURL    string  `json:"html_url"`
	Assets     []Asset `json:"assets"`
}

// Asset returns the release asset with the given name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Client talks to the GitHub Releases API for the pinned repository.
type Client struct {
	HTTP  *http.Client
	Token string // optional; never logged, never persisted
	// BaseURL overrides the GitHub API root. Empty means the real API;
	// tests point it at an httptest.Server.
	BaseURL string
}

func (c *Client) apiBase() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultAPIBase
}

// NewClient returns a Client with a bounded HTTP client and, if present, a
// token from GITHUB_TOKEN or GH_TOKEN. A token is optional and only raises
// the rate limit; the repository is public.
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: 20 * time.Second,
			},
		},
		Token: firstNonEmptyEnv("GITHUB_TOKEN", "GH_TOKEN"),
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// ErrNoStableRelease is returned when the repository has releases but none
// that are stable (non-draft, non-prerelease).
var ErrNoStableRelease = errors.New("no stable release found")

// LatestStableRelease returns the highest-semver non-draft, non-prerelease
// release. includePrerelease widens the selection to prereleases too (drafts
// are always excluded).
func (c *Client) LatestStableRelease(ctx context.Context, includePrerelease bool) (Release, error) {
	releases, err := c.listReleases(ctx)
	if err != nil {
		return Release{}, err
	}

	type candidate struct {
		rel Release
		ver Version
	}
	var cands []candidate
	for _, rel := range releases {
		if rel.Draft {
			continue
		}
		if rel.Prerelease && !includePrerelease {
			continue
		}
		ver, err := ParseVersion(rel.TagName)
		if err != nil {
			continue // ignore tags that are not semver
		}
		cands = append(cands, candidate{rel: rel, ver: ver})
	}
	if len(cands) == 0 {
		return Release{}, ErrNoStableRelease
	}
	sort.Slice(cands, func(i, j int) bool {
		return cands[i].ver.Compare(cands[j].ver) > 0
	})
	return cands[0].rel, nil
}

func (c *Client) listReleases(ctx context.Context) ([]Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=%d", c.apiBase(), RepoOwner, RepoName, releaseListLimit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// continue
	case http.StatusForbidden, http.StatusTooManyRequests:
		if isRateLimited(resp) {
			return nil, rateLimitError(resp)
		}
		return nil, fmt.Errorf("github returned HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	case http.StatusNotFound:
		return nil, fmt.Errorf("github repository %s/%s not found (HTTP 404)", RepoOwner, RepoName)
	default:
		return nil, fmt.Errorf("github returned HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read github response: %w", err)
	}
	if int64(len(body)) > maxAPIResponseBytes {
		return nil, fmt.Errorf("github response exceeds %d bytes", maxAPIResponseBytes)
	}

	var releases []Release
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, fmt.Errorf("parse github releases JSON: %w", err)
	}
	return releases, nil
}

func isRateLimited(resp *http.Response) bool {
	return resp.Header.Get("X-RateLimit-Remaining") == "0"
}

func rateLimitError(resp *http.Response) error {
	msg := "github API rate limit exceeded"
	if reset := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")); reset != "" {
		if unix, err := strconv.ParseInt(reset, 10, 64); err == nil {
			msg += fmt.Sprintf("; resets around %s", time.Unix(unix, 0).Local().Format(time.Kitchen))
		}
	}
	msg += ". Set GITHUB_TOKEN to raise the limit, or retry later."
	return errors.New(msg)
}

func classifyTransportError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("github request timed out: %w", err)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var dnsErr interface{ Timeout() bool }
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("network error contacting github (offline?): %w", err)
	}
	return fmt.Errorf("contact github: %w", err)
}
