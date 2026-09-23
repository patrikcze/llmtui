package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"

	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// rawReadCap bounds how much of a response body is read at all; maxPageKB
// then bounds what reaches the model.
const rawReadCap = 4 << 20

// userAgent presents as a mainstream desktop browser. A tool-identifying
// string ("llmtui/…") is refused or served a bot challenge by a large share
// of public sites (news, weather, anything behind Cloudflare/Akamai), which
// made web_fetch fail on exactly the pages users ask for. The request stays
// GET-only, rate-unfriendly behaviour is not added, and robots are not
// bypassed — this only stops trivial UA-string filtering.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

const acceptLanguage = "en-US,en;q=0.9"

// retryableStatuses are answered again once over HTTP/1.1: they are the codes
// bot-protection layers use for "prove you're a browser" holds rather than a
// genuine "this page does not exist".
var retryableStatuses = map[int]bool{
	http.StatusForbidden:          true, // 403
	http.StatusServiceUnavailable: true, // 503
}

// Fetch downloads one page and reduces it to Markdown/plain text. On non-2xx
// statuses the page (with any text body) and an error are both returned so
// the model can see what the server said.
func (c *Client) Fetch(ctx context.Context, rawURL string) (Page, error) {
	return c.FetchWithOptions(ctx, rawURL, FetchOptions{Mode: FetchRefresh})
}

// FetchWithOptions downloads one page with optional HTTP validators. A 304 is
// returned as a successful metadata-only Page so the caller can reuse its
// retained snapshot without issuing a second request or pretending that the
// network returned a fresh body.
func (c *Client) FetchWithOptions(ctx context.Context, rawURL string, opts FetchOptions) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()
	rawURL = strings.TrimSpace(rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return Page{URL: rawURL, RequestedURL: rawURL}, fmt.Errorf("parse URL: %w", err)
	}
	if err := checkURL(u); err != nil {
		return Page{URL: rawURL, RequestedURL: rawURL}, err
	}
	validators := make(http.Header)
	if strings.TrimSpace(opts.ETag) != "" {
		validators.Set("If-None-Match", strings.TrimSpace(opts.ETag))
	}
	if strings.TrimSpace(opts.LastModified) != "" {
		validators.Set("If-Modified-Since", strings.TrimSpace(opts.LastModified))
	}
	resp, err := c.fetchResponse(ctx, u, validators)
	if err != nil {
		return Page{URL: rawURL, RequestedURL: rawURL}, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	u = resp.Request.URL
	page := Page{URL: u.String(), RequestedURL: rawURL, Status: resp.StatusCode, AcquiredAt: time.Now().UTC()}
	page.ETag = resp.Header.Get("ETag")
	page.LastModified = resp.Header.Get("Last-Modified")
	page.CacheControl = resp.Header.Get("Cache-Control")
	page.Vary = resp.Header.Get("Vary")
	page.NoStore, page.FreshUntil = cachePolicy(resp.Header, page.AcquiredAt, opts.MaxAge)
	if resp.StatusCode == http.StatusNotModified {
		page.NotModified = true
		return page, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, rawReadCap+1))
	if err != nil {
		return page, fmt.Errorf("read response: %w", err)
	}
	rawTruncated := len(body) > rawReadCap
	if rawTruncated {
		body = body[:rawReadCap]
	}
	page.RawTruncated = rawTruncated
	page.Bytes = len(body)
	if !rawTruncated {
		page.SourceDigest = digestBytes(body)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	page.ContentType = ct

	switch {
	case ct == "text/html" || ct == "application/xhtml+xml":
		page.Title, page.Content = htmlToMarkdown(body, u)
	case strings.HasPrefix(ct, "text/") || ct == "application/json" ||
		strings.HasSuffix(ct, "+json") || ct == "application/xml" || strings.HasSuffix(ct, "+xml"):
		page.Content = string(body)
	default:
		return page, fmt.Errorf("unsupported content type %q — only HTML, text, and JSON/XML pages can be fetched", ct)
	}

	if len(page.Content) > rawReadCap {
		page.Content = page.Content[:rawReadCap]
		page.RawTruncated = true
	}
	page.Body = page.Content
	page.BodyDigest = digestBytes([]byte(page.Body))
	page.Content, page.Truncated = c.capContent(page.Body, len(body))
	page.Truncated = page.Truncated || rawTruncated
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return page, fmt.Errorf("fetch failed: status %d", resp.StatusCode)
	}
	return page, nil
}

// fetchResponse issues the GET over the default (HTTP/2-capable) client, then
// retries once over an HTTP/1.1-only client when the first attempt fails at
// the transport layer or comes back with a retryable block status. Public
// bot-protection layers routinely reset HTTP/2 streams for non-browser
// clients (surfacing as "stream error: …"), and an HTTP/1.1 retry with a
// browser User-Agent clears a large fraction of those without weakening any
// guardrail — the SSRF-checked dialer and redirect policy are shared by both
// clients.
func (c *Client) fetchResponse(ctx context.Context, u *url.URL, headers http.Header) (*http.Response, error) {
	resp, err := c.doFetch(ctx, c.http, u, headers)
	if !c.shouldRetryHTTP1(ctx, resp, err) {
		return resp, err
	}
	if resp != nil {
		resp.Body.Close()
	}
	return c.doFetch(ctx, c.httpH1, u, headers)
}

func (c *Client) doFetch(ctx context.Context, client *http.Client, u *url.URL, headers http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/*;q=0.9,application/json;q=0.8,*/*;q=0.1")
	req.Header.Set("Accept-Language", acceptLanguage)
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	// Validators describe the originally requested representation. Do not send
	// them to a different redirect target, where an unrelated resource could
	// incorrectly answer 304.
	copyClient := *client
	checkRedirect := client.CheckRedirect
	copyClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > 0 && (next.URL.Scheme != u.Scheme || !strings.EqualFold(next.URL.Host, u.Host)) {
			next.Header.Del("If-None-Match")
			next.Header.Del("If-Modified-Since")
		}
		if checkRedirect != nil {
			return checkRedirect(next, via)
		}
		return nil
	}
	return copyClient.Do(req)
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cachePolicy(header http.Header, now time.Time, requestedMaxAge time.Duration) (bool, time.Time) {
	control := strings.ToLower(header.Get("Cache-Control"))
	if strings.Contains(control, "no-store") {
		return true, time.Time{}
	}
	// A zero caller budget is an explicit request to disable fresh-hit reuse.
	// The body is still returned and may be retained for an explicit cached
	// read, but this response must not silently satisfy a later auto fetch.
	if requestedMaxAge <= 0 {
		return false, time.Time{}
	}
	maxAge := requestedMaxAge
	if age, err := strconv.Atoi(strings.TrimSpace(header.Get("Age"))); err == nil && age > 0 {
		maxAge -= time.Duration(age) * time.Second
	}
	for _, directive := range strings.Split(control, ",") {
		parts := strings.SplitN(strings.TrimSpace(directive), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) == "max-age" {
			if seconds, err := time.ParseDuration(strings.TrimSpace(parts[1]) + "s"); err == nil && seconds >= 0 {
				maxAge = seconds
			}
		}
	}
	if strings.TrimSpace(header.Get("Vary")) == "*" || maxAge <= 0 {
		return false, time.Time{}
	}
	base := now
	if date, err := http.ParseTime(header.Get("Date")); err == nil && date.After(now.Add(-24*time.Hour)) {
		base = date
	}
	return false, base.Add(maxAge)
}

// shouldRetryHTTP1 reports whether the HTTP/1.1 fallback is worth trying. It
// never retries once the caller's context is done (the deadline covers both
// attempts) or when the failure is a deliberate guardrail rejection.
func (c *Client) shouldRetryHTTP1(ctx context.Context, resp *http.Response, err error) bool {
	if c.httpH1 == nil || ctx.Err() != nil {
		return false
	}
	if err != nil {
		return !errors.Is(err, errBlockedAddress) && !errors.Is(err, errTooManyRedirects)
	}
	return retryableStatuses[resp.StatusCode]
}

// htmlToMarkdown extracts the readable article and converts it to Markdown,
// falling back to a plain-text strip when extraction fails.
func htmlToMarkdown(body []byte, u *url.URL) (title, content string) {
	article, err := readability.FromReader(bytes.NewReader(body), u)
	if err == nil && strings.TrimSpace(article.Content) != "" {
		if md, mdErr := htmltomarkdown.ConvertString(article.Content); mdErr == nil && strings.TrimSpace(md) != "" {
			return article.Title, md
		}
	}
	return article.Title, stripText(body)
}

// stripText walks the HTML and emits its visible text.
func stripText(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return string(body)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "iframe":
				return
			}
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				b.WriteString(t + "\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}

// capContent truncates model-facing content to maxPageKB.
func (c *Client) capContent(content string, rawBytes int) (string, bool) {
	limit := c.maxPageKB * 1024
	if len(content) <= limit {
		return content, false
	}
	prefix, _ := terminaltext.TruncateBytes(content, limit)
	return prefix + fmt.Sprintf("\n… truncated (%d KB of %d bytes shown)", c.maxPageKB, rawBytes), true
}
